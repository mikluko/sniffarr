// Package arr takes in what Radarr and Sonarr grab and import, by their
// webhook and from their history API, as demand for works and as matches
// of releases to works surer than TMDB's search: Radarr and Sonarr know
// which movie or series they grabbed a release as.
package arr

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"sarak.sh/mikluko/sniffarr/internal/store"
)

const scope = "sarak.sh/mikluko/sniffarr/internal/arr"

// Event is a grab or an import as Radarr or Sonarr tells it.
type Event struct {
	// App is store.AppRadarr or store.AppSonarr; Kind is store.EventGrab or
	// store.EventImport.
	App  string
	Kind string
	// DownloadID is the download client's ID of the download: a torrent
	// client's is the infohash in hex.
	DownloadID string
	// InfoHash is the torrent's infohash in hex where DownloadID is not it,
	// or "".
	InfoHash string
	// TMDBID, TVDBID and IMDbID name the work; 0 and "" name none.
	TMDBID int
	TVDBID int
	IMDbID string
	// Title is the release's title.
	Title   string
	Indexer string
	Size    int64
	// Season and Episode are a series'; 0 when the event names none or
	// several.
	Season  int
	Episode int
	At      time.Time
}

// Outcomes of Recorder.Record.
const (
	// Matched is a new event whose stored release was given its work.
	Matched = "matched"
	// Queued is a new event whose infohash was queued to be fetched.
	Queued = "queued"
	// Unknown is a new event that neither matched nor queued: it has no
	// infohash, no work, or its torrent is rejected.
	Unknown = "unknown"
	// Duplicate is an event recorded before.
	Duplicate = "duplicate"
)

// Sources of events, as metrics name them.
const (
	SourceWebhook = "webhook"
	SourceHistory = "history"
)

// Enqueuer queues a fetch of each sighted infohash it keeps; *ingest.Service
// is one.
type Enqueuer interface {
	Enqueue(ctx context.Context, sightings []store.Sighting) (int, error)
}

// Finder looks the TMDB ID of a series up by its TVDB ID; *tmdb.Client is
// one.
type Finder interface {
	FindTVDB(ctx context.Context, tvdbID int) (int, error)
}

// Recorder records events in the store, queues the infohashes of their
// torrents that are not stored, and counts them. It is safe for concurrent
// use.
type Recorder struct {
	Store *store.Store
	// Fetches queues the fetches of the infohashes.
	Fetches Enqueuer
	// Finder, when set, finds the TMDB ID of a series Sonarr names by its
	// TVDB ID only, unless a stored work has the TVDB ID.
	Finder Finder
	Log    *slog.Logger
	Now    func() time.Time
	// Meter records metrics; the global MeterProvider when nil.
	Meter metric.MeterProvider

	once     sync.Once
	events   metric.Int64Counter
	outcomes metric.Int64Counter
}

func (r *Recorder) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Record records the event from the source as store.RecordArr does and
// returns its outcome. The infohash of a new event's torrent that is
// neither stored nor rejected is queued under store.Arr. A TVDB ID that
// cannot be looked up leaves the event without a work.
func (r *Recorder) Record(ctx context.Context, source string, e Event) (string, error) {
	r.count(ctx, source, e)
	typ := store.TypeMovie
	if e.App == store.AppSonarr {
		typ = store.TypeTV
	}
	tmdbID := e.TMDBID
	if tmdbID == 0 && e.TVDBID != 0 && typ == store.TypeTV {
		var err error
		if tmdbID, err = r.findTVDB(ctx, e.TVDBID); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			r.Log.WarnContext(ctx, "find series by tvdb id", "tvdb", e.TVDBID, "err", err)
		}
	}
	var ids []store.ExternalID
	if e.IMDbID != "" {
		ids = append(ids, store.ExternalID{Provider: store.ProviderIMDb, ID: e.IMDbID})
	}
	if e.TVDBID != 0 && typ == store.TypeTV {
		ids = append(ids, store.ExternalID{Provider: store.ProviderTVDB, ID: strconv.Itoa(e.TVDBID)})
	}
	h, ok := infohash(e.DownloadID)
	if !ok {
		h, ok = infohash(e.InfoHash)
	}
	se := store.ArrEvent{App: e.App, Event: e.Kind, Source: source, Key: key(e), Type: typ, TMDBID: tmdbID, IDs: ids,
		Title: e.Title, Indexer: e.Indexer, Size: e.Size, Season: e.Season, Episode: e.Episode, At: e.At}
	if ok {
		se.Hash = &h
	}
	rec, err := r.Store.RecordArr(ctx, se, r.now())
	if err != nil {
		return "", fmt.Errorf("record event: %w", err)
	}
	outcome, err := r.effect(ctx, rec, se.Hash)
	if err != nil {
		return "", err
	}
	r.outcomes.Add(ctx, 1, metric.WithAttributes(attribute.String("sniffarr.arr.app", e.App), attribute.String("sniffarr.arr.outcome", outcome)))
	return outcome, nil
}

