package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/mikluko/batcher"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"sarak.sh/mikluko/libarr/database"
)

// queueProbeOrder stores ten releases a probe reads, b 1 to 10 with b*10
// peers, requests probes of 1 and then 2 and a search of 3, and returns the
// hashes in the order LeaseProbe takes them.
func queueProbeOrder(t *testing.T, s *Store) []Hash {
	t.Helper()
	ctx := context.Background()
	for b := byte(1); b <= 10; b++ {
		insertProbeable(t, s, b+10, "Release", int64(b)*10, "Release.mkv")
	}
	_, _, err := s.RequestProbe(ctx, []Hash{hash(11)}, t0)
	require.NoError(t, err)
	_, _, err = s.RequestProbe(ctx, []Hash{hash(12)}, t0.Add(time.Second))
	require.NoError(t, err)
	require.NoError(t, s.WantProbe(ctx, []Hash{hash(13)}, ProbeWant{T: "movie"}, t0))
	return []Hash{hash(12), hash(11), hash(13), hash(20), hash(19), hash(18), hash(17), hash(16), hash(15), hash(14)}
}

func TestLeaseProbeBatchOrder(t *testing.T) {
	s := open(t)
	order := queueProbeOrder(t, s)
	ls := make([]*probeLease, 6)
	for i := range ls {
		ls[i] = &probeLease{ctx: context.Background(), req: ProbeRequest{Owner: int64(i), Now: t0.Add(time.Minute), Lease: time.Hour, Admit: admitting(i != 1 && i != 4)}}
	}
	require.NoError(t, s.leaseProbes(context.Background(), s.tx, ls))
	var got []Hash
	for _, l := range ls {
		if l.ok {
			got = append(got, l.c.Hash)
		}
	}
	require.Equal(t, order[:5], got,
		"in the batch's order, a call admitting no background probe takes the next request or search and none after")
	require.False(t, ls[4].ok)
}

func TestLeaseProbeConcurrent(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), database.Options{Service: "sniffarr"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	s, err := New(context.Background(), db, Options{ProbeBatch: 4})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })
	order := queueProbeOrder(t, s)

	const callers = 12
	got := make(chan Hash, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			c, ok, err := s.LeaseProbe(context.Background(), ProbeRequest{Owner: int64(i), Now: t0.Add(time.Minute), Lease: time.Hour})
			require.NoError(t, err)
			if ok {
				got <- c.Hash
			}
		})
	}
	wg.Wait()
	close(got)
	seen := map[Hash]bool{}
	for h := range got {
		require.False(t, seen[h], "leased twice")
		seen[h] = true
	}
	want := map[Hash]bool{}
	for _, h := range order {
		want[h] = true
	}
	require.Equal(t, want, seen, "every release once, the rest of the callers none")
}

func TestLeaseProbeBudgetCountsTheBatch(t *testing.T) {
	s := open(t)
	queueProbeOrder(t, s)
	require.NoError(t, s.AddProbeTraffic(context.Background(), "2026-10-01", ProbeTraffic{Rx: 7}))
	var used []int64
	ls := make([]*probeLease, 4)
	for i := range ls {
		ls[i] = &probeLease{ctx: context.Background(), req: ProbeRequest{Owner: int64(i), Now: t0, Lease: time.Hour, Day: "2026-10-01",
			Admit: func(u int64, leased int) (bool, bool) {
				used = append(used, u)
				return leased < 2, true
			}}}
	}
	require.NoError(t, s.leaseProbes(context.Background(), s.tx, ls))
	require.True(t, ls[0].ok)
	require.True(t, ls[1].ok)
	require.False(t, ls[2].ok, "the leases the batch granted count")
	require.False(t, ls[3].ok)
	require.Contains(t, used, int64(7))
}

