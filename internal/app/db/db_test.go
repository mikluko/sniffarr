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

// leased leases a probe to a previous run's prober in the database at path
// and returns its hash.
func leased(t *testing.T, path string) store.Hash {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, path, database.Options{Service: "sniffarr"})
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	s, err := store.New(ctx, db, store.Options{})
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close(ctx)) }()
	h := store.Hash{1}
	_, _, err = s.Insert(ctx, store.Torrent{Hash: h, Name: "Heat.1995.1080p", Size: 1 << 30, Files: []rules.File{{Path: "Heat.1995.1080p.mkv", Size: 1 << 30}},
		Discovery: store.Sample, SeenAt: time.Now(), Kind: "movie", KindLayer: store.LayerRules})
	require.NoError(t, err)
	_, err = db.W.ExecContext(ctx, "UPDATE torrent_releases SET peers_week = 5")
	require.NoError(t, err)
	_, _, err = s.RequestProbe(ctx, []store.Hash{h}, time.Now())
	require.NoError(t, err)
	_, ok, err := s.LeaseProbe(ctx, store.ProbeRequest{Owner: 7, Now: time.Now(), Lease: time.Hour, MaxBytes: 1})
	require.NoError(t, err)
	require.True(t, ok)
	return h
}

func TestModuleStartsUnderWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	h := leased(t, path)

	var s *store.Store
	var q *store.Tasks
	a := fx.New(fx.NopLogger, fx.Supply(Config{Path: path}, slog.New(slog.DiscardHandler)), Module, fx.Populate(&s, &q))
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

	c, ok, err := s.LeaseProbe(context.Background(), store.ProbeRequest{Owner: 8, Now: time.Now(), Lease: time.Hour, MaxBytes: 1})
	require.NoError(t, err)
	require.True(t, ok, "the previous run's lease is freed before the start")
	require.Equal(t, h, c.Hash)
	require.NoError(t, a.Stop(context.Background()))
}
