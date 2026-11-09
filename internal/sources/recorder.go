package sources

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"sarak.sh/mikluko/sniffarr/internal/store"
)

const scope = "sarak.sh/mikluko/sniffarr/internal/sources"

// Outcomes of Recorder.Record.
const (
	// Stored is a new listing whose torrent the store has.
	Stored = "stored"
	// Queued is a new listing whose infohash was queued to be fetched.
	Queued = "queued"
	// Rejected is a new listing whose torrent is rejected, so not queued.
	Rejected = "rejected"
	// Duplicate is a listing recorded before.
	Duplicate = "duplicate"
)

// Enqueuer queues a fetch of each sighted infohash it keeps; *ingest.Service
// is one.
type Enqueuer interface {
	Enqueue(ctx context.Context, sightings []store.Sighting) (int, error)
}

// Recorder records listings in the store, queues the infohashes of their
// torrents that are not stored, and counts them. It is safe for concurrent
// use.
type Recorder struct {
	Store *store.Store
	// Fetches queues the fetches of the infohashes.
	Fetches Enqueuer
	Log     *slog.Logger
	Now     func() time.Time
	// Meter records metrics; the global MeterProvider when nil.
	Meter metric.MeterProvider

	once     sync.Once
	listings metric.Int64Counter
	requests metric.Int64Counter
}

func (r *Recorder) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Record records the listing and returns its outcome. The infohash of a new
// listing whose torrent is neither stored nor rejected is queued under
// store.Torznab. The outcome of a new listing is journaled once it is
// recorded, in a transaction of its own; a failure to journal it is logged,
// not returned.
func (r *Recorder) Record(ctx context.Context, l store.Listing) (string, error) {
	r.init()
	now := r.now()
	rec, err := r.Store.RecordListing(ctx, l, now)
	if err != nil {
		return "", fmt.Errorf("record listing: %w", err)
	}
	outcome, err := r.effect(ctx, rec, l.Hash)
	if err != nil {
		return "", err
	}
	if rec.New {
		if err := r.Store.JournalListed(ctx, l.Hash, now, outcome, store.Detail("source", l.Source, "title", l.Title, "url", l.Comments,
			"categories", l.Categories, "size", l.Size, "published_at", l.PublishedAt, "imdb", l.IMDb, "tmdb_id", l.TMDBID, "tvdb_id", l.TVDBID)); err != nil {
			r.Log.ErrorContext(ctx, "journal listing", "source", l.Source, "infohash", hex.EncodeToString(l.Hash[:]), "err", err)
		}
	}
	r.listings.Add(ctx, 1, metric.WithAttributes(attribute.String("sniffarr.source.name", l.Source), attribute.String("sniffarr.source.outcome", outcome)))
	return outcome, nil
}

// effect queues the infohash of a new listing and returns its outcome.
func (r *Recorder) effect(ctx context.Context, rec store.ListingRecorded, h store.Hash) (string, error) {
	switch {
	case !rec.New:
		return Duplicate, nil
	case rec.Stored:
		return Stored, nil
	}
	held, err := r.Store.Screen(ctx, []store.Hash{h}, r.now())
	if err != nil {
		return "", fmt.Errorf("screen: %w", err)
	}
	if held[h].Rejected {
		return Rejected, nil
	}
	if _, err := r.Fetches.Enqueue(ctx, []store.Sighting{{Hash: h, Discovery: store.Torznab}}); err != nil {
		return "", fmt.Errorf("enqueue: %w", err)
	}
	return Queued, nil
}

// Results of a request to a source, as metrics name them.
const (
	// ResultOK is a Torznab feed.
	ResultOK = "ok"
	// ResultRefused is a Newznab error, as for credentials it does not take.
	ResultRefused = "refused"
	// ResultFailed is no answer, a status other than 2xx, or an answer that
	// is neither a feed nor a Newznab error.
	ResultFailed = "failed"
)

// request counts a request to the source that ended in err.
func (r *Recorder) request(ctx context.Context, source string, err error) {
	r.init()
	result := ResultOK
	switch {
	case isRefusal(err):
		result = ResultRefused
	case err != nil:
		result = ResultFailed
	}
	r.requests.Add(ctx, 1, metric.WithAttributes(attribute.String("sniffarr.source.name", source), attribute.String("sniffarr.source.result", result)))
}

func (r *Recorder) init() {
	r.once.Do(func() {
		mp := r.Meter
		if mp == nil {
			mp = otel.GetMeterProvider()
		}
		m := mp.Meter(scope)
		var err error
		if r.listings, err = m.Int64Counter("sniffarr.source.listings", metric.WithUnit("{listing}"),
			metric.WithDescription("Listings Torznab sources returned, by source and what came of them.")); err != nil {
			otel.Handle(err)
		}
		if r.requests, err = m.Int64Counter("sniffarr.source.requests", metric.WithUnit("{request}"),
			metric.WithDescription("Requests for the latest listings of Torznab sources, by source and result.")); err != nil {
			otel.Handle(err)
		}
	})
}
