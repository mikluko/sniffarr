package db

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	"sarak.sh/mikluko/libarr/database"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

// leased leases a fetch to a previous run's fetcher in the database at path
// and returns its hash.
func leased(t *testing.T, path string) store.Hash {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, path, database.Options{Service: "sniffarr"})
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	q, err := store.NewTasks(ctx, db, store.TaskOptions{})
	require.NoError(t, err)
	h := store.Hash{1}
	_, _, err = q.Enqueue(ctx, []store.Sighting{{Hash: h, Discovery: store.Announce}}, time.Now())
	require.NoError(t, err)
	tasks, _, err := q.Lease(ctx, 7, 1, time.Now(), time.Hour)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	return h
}

func TestModuleStartsUnderWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	h := leased(t, path)

	var q *store.Tasks
	a := fx.New(fx.NopLogger, fx.Supply(Config{Path: path}, slog.New(slog.DiscardHandler)), Module, fx.Populate(&q))
	require.NoError(t, a.Err())

	other, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, other.Close()) })
	conn, err := other.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	_, err = conn.ExecContext(context.Background(), "BEGIN IMMEDIATE")
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, a.Start(ctx), "the start writes nothing, so another process's write lock cannot hold it up")
	_, err = conn.ExecContext(context.Background(), "ROLLBACK")
	require.NoError(t, err)

	tasks, _, err := q.Lease(context.Background(), 8, 1, time.Now(), time.Hour)
	require.NoError(t, err)
	require.Len(t, tasks, 1, "the previous run's lease is freed before the start")
	require.Equal(t, h, tasks[0].Hash)
	require.NoError(t, a.Stop(context.Background()))
}
