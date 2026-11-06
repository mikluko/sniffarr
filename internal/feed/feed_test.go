package feed

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/mikluko/sniffarr/internal/database"
	"github.com/mikluko/sniffarr/internal/rules"
	"github.com/mikluko/sniffarr/internal/store"
)

type fixture struct {
	store  *store.Store
	queue  *store.Queue
	rec    *Recorder
	reader *sdkmetric.ManualReader
	now    time.Time
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	s, err := store.New(ctx, db, store.Options{})
	require.NoError(t, err)
	q, err := store.NewQueue(ctx, db, store.QueueOptions{})
	require.NoError(t, err)
	f := &fixture{store: s, queue: q, reader: sdkmetric.NewManualReader(), now: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}
	f.rec = &Recorder{Store: s, Queue: q, Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return f.now },
		Meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(f.reader))}
	return f
}

func hash(b byte) store.Hash {
	var h store.Hash
	h[0] = b
	return h
}

// items counts the sniffarr.feed.items metric by outcome.
func (f *fixture) items(t *testing.T) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, f.reader.Collect(context.Background(), &rm))
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "sniffarr.feed.items" {
				continue
			}
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				v, _ := dp.Attributes.Value("sniffarr.feed.outcome")
				out[v.AsString()] += dp.Value
			}
		}
	}
	return out
}

func TestRecord(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	_, _, err := f.store.Insert(ctx, store.Torrent{Hash: hash(1), Name: "Alien.1979.mkv", Size: 1 << 30, Discovery: store.Announce, SeenAt: f.now,
		Files: []rules.File{{Path: "Alien.1979.mkv", Size: 1 << 30}}})
	require.NoError(t, err)
	_, err = f.store.Reject(ctx, hash(2), rules.Small, f.now)
	require.NoError(t, err)

	item := func(b byte) Item {
		return Item{Hash: hash(b), Title: "Title " + string(rune('0'+b)), URL: "https://example.com/t/" + string(rune('0'+b)), Category: "f-1457", Size: 1 << 30, At: f.now.Add(-time.Hour)}
	}
	outcome, err := f.rec.Record(ctx, "rutracker", item(1))
	require.NoError(t, err)
	require.Equal(t, Stored, outcome, "the torrent is stored")
	outcome, err = f.rec.Record(ctx, "rutracker", item(2))
	require.NoError(t, err)
	require.Equal(t, Rejected, outcome, "the torrent is rejected, not queued")
	outcome, err = f.rec.Record(ctx, "rutracker", item(3))
	require.NoError(t, err)
	require.Equal(t, Queued, outcome)
	d, err := f.queue.Discovery(ctx, hash(3))
	require.NoError(t, err)
	require.Equal(t, store.Feed, d, "queued under the feed discovery")
	_, err = f.queue.Discovery(ctx, hash(2))
	require.ErrorIs(t, err, store.ErrNotQueued)
	outcome, err = f.rec.Record(ctx, "rutracker", item(3))
	require.NoError(t, err)
	require.Equal(t, Duplicate, outcome, "the same item again")
	outcome, err = f.rec.Record(ctx, "other", item(3))
	require.NoError(t, err)
	require.Equal(t, Queued, outcome, "another source's item of the hash is new, and the hash already queued")

	got, err := f.store.FeedItems(ctx, hash(3))
	require.NoError(t, err)
	require.Equal(t, []store.FeedItem{
		{Source: "other", Hash: hash(3), Title: "Title 3", URL: "https://example.com/t/3", Category: "f-1457", Size: 1 << 30, PublishedAt: f.now.Add(-time.Hour)},
		{Source: "rutracker", Hash: hash(3), Title: "Title 3", URL: "https://example.com/t/3", Category: "f-1457", Size: 1 << 30, PublishedAt: f.now.Add(-time.Hour)},
	}, got)
	require.Equal(t, map[string]int64{Stored: 1, Rejected: 1, Queued: 2, Duplicate: 1}, f.items(t))
}

// fakeSource lists what it is given, or fails.
type fakeSource struct {
	mu      sync.Mutex
	items   []Item
	err     error
	calls   int
	crawler Crawler // the last one Fetch was handed
}

func (s *fakeSource) Name() string { return "fake" }
func (s *fakeSource) Fetch(_ context.Context, c Crawler) ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.crawler = c
	return s.items, s.err
}

