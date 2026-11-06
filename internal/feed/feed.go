// Package feed takes in the releases that tracker feeds list: each one's
// infohash queued to be fetched when the store lacks it, its title and page
// kept beside the torrent. A Source is one feed; a Poller reads it on a
// schedule and a Recorder keeps what it lists.
package feed

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/mikluko/sniffarr/internal/store"
)

const scope = "github.com/mikluko/sniffarr/internal/feed"

// Item is one release a feed lists.
type Item struct {
	Hash store.Hash
	// Title is the release as the feed titles it.
	Title string
	// URL is the release's page at the source, "" for none.
	URL string
	// Category is the source's category of the release, "" for none.
	Category string
	// Size is the release's size in bytes, 0 unknown.
	Size int64
	// At is when the source listed the release.
	At time.Time
}

// Source is one feed. Implementations live in packages under this one and
// are named in the configuration by Name.
type Source interface {
	// Name keys the source's items in the store and its metrics:
	// "rutracker".
	Name() string
	// Fetch returns the releases the feed lists at the moment. A feed is a
	// window, so Fetch returns the same release again until it leaves it.
	Fetch(ctx context.Context) ([]Item, error)
}

// Outcomes of Recorder.Record.
const (
	// Stored is a new item whose torrent the store has.
	Stored = "stored"
	// Queued is a new item whose infohash was queued to be fetched.
	Queued = "queued"
	// Rejected is a new item whose torrent is rejected, so not queued.
	Rejected = "rejected"
	// Duplicate is an item recorded before.
	Duplicate = "duplicate"
)

// Recorder records items in the store, queues the infohashes of their
// torrents that are not stored, and counts them. It is safe for concurrent
// use.
type Recorder struct {
	Store *store.Store
	Queue *store.Queue
	Log   *slog.Logger
	Now   func() time.Time
	// Meter records metrics; the global MeterProvider when nil.
	Meter metric.MeterProvider

	once  sync.Once
	items metric.Int64Counter
}

func (r *Recorder) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Record records the item of the source and returns its outcome. The
// infohash of a new item whose torrent is neither stored nor rejected is
// queued under store.Feed.
func (r *Recorder) Record(ctx context.Context, source string, it Item) (string, error) {
	r.init()
	rec, err := r.Store.RecordFeedItem(ctx, store.FeedItem{Source: source, Hash: it.Hash, Title: it.Title, URL: it.URL,
		Category: it.Category, Size: it.Size, PublishedAt: it.At}, r.now())
	if err != nil {
		return "", fmt.Errorf("record feed item: %w", err)
	}
	outcome, err := r.effect(ctx, rec, it.Hash)
	if err != nil {
		return "", err
	}
	r.items.Add(ctx, 1, metric.WithAttributes(attribute.String("sniffarr.feed.source", source), attribute.String("sniffarr.feed.outcome", outcome)))
	return outcome, nil
}

func (r *Recorder) effect(ctx context.Context, rec store.FeedRecorded, h store.Hash) (string, error) {
	switch {
	case !rec.New:
		return Duplicate, nil
	case rec.Stored:
		return Stored, nil
	}
	_, rejected, err := r.Store.Known(ctx, []store.Hash{h}, r.now())
	if err != nil {
		return "", fmt.Errorf("known: %w", err)
	}
	if rejected[h] {
		return Rejected, nil
	}
	if _, _, err := r.Queue.Enqueue(ctx, []store.Sighting{{Hash: h, Discovery: store.Feed}}, r.now()); err != nil {
		return "", fmt.Errorf("enqueue: %w", err)
	}
	return Queued, nil
}

func (r *Recorder) init() {
	r.once.Do(func() {
		mp := r.Meter
		if mp == nil {
			mp = otel.GetMeterProvider()
		}
		var err error
		if r.items, err = mp.Meter(scope).Int64Counter("sniffarr.feed.items", metric.WithUnit("{item}"),
			metric.WithDescription("Releases tracker feeds listed, by source and what came of them.")); err != nil {
			otel.Handle(err)
		}
	})
}

// Defaults of Poller.
const (
	DefaultInterval = time.Hour
	DefaultRetry    = 10 * time.Minute
)

// Poller records what Source lists, at start and every Interval. Zero
// fields take the default named in their doc.
type Poller struct {
	Source   Source
	Recorder *Recorder
	// Interval is how often Run reads the feed; DefaultInterval. Retry is
	// how long it waits after a read that failed; DefaultRetry.
	Interval time.Duration
	Retry    time.Duration
	Log      *slog.Logger
}

// Run reads the feed and records its items until ctx is done.
func (p *Poller) Run(ctx context.Context) error {
	interval, retry := p.Interval, p.Retry
	if interval <= 0 {
		interval = DefaultInterval
	}
	if retry <= 0 {
		retry = DefaultRetry
	}
	for {
		wait := interval
		if err := p.Round(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			p.Log.ErrorContext(ctx, "read feed", "source", p.Source.Name(), "err", err)
			wait = retry
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// Round reads the feed once and records every item, logging how many were
// new.
func (p *Poller) Round(ctx context.Context) error {
	items, err := p.Source.Fetch(ctx)
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for _, it := range items {
		outcome, err := p.Recorder.Record(ctx, p.Source.Name(), it)
		if err != nil {
			return err
		}
		counts[outcome]++
	}
	if len(items) > counts[Duplicate] {
		p.Log.InfoContext(ctx, "read feed", "source", p.Source.Name(), "items", len(items),
			"queued", counts[Queued], "stored", counts[Stored], "rejected", counts[Rejected])
	}
	return nil
}
