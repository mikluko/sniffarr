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

	"sarak.sh/mikluko/libarr/database"
)

func TestDeadKey(t *testing.T) {
	ctx := context.Background()
	q := open(t)
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
		require.NoError(t, q.db.R.QueryRowContext(ctx, "SELECT task_dead_key(?)", c.hash).Scan(&got))
		require.Equal(t, c.want, got, "%x in SQL", c.hash)
	}
	var null sql.NullInt64
	require.NoError(t, q.db.R.QueryRowContext(ctx, "SELECT task_dead_key(NULL)").Scan(&null))
	require.False(t, null.Valid)
	_, err := q.db.R.ExecContext(ctx, "SELECT task_dead_key('text')")
	require.Error(t, err)
}

func TestMigrateDeadKey(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{Service: "sniffarr"})
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

	q, err := New(ctx, db, Options{})
	require.NoError(t, err)
	got := map[int64]int64{}
	require.NoError(t, func() error {
		rows, err := q.db.R.QueryContext(ctx, "SELECT key, until FROM task_dead")
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
	require.NoError(t, q.db.R.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE name = 'task_dead_until'").Scan(&index))
	require.Contains(t, index, "task_dead (until)")
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
			db, err := database.Open(ctx, filepath.Join(b.TempDir(), "test.db"), database.Options{Service: "sniffarr"})
			require.NoError(b, err)
			defer db.Close()
			_, err = New(ctx, db, Options{})
			require.NoError(b, err)
			if !indexed {
				_, err := db.W.ExecContext(ctx, "DROP INDEX task_dead_until")
				require.NoError(b, err)
			}
			insert := func(n int, until func() int64) {
				require.NoError(b, db.Tx(ctx, func(tx *sql.Tx) error {
					st, err := tx.PrepareContext(ctx, "INSERT INTO task_dead (key, until) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET until = excluded.until")
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
			require.NoError(b, db.R.QueryRowContext(ctx, `SELECT coalesce(sum(pgsize) FILTER (WHERE name = 'task_dead'), 0),
				coalesce(sum(pgsize) FILTER (WHERE name = 'task_dead_until'), 0) FROM dbstat`).Scan(&table, &index))
			var inserting, deleting time.Duration
			for b.Loop() {
				now += int64(time.Minute / time.Millisecond)
				start := time.Now()
				insert(perMin, func() int64 { return now + day })
				mid := time.Now()
				_, err := db.W.ExecContext(ctx, "DELETE FROM task_dead WHERE until <= ?", now)
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

func TestRecordQueued(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for _, b := range []byte{3, 4} {
		_, err := s.w.ExecContext(ctx, "INSERT INTO task_dead (key, until) VALUES (task_dead_key(?), ?)", []byte{b}, t0.Add(time.Hour).UnixMilli())
		require.NoError(t, err)
	}
	require.NoError(t, s.RecordQueued(ctx, []Queued{{Hash: hash(1), Discovery: Announce}, {Hash: hash(2), Discovery: Sample},
		{Hash: hash(3), Discovery: Announce, Revived: true}, {Hash: hash(4), Discovery: Sample, Revived: true}, {Hash: hash(5), Discovery: Arr}}, t0))
	require.JSONEq(t, `{"discovery":"announce"}`, entry(t, s, hash(1), JournalSighted, "queued").Detail)
	require.Empty(t, entries(t, s, hash(2)), "a sample's fetch is not journaled")
	require.JSONEq(t, `{"discovery":"announce"}`, entry(t, s, hash(3), JournalSighted, "revived").Detail)
	require.Empty(t, entries(t, s, hash(4)))
	require.Equal(t, []string{"sighted/queued"}, entries(t, s, hash(5)))
	held, err := s.Screen(ctx, []Hash{hash(3), hash(4)}, t0)
	require.NoError(t, err)
	require.Empty(t, held, "a revived hash leaves the dead set, journaled or not")
	require.NoError(t, s.RecordQueued(ctx, nil, t0))
}

func TestRecordLeases(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	until := t0.Add(time.Minute)
	require.NoError(t, s.RecordLeases(ctx, []Lease{{Hash: hash(1), Priority: PriorityAnnounce, Attempt: 2, Until: until},
		{Hash: hash(2), Priority: PrioritySample, Attempt: 1, Until: until}}, t0))
	require.JSONEq(t, `{"attempt":2,"lease_until":`+unix(until)+`}`, entry(t, s, hash(1), JournalLeased, "fetch").Detail)
	require.Empty(t, entries(t, s, hash(2)), "nor a sample's lease")
}

func TestRecordFetchFailure(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	retry, dead := t0.Add(10*time.Minute), t0.Add(24*time.Hour)
	require.NoError(t, s.RecordFetchFailure(ctx, FetchFailure{Hash: hash(1), Priority: PriorityAnnounce, Outcome: "timeout", Attempt: 1, RetryAt: retry, Leases: 2}, t0))
	require.NoError(t, s.RecordFetchFailure(ctx, FetchFailure{Hash: hash(2), Priority: PriorityArr, Outcome: "dropped", Attempt: 3, DeadUntil: dead, Leases: 1}, t0))
	require.NoError(t, s.RecordFetchFailure(ctx, FetchFailure{Hash: hash(3), Priority: PriorityAnnounce, Outcome: "no_peers", DeadUntil: t0.Add(time.Hour), Leases: 3}, t0))
	require.NoError(t, s.RecordFetchFailure(ctx, FetchFailure{Hash: hash(4), Priority: PrioritySample, Outcome: "no_peers", DeadUntil: t0.Add(time.Hour)}, t0))
	require.JSONEq(t, `{"attempt":1,"retry_at":`+unix(retry)+`,"leases":2}`, entry(t, s, hash(1), JournalFetchFailed, "timeout").Detail)
	require.JSONEq(t, `{"attempt":3,"dead_until":`+unix(dead)+`}`, entry(t, s, hash(2), JournalFetchFailed, "dropped").Detail)
	require.JSONEq(t, `{"dead_until":`+unix(t0.Add(time.Hour))+`,"leases":3}`, entry(t, s, hash(3), JournalFetchFailed, "no_peers").Detail)
	require.Empty(t, entries(t, s, hash(4)), "a sample's failure is not journaled")

	shared := hash(4)
	shared[19] = 1
	held, err := s.Screen(ctx, []Hash{hash(1), hash(2), hash(3), hash(4), shared}, t0.Add(time.Hour-time.Millisecond))
	require.NoError(t, err)
	require.Equal(t, map[Hash]Screened{hash(2): {Dead: true}, hash(3): {Dead: true}, hash(4): {Dead: true}, shared: {Dead: true}}, held,
		"a timeout to retry is not dead; a hash sharing the first 8 bytes of a dead one is dead too")
	n, err := s.Dead(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 3, n)

	expired, err := s.ExpireDead(ctx, t0.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, 2, expired, "the no-peers ones end first")
	held, err = s.Screen(ctx, []Hash{hash(2), hash(3)}, t0.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, map[Hash]Screened{hash(2): {Dead: true}}, held)
}
