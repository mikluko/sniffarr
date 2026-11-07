package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/database"
)

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
	db, err := database.Open(ctx, filepath.Join(tb.TempDir(), "bench.db"), database.Options{})
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
