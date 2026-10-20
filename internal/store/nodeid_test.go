package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNodeIDOutlivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	s, db := openAt(t, path)
	id1, err := s.NodeID(ctx)
	require.NoError(t, err)
	id2, err := s.NodeID(ctx)
	require.NoError(t, err)
	require.Equal(t, id1, id2)
	require.NotEqual(t, [20]byte{}, id1)
	k, err := s.HashKey(ctx)
	require.NoError(t, err)
	require.NotEqual(t, k[:], id1[:16], "the ID and the key are separate settings")
	require.NoError(t, db.Close())

	s, db = openAt(t, path)
	defer db.Close()
	id3, err := s.NodeID(ctx)
	require.NoError(t, err)
	require.Equal(t, id1, id3, "the ID is kept in the store")
}
