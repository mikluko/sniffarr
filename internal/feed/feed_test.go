package feed

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
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
	mu    sync.Mutex
	items []Item
	err   error
	calls int
	state State // the last one Fetch was handed
}

func (s *fakeSource) Name() string { return "fake" }
func (s *fakeSource) Fetch(_ context.Context, state State) ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.state = state
	return s.items, s.err
}

// The state a source is handed is its own, kept in the store across
// pollers and so across restarts.
func TestState(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	src := &fakeSource{}
	require.NoError(t, (&Poller{Source: src, Recorder: f.rec, Log: slog.New(slog.DiscardHandler)}).Round(ctx))
	done, err := src.state.Done(ctx, "forum:1457")
	require.NoError(t, err)
	require.False(t, done)
	require.NoError(t, src.state.Mark(ctx, "forum:1457"))
	require.NoError(t, src.state.Mark(ctx, "forum:1457"), "marking twice is fine")

	again := &fakeSource{}
	require.NoError(t, (&Poller{Source: again, Recorder: f.rec, Log: slog.New(slog.DiscardHandler)}).Round(ctx))
	done, err = again.state.Done(ctx, "forum:1457")
	require.NoError(t, err)
	require.True(t, done, "another poller of the same source sees the mark")
	done, err = storeState{store: f.store, source: "other", now: f.rec.now}.Done(ctx, "forum:1457")
	require.NoError(t, err)
	require.False(t, done, "another source does not")
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
	off := Offset("fake", time.Hour)
	require.Greater(t, off, time.Duration(0))
	require.Less(t, off, time.Hour)
	require.Equal(t, off, Offset("fake", time.Hour), "fixed by the name")
	require.NotEqual(t, off, Offset("rutracker", time.Hour))

	at := time.Date(2026, 11, 6, 14, 0, 0, 0, time.UTC)
	require.Equal(t, at.Add(off), p.Next(at), "the first round of the hour is still ahead")
	require.Equal(t, at.Add(time.Hour+off), p.Next(at.Add(off)), "a round at its moment is the next hour's")
	require.Equal(t, at.Add(time.Hour+off), p.Next(at.Add(off+time.Minute)), "past it, the next hour's")
	require.Equal(t, time.Duration(0), Offset("fake", time.Second), "nothing to spread over")
}
