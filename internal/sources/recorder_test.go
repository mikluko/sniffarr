package sources

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"sarak.sh/mikluko/libarr/database"
	"sarak.sh/mikluko/libarr/torznab"

	"sarak.sh/mikluko/sniffarr/internal/rules"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

var t0 = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

// fakeQueue records the discovery of each infohash queued.
type fakeQueue struct {
	mu  sync.Mutex
	got map[store.Hash]string
}

func (q *fakeQueue) Enqueue(_ context.Context, sightings []store.Sighting) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, g := range sightings {
		q.got[g.Hash] = g.Discovery
	}
	return len(sightings), nil
}

func (q *fakeQueue) queued() map[store.Hash]string {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make(map[store.Hash]string, len(q.got))
	for h, d := range q.got {
		out[h] = d
	}
	return out
}

type fixture struct {
	store  *store.Store
	rec    *Recorder
	queue  *fakeQueue
	reader *sdkmetric.ManualReader
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{Service: "sniffarr"})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	s, err := store.New(ctx, db, store.Options{})
	require.NoError(t, err)
	f := &fixture{store: s, queue: &fakeQueue{got: map[store.Hash]string{}}, reader: sdkmetric.NewManualReader()}
	f.rec = &Recorder{Store: s, Fetches: f.queue, Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return t0 },
		Meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(f.reader))}
	return f
}

// sums returns the counters by name and attribute values.
func (f *fixture) sums(t *testing.T) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, f.reader.Collect(context.Background(), &rm))
	sums := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				k := m.Name
				for _, kv := range dp.Attributes.ToSlice() {
					k += " " + kv.Value.AsString()
				}
				sums[k] += dp.Value
			}
		}
	}
	return sums
}

func journal(t *testing.T, s *store.Store, h store.Hash) []store.JournalEntry {
	t.Helper()
	got, err := s.Journal(context.Background(), h, 100)
	require.NoError(t, err)
	return got
}

func TestRecord(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	_, _, err := f.store.Insert(ctx, store.Torrent{Hash: store.Hash{1}, Name: "Alien.1979.1080p.BluRay", Size: 8 << 30, Discovery: store.Announce, SeenAt: t0})
	require.NoError(t, err)
	_, err = f.store.Reject(ctx, store.Hash{2}, rules.Software, "setup.exe", 1, t0)
	require.NoError(t, err)

	for b, want := range map[byte]string{1: Stored, 2: Rejected, 3: Queued} {
		got, err := f.rec.Record(ctx, store.Listing{Source: "spidarr", Hash: store.Hash{b}, Title: "Alien", Comments: "https://x/1",
			Categories: []int{2040}, Size: 42, IMDb: "tt0078748", PublishedAt: t0.Add(-time.Hour)})
		require.NoError(t, err)
		require.Equal(t, want, got, "hash %d", b)
	}
	got, err := f.rec.Record(ctx, store.Listing{Source: "spidarr", Hash: store.Hash{3}, Title: "Alien"})
	require.NoError(t, err)
	require.Equal(t, Duplicate, got)

	require.Equal(t, map[store.Hash]string{{3}: store.Torznab}, f.queue.queued(), "only what the store lacks is queued, under torznab")
	e := journal(t, f.store, store.Hash{3})
	require.Len(t, e, 1, "a listing again is not journaled")
	require.Equal(t, store.JournalListed, e[0].Kind)
	require.Equal(t, Queued, e[0].Outcome)
	require.JSONEq(t, `{"source":"spidarr","title":"Alien","url":"https://x/1","categories":[2040],"size":42,"published_at":`+
		strconv.FormatInt(t0.Add(-time.Hour).Unix(), 10)+`,"imdb":"tt0078748"}`, e[0].Detail)
	require.Equal(t, Rejected, journal(t, f.store, store.Hash{2})[0].Outcome)
	require.Equal(t, map[string]int64{
		"sniffarr.source.listings spidarr stored":    1,
		"sniffarr.source.listings spidarr rejected":  1,
		"sniffarr.source.listings spidarr queued":    1,
		"sniffarr.source.listings spidarr duplicate": 1,
	}, f.sums(t))
}

func TestRequestResults(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.rec.request(ctx, "spidarr", nil)
	f.rec.request(ctx, "spidarr", &torznab.Error{Code: 100})
	f.rec.request(ctx, "spidarr", errors.New("502 Bad Gateway"))
	require.Equal(t, map[string]int64{
		"sniffarr.source.requests spidarr ok":      1,
		"sniffarr.source.requests spidarr refused": 1,
		"sniffarr.source.requests spidarr failed":  1,
	}, f.sums(t))
}
