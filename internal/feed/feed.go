// Package feed takes in the releases that tracker feeds list: each one's
// infohash queued to be fetched when the store lacks it, its title and page
// kept beside the torrent. A Source is one feed; a Poller reads it on a
// schedule and a Recorder keeps what it lists.
package feed

import (
	"context"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/mikluko/sniffarr/internal/store"
	"github.com/mikluko/sniffarr/internal/telemetry"
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
	// What the source did once and need not do again, a page read or a
	// pass made, it marks in state.
	Fetch(ctx context.Context, state State) ([]Item, error)
}

// State keeps, per source and across restarts, the keys a source marked
// done.
type State interface {
	Done(ctx context.Context, key string) (bool, error)
	Mark(ctx context.Context, key string) error
}

// storeState is the State of one source in the store.
type storeState struct {
	store  *store.Store
	source string
	now    func() time.Time
}

func (s storeState) Done(ctx context.Context, key string) (bool, error) {
	return s.store.FeedDone(ctx, s.source, key)
}

func (s storeState) Mark(ctx context.Context, key string) error {
	return s.store.MarkFeedDone(ctx, s.source, key, s.now())
}

// Client returns c, or http.DefaultClient for nil, with each request traced
// as a client span.
func Client(c *http.Client) *http.Client {
	if c == nil {
		c = http.DefaultClient
	}
	return telemetry.HTTPClient(c)
}

// Sleep waits d, or until ctx is done.
func Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Infohash reads the v1 infohash of a magnet link, in either case.
func Infohash(magnet string) (store.Hash, bool) {
	var h store.Hash
	u, err := url.Parse(magnet)
	if err != nil || u.Scheme != "magnet" {
		return h, false
	}
	for _, xt := range u.Query()["xt"] {
		hexed, ok := strings.CutPrefix(xt, "urn:btih:")
		if !ok || len(hexed) != 2*len(h) {
			continue
		}
		if _, err := hex.Decode(h[:], []byte(hexed)); err == nil {
			return h, true
		}
	}
	return h, false
}

// Bytes is n of the binary unit, "GB" or "ГБ"; 0 for a unit it does not
// know.
func Bytes(n float64, unit string) int64 {
	switch strings.ToUpper(unit) {
	case "B", "Б":
		return int64(n)
	case "KB", "КБ":
		return int64(n * (1 << 10))
	case "MB", "МБ":
		return int64(n * (1 << 20))
	case "GB", "ГБ":
		return int64(n * (1 << 30))
	case "TB", "ТБ":
		return int64(n * (1 << 40))
	}
	return 0
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

// Poller records what Source lists, at start and then once an Interval, at
// the moment of the interval Offset names. Zero fields take the default
// named in their doc.
type Poller struct {
	Source   Source
	Recorder *Recorder
	// Interval is how often Run reads the feed; DefaultInterval. Retry is
	// how long it waits after a read that failed, at most until the next
	// round; DefaultRetry.
	Interval time.Duration
	Retry    time.Duration
	Log      *slog.Logger
	// Now is time.Now when nil.
	Now func() time.Time
}

// Run reads the feed at once, then at every Interval of wall-clock time
// plus the source's Offset, until ctx is done. The rounds fall at the same
// moments across restarts, and sources with different names at different
// ones.
func (p *Poller) Run(ctx context.Context) error {
	for {
		now := p.now()
		next := p.Next(now)
		if err := p.Round(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			p.Log.ErrorContext(ctx, "read feed", "source", p.Source.Name(), "err", err)
			if retry := p.now().Add(p.retry()); retry.Before(next) {
				next = retry
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Until(next)):
		}
	}
}

// Next is the first moment after now that falls on an Interval of
// wall-clock time plus Offset(Source.Name()).
func (p *Poller) Next(now time.Time) time.Time {
	interval := p.interval()
	next := now.Truncate(interval).Add(Offset(p.Source.Name(), interval))
	for !next.After(now) {
		next = next.Add(interval)
	}
	return next
}

// Offset is the share of interval the rounds of the source name fall at:
// fixed by the name, never zero for an interval over a second, so no source
// reads on the hour and two sources read at different moments.
func Offset(name string, interval time.Duration) time.Duration {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	if interval <= time.Second {
		return 0
	}
	return time.Second + time.Duration(h.Sum32())%(interval-time.Second)
}

func (p *Poller) interval() time.Duration {
	if p.Interval > 0 {
		return p.Interval
	}
	return DefaultInterval
}

func (p *Poller) retry() time.Duration {
	if p.Retry > 0 {
		return p.Retry
	}
	return DefaultRetry
}

func (p *Poller) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Round reads the feed once and records every item, logging how many were
// new.
func (p *Poller) Round(ctx context.Context) error {
	items, err := p.Source.Fetch(ctx, storeState{store: p.Recorder.Store, source: p.Source.Name(), now: p.Recorder.now})
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