func TestLeaseProbeCallerGone(t *testing.T) {
	s := open(t)
	order := queueProbeOrder(t, s)
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	ls := []*probeLease{
		{ctx: gone, req: ProbeRequest{Owner: 1, Now: t0, Lease: time.Hour}},
		{ctx: context.Background(), req: ProbeRequest{Owner: 2, Now: t0, Lease: time.Hour}},
	}
	err := s.leaseProbes(context.Background(), s.tx, ls)
	var ie batcher.ItemErrors
	require.ErrorAs(t, err, &ie)
	require.ErrorIs(t, ie[0], context.Canceled)
	require.NoError(t, ie[1])
	require.False(t, ls[0].ok, "a caller gone takes no lease")
	require.True(t, ls[1].ok)
	require.Equal(t, order[0], ls[1].c.Hash)
}

func TestLeaseProbeFailedBatch(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{Service: "sniffarr"})
	require.NoError(t, err)
	var log bytes.Buffer
	s, err := New(ctx, db, Options{Log: slog.New(slog.NewTextHandler(&log, nil))})
	require.NoError(t, err)
	queueProbeOrder(t, s)
	require.NoError(t, db.Close())

	var wg sync.WaitGroup
	for i := range 3 {
		wg.Go(func() {
			_, ok, err := s.LeaseProbe(ctx, ProbeRequest{Owner: int64(i), Now: t0, Lease: time.Hour})
			require.Error(t, err, "each call gets the batch's failure")
			require.False(t, ok)
		})
	}
	wg.Wait()
	require.NoError(t, s.Close(ctx))
	require.Contains(t, log.String(), "probe lease batch failed")
	_, _, err = s.LeaseProbe(ctx, ProbeRequest{Owner: 9, Now: t0, Lease: time.Hour})
	require.ErrorIs(t, err, batcher.ErrClosed)
}

func TestLeaseProbeSearchesOutsideTheWriter(t *testing.T) {
	ctx := context.Background()
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{Service: "sniffarr", Tracer: tp})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	s, err := New(ctx, db, Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close(ctx)) })
	insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")

	held, err := db.W.BeginTx(ctx, nil)
	require.NoError(t, err)
	traced, root := tp.Tracer("test").Start(ctx, "ProbeTask")
	defer root.End()
	type result struct {
		c   ProbeCandidate
		ok  bool
		err error
	}
	done := make(chan result, 1)
	go func() {
		c, ok, err := s.LeaseProbe(traced, ProbeRequest{Owner: 1, Now: t0, Lease: time.Hour})
		done <- result{c, ok, err}
	}()

	searched := func(pool string) bool {
		for _, sp := range spans.Ended() {
			attrs := attribute.NewSet(sp.Attributes()...)
			text, _ := attrs.Value(semconv.DBQueryTextKey)
			p, _ := attrs.Value("db.pool")
			if text.AsString() == backgroundProbeQuery && p.AsString() == pool {
				return true
			}
		}
		return false
	}
	require.Eventually(t, func() bool { return searched("read") }, 5*time.Second, time.Millisecond, "the search runs while another holds the writer")
	select {
	case r := <-done:
		t.Fatalf("leased while the writer is held: %+v", r)
	default:
	}
	require.NoError(t, held.Rollback())
	r := <-done
	require.NoError(t, r.err)
	require.True(t, r.ok)
	require.Equal(t, hash(1), r.c.Hash)
	require.False(t, searched("write"))
	require.False(t, errors.Is(r.err, batcher.ErrAbandoned))
}

// benchReleases is how many releases openProbeBench stores, or
// SNIFFARR_BENCH_RELEASES.
func benchReleases(tb testing.TB) int {
	n := 300_000
	if v := os.Getenv("SNIFFARR_BENCH_RELEASES"); v != "" {
		var err error
		n, err = strconv.Atoi(v)
		require.NoError(tb, err)
	}
	return n
}

