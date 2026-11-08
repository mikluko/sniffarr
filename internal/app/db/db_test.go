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
	"sarak.sh/mikluko/sniffarr/internal/rules"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

// stored stores a torrent in the database at path and returns its hash.
func stored(t *testing.T, path string) store.Hash {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, path, database.Options{Service: "sniffarr"})
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	s, err := store.New(ctx, db, store.Options{})
	require.NoError(t, err)
	h := store.Hash{1}
	_, _, err = s.Insert(ctx, store.Torrent{Hash: h, Name: "Heat.1995.1080p", Size: 1 << 30, Files: []rules.File{{Path: "Heat.1995.1080p.mkv", Size: 1 << 30}},
		Discovery: store.Sample, SeenAt: time.Now(), Kind: "movie", KindLayer: store.LayerRules})
	require.NoError(t, err)
	return h
}

func TestModuleStartsUnderWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	h := stored(t, path)

	var s *store.Store
	a := fx.New(fx.NopLogger, fx.Supply(Config{Path: path}, slog.New(slog.DiscardHandler)), Module, fx.Populate(&s))
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

	r, err := s.Get(context.Background(), h)
	require.NoError(t, err)
	require.Equal(t, "Heat.1995.1080p", r.Name)
	require.NoError(t, a.Stop(context.Background()))
}
