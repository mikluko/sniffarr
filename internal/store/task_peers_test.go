package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"sarak.sh/mikluko/libarr/database"
)

func TestMigrateTaskQueue(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{Service: "sniffarr"})
	require.NoError(t, err)
	defer db.Close()
	migrateBefore(t, db, "migrations/022")
	for _, stmt := range []string{
		"INSERT INTO torrent_releases (id, infohash, name, size, file_count, discovery, first_seen, last_seen) VALUES (1, x'01', 'a', 1, 1, 'sample', 0, 0), (2, x'02', 'b', 1, 1, 'sample', 0, 0), (3, x'03', 'c', 1, 1, 'sample', 0, 0)",
		"INSERT INTO probe_releases (torrent_id, wanted_at) VALUES (1, 100)",
		"INSERT INTO probe_releases (torrent_id, wanted_at, outcome, attempts, retry_at) VALUES (2, 200, 'failed', 1, 300)",
		"INSERT INTO probe_releases (torrent_id, wanted_at, outcome) VALUES (3, 300, 'tracks')",
		"INSERT INTO ingest_queue (infohash, priority, queued_at, attempts) VALUES (x'09', 0, 1000, 2)",
	} {
		_, err := db.W.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	migrateBefore(t, db, "migrations/031")
	type row struct {
		Kind                          string
		Hash                          byte
		Priority, QueuedAt, NotBefore int64
		Attempts                      int
	}
	var got []row
	rows, err := db.R.QueryContext(ctx, "SELECT kind, infohash, priority, queued_at, not_before, attempts FROM task_queue ORDER BY kind, infohash")
	require.NoError(t, err)
	for rows.Next() {
		var r row
		var h []byte
		require.NoError(t, rows.Scan(&r.Kind, &h, &r.Priority, &r.QueuedAt, &r.NotBefore, &r.Attempts))
		r.Hash = h[0]
		got = append(got, r)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Equal(t, []row{
		{Kind: TaskFetch, Hash: 9, Priority: PriorityAnnounce, QueuedAt: 1000, Attempts: 2},
		{Kind: TaskProbe, Hash: 1, Priority: PriorityArr, QueuedAt: 100000},
		{Kind: TaskProbe, Hash: 2, Priority: PriorityArr, QueuedAt: 200000, NotBefore: 300000},
	}, got, "fetches keep their state, wanted probes not done are queued")
}

func TestWantPeers(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	a := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	b := insertProbeable(t, s, 2, "Heat.1995.1080p", 5, "Heat.1995.1080p.mkv")
	wanted := func(h Hash) []time.Time {
		t.Helper()
		got, err := s.Journal(ctx, h, 1000)
		require.NoError(t, err)
		var out []time.Time
		for _, e := range got {
			if e.Kind == JournalPeersWanted {
				require.Equal(t, "request", e.Outcome)
				require.Empty(t, e.Detail)
				out = append(out, e.At)
			}
		}
		return out
	}

	stored, err := s.WantPeers(ctx, []Hash{a, hash(99)}, t0)
	require.NoError(t, err)
	require.Equal(t, []Hash{a}, stored, "only stored releases")
	require.Equal(t, []time.Time{t0}, wanted(a))
	require.Empty(t, entries(t, s, hash(99)), "nor one of a release not stored")
	stored, err = s.WantPeers(ctx, []Hash{a, b}, t0.Add(time.Minute))
	require.NoError(t, err)
	require.ElementsMatch(t, []Hash{a, b}, stored)
	require.Equal(t, []time.Time{t0, t0.Add(time.Minute)}, wanted(a), "each request")
	require.Equal(t, []time.Time{t0.Add(time.Minute)}, wanted(b))
	stored, err = s.WantPeers(ctx, nil, t0)
	require.NoError(t, err)
	require.Empty(t, stored)
}