// openProbeBench returns a store of n releases shaped like a deployment's:
// three in ten with peers, two in three a movie or a series, three in five
// of those with a Matroska file, every release with more than 100 peers
// probed already, queued probes queued from searches and 100k fetches
// queued.
func openProbeBench(tb testing.TB, n, queued int) (*Store, *database.DB) {
	tb.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(tb.TempDir(), "bench.db"), database.Options{Service: "sniffarr"})
	require.NoError(tb, err)
	tb.Cleanup(func() { db.Close() })
	s, err := New(ctx, db, Options{Languages: []string{"ru"}})
	require.NoError(tb, err)
	for _, q := range []string{
		`WITH RECURSIVE i(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM i WHERE n < ?1)
		INSERT INTO torrent_releases (id, infohash, name, size, file_count, discovery, first_seen, last_seen, kind, peers_week)
		SELECT n, randomblob(20), 'Release.' || n, 1 << 30, 3, 'sample', 0, 0,
			CASE WHEN n % 3 = 0 THEN NULL WHEN n % 6 = 1 THEN 'series' ELSE 'movie' END,
			CASE WHEN n % 10 < 3 THEN (n * 7919) % 1000 ELSE 0 END FROM i`,
		`INSERT INTO torrent_files (torrent_id, idx, path, size)
		SELECT id, 0, 'Release.' || id || CASE WHEN id % 5 < 3 THEN '.mkv' WHEN id % 5 = 3 THEN '.avi' ELSE '.m2ts' END, 1 << 30 FROM torrent_releases
		UNION ALL SELECT id, 1, 'Release.' || id || '.srt', 1 << 10 FROM torrent_releases
		UNION ALL SELECT id, 2, 'Release.' || id || '.nfo', 1 << 10 FROM torrent_releases`,
		`INSERT INTO probe_releases (torrent_id, outcome, attempts, probed_at) SELECT id, 'tracks', 0, 0 FROM torrent_releases WHERE peers_week > 100`,
		`INSERT INTO task_queue (kind, infohash, priority, queued_at) SELECT 'probe', infohash, 1, id FROM torrent_releases
		WHERE peers_week BETWEEN 1 AND 100 AND kind IS NOT NULL AND id % 5 < 3 ORDER BY id LIMIT ?2`,
		`INSERT INTO task_queue (kind, infohash, priority, queued_at) SELECT 'fetch', randomblob(20), 4, n FROM
		(WITH RECURSIVE i(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM i WHERE n < 100000) SELECT n FROM i)`,
	} {
		_, err := db.W.ExecContext(ctx, q, n, queued)
		require.NoError(tb, err)
	}
	return s, db
}

// BenchmarkLeaseProbe reports the time of one batch of background probe
// leases from openProbeBench, of its write transaction, the writer held,
// and that time per lease.
func BenchmarkLeaseProbe(b *testing.B) {
	for _, queued := range []int{0, 1000} {
		for _, batch := range []int{1, 50} {
			b.Run("queued="+strconv.Itoa(queued)+"/batch="+strconv.Itoa(batch), func(b *testing.B) { benchLeaseProbe(b, queued, batch) })
		}
	}
}

func benchLeaseProbe(b *testing.B, queued, batch int) {
	ctx := context.Background()
	s, _ := openProbeBench(b, benchReleases(b), queued)
	var held time.Duration
	leased := 0
	timed := func(ctx context.Context, f func(*sql.Tx) error) error {
		start := time.Now()
		err := s.tx(ctx, f)
		held += time.Since(start)
		return err
	}
	b.ResetTimer()
	for i := range b.N {
		ls := make([]*probeLease, batch)
		for j := range ls {
			ls[j] = &probeLease{ctx: ctx, req: ProbeRequest{Owner: int64(i*batch + j), Now: t0, Lease: time.Hour}}
		}
		require.NoError(b, s.leaseProbes(ctx, timed, ls))
		for _, l := range ls {
			require.True(b, l.ok)
			leased++
		}
	}
	b.ReportMetric(float64(held.Microseconds())/float64(b.N), "writer-µs/batch")
	b.ReportMetric(float64(held.Microseconds())/float64(leased), "writer-µs/lease")
}
