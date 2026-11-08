package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"sarak.sh/mikluko/libarr/database"
)

func TestBacklog(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{Service: "sniffarr"})
	require.NoError(t, err)
	defer db.Close()
	migrateBefore(t, db, "migrations/031")
	for _, r := range []struct {
		kind                          string
		hash                          byte
		priority, attempts, notBefore int64
	}{{"fetch", 3, 3, 0, 0}, {"fetch", 1, 1, 2, 5000}, {"peers", 2, 0, 0, 0}, {"probe", 4, 1, 0, 0}} {
		h := hash(r.hash)
		_, err := db.W.ExecContext(ctx, "INSERT INTO task_queue (kind, infohash, priority, queued_at, attempts, not_before) VALUES (?, ?, ?, 0, ?, ?)",
			r.kind, h[:], r.priority, r.attempts, r.notBefore)
		require.NoError(t, err)
	}
	s, err := New(ctx, db, Options{})
	require.NoError(t, err)

	first, err := s.Backlog(ctx, 2)
	require.NoError(t, err)
	require.Len(t, first, 2)
	require.Equal(t, BacklogTask{ID: first[0].ID, Kind: TaskPeers, Hash: Hash{2}, Priority: PriorityRequest}, first[0], "by priority")
	require.Equal(t, BacklogTask{ID: first[1].ID, Kind: TaskFetch, Hash: Hash{1}, Priority: PriorityArr, Attempts: 2, NotBefore: time.UnixMilli(5000)}, first[1])
	var probes int
	require.NoError(t, db.R.QueryRowContext(ctx, "SELECT count(*) FROM task_queue WHERE kind = 'probe'").Scan(&probes))
	require.Equal(t, 1, probes, "the probes stay queued")

	require.NoError(t, s.DropBacklog(ctx, []int64{first[0].ID, first[1].ID}))
	rest, err := s.Backlog(ctx, 10)
	require.NoError(t, err)
	require.Len(t, rest, 1)
	require.Equal(t, Hash{3}, rest[0].Hash)
	require.Contains(t, schema(t, db), "table task_backlog", "kept while it holds a task")
	require.NoError(t, s.DropBacklog(ctx, []int64{rest[0].ID}))
	require.NotContains(t, schema(t, db), "table task_backlog", "dropped once empty")
	gone, err := s.Backlog(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, gone)
	require.NoError(t, s.DropBacklog(ctx, nil), "a backlog gone is no error")
}
