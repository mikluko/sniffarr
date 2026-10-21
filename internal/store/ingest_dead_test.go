package store

import (
	"context"
	"database/sql"
	"io/fs"
	"math"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/mikluko/sniffarr/internal/database"
)

func evictions(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	got := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "sniffarr.queue.evictions" {
				continue
			}
			for _, p := range m.Data.(metricdata.Sum[int64]).DataPoints {
				why, _ := p.Attributes.Value("sniffarr.queue.eviction")
				got[why.AsString()] += p.Value
			}
		}
	}
	return got
}

func TestNoPeers(t *testing.T) {
	ctx := context.Background()
	reader := sdkmetric.NewManualReader()
	q := openQueue(t, QueueOptions{NoPeersFor: time.Hour, Meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))})
	enqueue(t, q, Sample, 1)
	require.Len(t, lease(t, q, 1, 1, t0), 1)

	require.NoError(t, q.NoPeers(ctx, hash(1), 2, t0))
	_, err := q.Discovery(ctx, hash(1))
	require.NoError(t, err, "a report from another owner is ignored")

	require.NoError(t, q.NoPeers(ctx, hash(1), 1, t0))
	_, err = q.Discovery(ctx, hash(1))
	require.ErrorIs(t, err, ErrNotQueued, "dropped at the first report")
	require.EqualValues(t, 1, evictions(t, reader)["no_peers"])

	queued, known := enqueueAt(t, q, t0.Add(59*time.Minute), Sample, 1)
	require.Zero(t, queued, "a sample leaves it dead")
	require.Equal(t, 1, known)
	queued, _ = enqueueAt(t, q, t0.Add(time.Hour), Sample, 1)
	require.Equal(t, 1, queued, "for NoPeersFor, not DeadFor")
}

func TestAnnounceRevives(t *testing.T) {
	ctx := context.Background()
	q := openQueue(t)
	enqueue(t, q, Sample, 1, 2)
	require.Len(t, lease(t, q, 1, 2, t0), 2)
	require.NoError(t, q.NoPeers(ctx, hash(1), 1, t0))
	dropped, err := q.Fail(ctx, hash(2), 1, t0, 1, time.Minute)
	require.NoError(t, err)
	require.True(t, dropped)

	queued, known := enqueueAt(t, q, t0.Add(time.Minute), Announce, 1, 2)
	require.Equal(t, 2, queued, "an announce revives any dead hash")
	require.Zero(t, known)
	d, err := q.Discovery(ctx, hash(1))
	require.NoError(t, err)
	require.Equal(t, Announce, d)
	require.Equal(t, []Hash{hash(1), hash(2)}, lease(t, q, 2, 2, t0.Add(time.Minute)), "revived with no attempts behind it")
}

func TestDeadKey(t *testing.T) {
	ctx := context.Background()
	q := openQueue(t)
	for _, c := range []struct {
		hash []byte
		want int64
	}{
		{hash: []byte{0, 0, 0, 0, 0, 0, 0, 1, 0xff, 0xff}, want: 1},
		{hash: []byte{0x80, 0, 0, 0, 0, 0, 0, 0, 7}, want: math.MinInt64},
		{hash: []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, want: -1},
		{hash: []byte{0x04}, want: 0x04 << 56},
	} {
		require.Equal(t, c.want, deadKey(c.hash), "%x", c.hash)
		var got int64
		require.NoError(t, q.db.R.QueryRowContext(ctx, "SELECT ingest_dead_key(?)", c.hash).Scan(&got))
		require.Equal(t, c.want, got, "%x in SQL", c.hash)
	}
	var null sql.NullInt64
	require.NoError(t, q.db.R.QueryRowContext(ctx, "SELECT ingest_dead_key(NULL)").Scan(&null))
	require.False(t, null.Valid)
	_, err := q.db.R.ExecContext(ctx, "SELECT ingest_dead_key('text')")
	require.Error(t, err)
}

func TestDeadKeyShared(t *testing.T) {
	ctx := context.Background()
	q := openQueue(t)
	a, b := hash(1), hash(1)
	b[19] = 1
	_, _, err := q.Enqueue(ctx, []Sighting{{Hash: a, Discovery: Sample}}, t0)
	require.NoError(t, err)
	_, _, err = q.Lease(ctx, 1, 1, t0, time.Minute)
	require.NoError(t, err)
	require.NoError(t, q.NoPeers(ctx, a, 1, t0))

	queued, known, err := q.Enqueue(ctx, []Sighting{{Hash: b, Discovery: Sample}}, t0.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, [2]int{0, 1}, [2]int{queued, known}, "a hash sharing the first 8 bytes of a dead one is dead too")
}

