package api

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/database"
)

func TestTruncateWAL(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{})
	require.NoError(t, err)
	defer db.Close()
	_, err = db.W.ExecContext(ctx, "CREATE TABLE t (x BLOB)")
	require.NoError(t, err)
	for range 50 {
		_, err := db.W.ExecContext(ctx, "INSERT INTO t VALUES (zeroblob(65536))")
		require.NoError(t, err)
	}
	size, err := db.WALSize()
	require.NoError(t, err)
	require.Positive(t, size)
	log := slog.New(slog.DiscardHandler)

	truncateWAL(ctx, db, size+1, log)
	after, err := db.WALSize()
	require.NoError(t, err)
	require.Equal(t, size, after, "under the limit the WAL is left alone")

	truncateWAL(ctx, db, size, log)
	after, err = db.WALSize()
	require.NoError(t, err)
	require.Zero(t, after)
}