// The crawler a source is handed records every fetch in the store, asks
// only for what changed, fetches a page once on request, and a request at
// most every Pause.
func TestCrawler(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/feed":
			if r.Header.Get("If-None-Match") == `"v1"` && r.Header.Get("If-Modified-Since") == "Thu, 05 Nov 2026 18:30:55 GMT" {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Last-Modified", "Thu, 05 Nov 2026 18:30:55 GMT")
			_, _ = w.Write([]byte("feed"))
		case "/page":
			_, _ = w.Write([]byte("page"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	src := &fakeSource{}
	p := &Poller{Source: src, Recorder: f.rec, HTTP: srv.Client(), Now: func() time.Time { return f.now }, Log: slog.New(slog.DiscardHandler)}
	require.NoError(t, p.Round(ctx))
	c := src.crawler

	body, ok, err := c.Fetch(ctx, srv.URL+"/feed")
	require.NoError(t, err)
	require.True(t, ok)
	b, _ := io.ReadAll(body)
	require.NoError(t, body.Close())
	require.Equal(t, "feed", string(b))
	rec, found, err := f.store.CrawlState(ctx, "fake", srv.URL+"/feed")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, store.Crawl{Source: "fake", URL: srv.URL + "/feed", CreatedAt: f.now, UpdatedAt: f.now, Status: 200, ETag: `"v1"`, LastModified: "Thu, 05 Nov 2026 18:30:55 GMT"}, rec)

	f.now = f.now.Add(time.Hour)
	_, ok, err = c.Fetch(ctx, srv.URL+"/feed")
	require.NoError(t, err)
	require.False(t, ok, "unchanged since: the validators went back and 304 came")
	rec, _, err = f.store.CrawlState(ctx, "fake", srv.URL+"/feed")
	require.NoError(t, err)
	require.Equal(t, 304, rec.Status)
	require.Equal(t, `"v1"`, rec.ETag, "the validators kept")
	require.Equal(t, f.now, rec.UpdatedAt)
	require.Equal(t, f.now.Add(-time.Hour), rec.CreatedAt)

	body, ok, err = c.Once(ctx, srv.URL+"/page")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, body.Close())
	_, ok, err = c.Once(ctx, srv.URL+"/page")
	require.NoError(t, err)
	require.False(t, ok, "fetched before")

	_, _, err = c.Once(ctx, srv.URL+"/gone")
	require.ErrorContains(t, err, "404")
	rec, _, err = f.store.CrawlState(ctx, "fake", srv.URL+"/gone")
	require.NoError(t, err)
	require.Equal(t, 404, rec.Status)
	require.Contains(t, rec.Error, "404")
	_, _, err = c.Once(ctx, srv.URL+"/gone")
	require.ErrorContains(t, err, "404", "a failed fetch is tried again")
	require.Equal(t, int32(5), hits.Load())
}

// Two requests of one source are a Pause apart.
func TestCrawlerPause(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	src := &fakeSource{}
	p := &Poller{Source: src, Recorder: f.rec, HTTP: srv.Client(), Log: slog.New(slog.DiscardHandler)}
	require.NoError(t, p.Round(ctx))
	start := time.Now()
	for range 2 {
		body, _, err := src.crawler.Fetch(ctx, srv.URL)
		require.NoError(t, err)
		require.NoError(t, body.Close())
	}
	require.GreaterOrEqual(t, time.Since(start), Pause)
}

func (s *fakeSource) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *fakeSource) called() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestPoller(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	src := &fakeSource{items: []Item{{Hash: hash(5), Title: "Five", At: f.now}, {Hash: hash(6), Title: "Six", At: f.now}}}
	p := &Poller{Source: src, Recorder: f.rec, Log: slog.New(slog.DiscardHandler)}
	require.NoError(t, p.Round(ctx))
	require.NoError(t, p.Round(ctx))
	require.Equal(t, map[string]int64{Queued: 2, Duplicate: 2}, f.items(t), "the window read twice records each item once")

	src.fail(errors.New("502 Bad Gateway"))
	require.EqualError(t, p.Round(ctx), "502 Bad Gateway")

	// Run reads at start and then waits; a done context ends it.
	src.fail(nil)
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	calls := src.called()
	go func() { done <- (&Poller{Source: src, Recorder: f.rec, Interval: time.Hour, Log: p.Log}).Run(ctx) }()
	require.Eventually(t, func() bool { return src.called() > calls }, 5*time.Second, 10*time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

// Rounds fall on wall-clock intervals plus an offset the source's name
// fixes, so a restart does not move them and no source reads on the hour.
func TestNext(t *testing.T) {
	p := &Poller{Source: &fakeSource{}, Interval: time.Hour}
	off := Offset("fake", nil, time.Hour)
	require.Equal(t, 48*time.Minute+4*time.Second, off, "whole seconds, spread over the hour, fixed by the name")
	require.Equal(t, 22*time.Minute+4*time.Second, Offset("rutracker", nil, time.Hour))
	require.Equal(t, 55*time.Minute+32*time.Second, Offset("nnmclub", nil, time.Hour))
	require.Equal(t, 52*time.Minute+17*time.Second, Offset("rutor", nil, time.Hour))
	require.Equal(t, 12*time.Minute+17*time.Second, Offset("fake", []byte("another node"), time.Hour), "and by the node")

	at := time.Date(2026, 11, 6, 14, 0, 0, 0, time.UTC)
	require.Equal(t, at.Add(off), p.Next(at), "the first round of the hour is still ahead")
	require.Equal(t, at.Add(time.Hour+off), p.Next(at.Add(off)), "a round at its moment is the next hour's")
	require.Equal(t, at.Add(time.Hour+off), p.Next(at.Add(off+time.Minute)), "past it, the next hour's")
	require.Equal(t, time.Duration(0), Offset("fake", nil, 500*time.Millisecond), "nothing to spread over")
}