func TestMigrateDeadKey(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	defer db.Close()
	old := fstest.MapFS{}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	require.NoError(t, err)
	for _, name := range names {
		if name >= "migrations/012" {
			break
		}
		b, err := migrations.ReadFile(name)
		require.NoError(t, err)
		old[strings.TrimPrefix(name, "migrations/")] = &fstest.MapFile{Data: b}
	}
	require.NoError(t, db.Migrate(ctx, "store", old))
	a, b, c := hash(1), hash(1), hash(2)
	b[19] = 1
	for h, until := range map[Hash]int64{a: 200, b: 300, c: 100} {
		_, err := db.W.ExecContext(ctx, "INSERT INTO ingest_dead (infohash, until) VALUES (?, ?)", h[:], until)
		require.NoError(t, err)
	}

	q, err := NewQueue(ctx, db, QueueOptions{})
	require.NoError(t, err)
	got := map[int64]int64{}
	require.NoError(t, func() error {
		rows, err := q.db.R.QueryContext(ctx, "SELECT key, until FROM ingest_dead")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k, u int64
			if err := rows.Scan(&k, &u); err != nil {
				return err
			}
			got[k] = u
		}
		return rows.Err()
	}())
	require.Equal(t, map[int64]int64{deadKey(a[:]): 300, deadKey(c[:]): 100}, got, "one row a key, with the latest until")
	var index string
	require.NoError(t, q.db.R.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE name = 'ingest_dead_until'").Scan(&index))
	require.Contains(t, index, "ingest_dead (until)")
}

// BenchmarkDeadExpiry times, on a dead set of 3M hashes expiring over a day,
// one minute of it: the hashes evicted in a minute inserted, then the
// minute's DELETE of the expired, with and without the until index, and
// reports the bytes the table and the index take.
func BenchmarkDeadExpiry(b *testing.B) {
	const (
		rows   = 3_000_000
		perMin = rows / (24 * 60)
		day    = int64(24 * time.Hour / time.Millisecond)
	)
	for _, indexed := range []bool{true, false} {
		b.Run(map[bool]string{true: "indexed", false: "unindexed"}[indexed], func(b *testing.B) {
			ctx := context.Background()
			db, err := database.Open(ctx, filepath.Join(b.TempDir(), "test.db"))
			require.NoError(b, err)
			defer db.Close()
			_, err = NewQueue(ctx, db, QueueOptions{})
			require.NoError(b, err)
			if !indexed {
				_, err := db.W.ExecContext(ctx, "DROP INDEX ingest_dead_until")
				require.NoError(b, err)
			}
			insert := func(n int, until func() int64) {
				require.NoError(b, db.Tx(ctx, func(tx *sql.Tx) error {
					st, err := tx.PrepareContext(ctx, "INSERT INTO ingest_dead (key, until) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET until = excluded.until")
					if err != nil {
						return err
					}
					defer st.Close()
					for range n {
						if _, err := st.ExecContext(ctx, rand.Int64(), until()); err != nil {
							return err
						}
					}
					return nil
				}))
			}
			var now int64
			insert(rows, func() int64 { return rand.Int64N(day) })
			var table, index int64
			require.NoError(b, db.R.QueryRowContext(ctx, `SELECT coalesce(sum(pgsize) FILTER (WHERE name = 'ingest_dead'), 0),
				coalesce(sum(pgsize) FILTER (WHERE name = 'ingest_dead_until'), 0) FROM dbstat`).Scan(&table, &index))
			var inserting, deleting time.Duration
			for b.Loop() {
				now += int64(time.Minute / time.Millisecond)
				start := time.Now()
				insert(perMin, func() int64 { return now + day })
				mid := time.Now()
				_, err := db.W.ExecContext(ctx, "DELETE FROM ingest_dead WHERE until <= ?", now)
				require.NoError(b, err)
				inserting += mid.Sub(start)
				deleting += time.Since(mid)
			}
			b.ReportMetric(float64(table)/1e6, "table-MB")
			b.ReportMetric(float64(index)/1e6, "index-MB")
			b.ReportMetric(float64(inserting.Milliseconds())/float64(b.N), "insert-ms/op")
			b.ReportMetric(float64(deleting.Milliseconds())/float64(b.N), "delete-ms/op")
		})
	}
}
