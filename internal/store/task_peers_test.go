package store

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/database"
)

func TestWantPeers(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	q, err := NewTasks(ctx, s.db, TaskOptions{})
	require.NoError(t, err)
	a := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	b := insertProbeable(t, s, 2, "Heat.1995.1080p", 5, "Heat.1995.1080p.mkv")
	enqueue(t, q, Arr, 9)

	n, err := s.WantPeers(ctx, []Hash{b, hash(99)}, t0)
	require.NoError(t, err)
	require.Equal(t, 1, n, "only stored releases")
	n, err = s.WantPeers(ctx, []Hash{a, b}, t0.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 2, n)

	tasks, _, err := q.Lease(ctx, 1, 2, t0.Add(time.Minute), time.Minute)
	require.NoError(t, err)
	require.Equal(t, []Task{{Kind: TaskPeers, Hash: b}, {Kind: TaskPeers, Hash: a}}, tasks, "lookups before arr fetches, a release asked for again keeps its place")
	tasks, _, err = q.Lease(ctx, 1, 2, t0.Add(time.Minute), time.Minute)
	require.NoError(t, err)
	require.Equal(t, []Task{{Kind: TaskFetch, Hash: hash(9)}}, tasks)

	require.NoError(t, q.Complete(ctx, TaskPeers, b))
	require.NoError(t, q.Release(ctx, 1))
	tasks, _, err = q.Lease(ctx, 2, 10, t0.Add(time.Minute), time.Minute)
	require.NoError(t, err)
	require.Equal(t, []Task{{Kind: TaskPeers, Hash: a}, {Kind: TaskFetch, Hash: hash(9)}}, tasks, "a released lookup is leased again")
	n, err = q.Len(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n, "Len counts fetches")
}

func TestTrimDropsTasksOfGoneReleases(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	q, err := NewTasks(ctx, s.db, TaskOptions{})
	require.NoError(t, err)
	a := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	b := insertProbeable(t, s, 2, "Heat.1995.1080p", 5, "Heat.1995.1080p.mkv")
	_, err = s.WantPeers(ctx, []Hash{a, b}, t0)
	require.NoError(t, err)
	_, _, err = s.RequestProbe(ctx, []Hash{a, b}, t0)
	require.NoError(t, err)
	enqueue(t, q, Sample, 9)
	_, err = s.w.ExecContext(ctx, "DELETE FROM torrent_releases WHERE infohash = ?", a[:])
	require.NoError(t, err)

	_, err = q.Trim(ctx, t0)
	require.NoError(t, err)
	var kinds []string
	require.NoError(t, s.each(ctx, func(rows *sql.Rows) error {
		var k string
		var h []byte
		if err := rows.Scan(&k, &h); err != nil {
			return err
		}
		kinds = append(kinds, k+" "+string(rune('0'+h[0])))
		return nil
	}, "SELECT kind, infohash FROM task_queue ORDER BY kind, infohash"))
	require.Equal(t, []string{"fetch 9", "peers 2", "probe 2"}, kinds)
}

func TestTrimDropsProbesNeverLeased(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	q, err := NewTasks(ctx, s.db, TaskOptions{})
	require.NoError(t, err)
	readable := insertProbeable(t, s, 1, "Alien.1979.1080p", 0, "Alien.1979.1080p.mkv")
	avi := insertProbeable(t, s, 2, "Heat.1995.DVDRip", 5, "Heat.1995.DVDRip.avi")
	_, _, err = s.RequestProbe(ctx, []Hash{readable, avi}, t0)
	require.NoError(t, err)

	_, err = q.Trim(ctx, t0)
	require.NoError(t, err)
	require.Equal(t, []Hash{readable}, queuedProbes(t, s), "a release without peers waits; one a probe cannot read goes")
}

func TestMigrateTaskQueue(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{})
	require.NoError(t, err)
	defer db.Close()
	old := fstest.MapFS{}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	require.NoError(t, err)
	for _, name := range names {
		if name >= "migrations/022" {
			break
		}
		b, err := migrations.ReadFile(name)
		require.NoError(t, err)
		old[name[len("migrations/"):]] = &fstest.MapFile{Data: b}
	}
	require.NoError(t, db.Migrate(ctx, "store", old))
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

	_, err = New(ctx, db, Options{})
	require.NoError(t, err)
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