// effect queues the hash of a new event whose torrent is not stored, unless
// it is rejected, and returns the event's outcome.
func (r *Recorder) effect(ctx context.Context, rec store.ArrRecorded, h *store.Hash) (string, error) {
	switch {
	case !rec.New:
		return Duplicate, nil
	case h == nil:
		return Unknown, nil
	case rec.Stored && rec.Work != 0:
		return Matched, nil
	case rec.Stored:
		return Unknown, nil
	}
	_, rejected, err := r.Store.Known(ctx, []store.Hash{*h}, r.now())
	if err != nil {
		return "", fmt.Errorf("known: %w", err)
	}
	if rejected[*h] {
		return Unknown, nil
	}
	if _, err := r.Fetches.Enqueue(ctx, []store.Sighting{{Hash: *h, Discovery: store.Arr}}); err != nil {
		return "", fmt.Errorf("enqueue: %w", err)
	}
	return Queued, nil
}

// findTVDB returns the TMDB ID of the series with the TVDB ID, from a
// stored work or else from the Finder; 0 when neither knows it.
func (r *Recorder) findTVDB(ctx context.Context, tvdbID int) (int, error) {
	id, err := r.Store.TMDBByExternal(ctx, store.TypeTV, store.ProviderTVDB, strconv.Itoa(tvdbID))
	switch {
	case err == nil:
		return id, nil
	case !errors.Is(err, store.ErrNotFound):
		return 0, err
	case r.Finder == nil:
		return 0, nil
	}
	return r.Finder.FindTVDB(ctx, tvdbID)
}

// key is the event's store.ArrEvent.Key: its download ID in lower case, or,
// without one, its title and time, which no other report of the download
// shares.
func key(e Event) string {
	if e.DownloadID != "" {
		return strings.ToLower(e.DownloadID)
	}
	return "title:" + e.Title + "@" + strconv.FormatInt(e.At.Unix(), 10)
}

// infohash reads a BitTorrent v1 infohash in hex, in either case.
func infohash(s string) (store.Hash, bool) {
	var h store.Hash
	if len(s) != 2*len(h) {
		return h, false
	}
	_, err := hex.Decode(h[:], []byte(s))
	return h, err == nil
}

func (r *Recorder) count(ctx context.Context, source string, e Event) {
	r.once.Do(func() {
		mp := r.Meter
		if mp == nil {
			mp = otel.GetMeterProvider()
		}
		m := mp.Meter(scope)
		var err error
		if r.events, err = m.Int64Counter("sniffarr.arr.events", metric.WithUnit("{event}"),
			metric.WithDescription("Grabs and imports Radarr and Sonarr reported, by app, event and source.")); err != nil {
			otel.Handle(err)
		}
		if r.outcomes, err = m.Int64Counter("sniffarr.arr.outcomes", metric.WithUnit("{event}"),
			metric.WithDescription("Grabs and imports Radarr and Sonarr reported, by app and what came of them.")); err != nil {
			otel.Handle(err)
		}
	})
	r.events.Add(ctx, 1, metric.WithAttributes(attribute.String("sniffarr.arr.app", e.App), attribute.String("sniffarr.arr.event", e.Kind),
		attribute.String("sniffarr.arr.source", source)))
}
