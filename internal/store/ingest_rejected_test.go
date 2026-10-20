package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/rules"
)

func TestInsertReject(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	ok, _, err := s.Insert(ctx, torrent(1, "Alien.1979.1080p.BluRay", "Alien.1979.1080p.BluRay.mkv"))
	require.NoError(t, err)
	require.True(t, ok)
	ok, _, err = s.Insert(ctx, torrent(1, "Alien.1979.1080p.BluRay"))
	require.NoError(t, err)
	require.False(t, ok, "second insert")

	ok, err = s.Reject(ctx, hash(2), rules.Adult, t0)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = s.Reject(ctx, hash(2), rules.Adult, t0)
	require.NoError(t, err)
	require.False(t, ok, "second reject")
	ok, err = s.Reject(ctx, hash(1), rules.Small, t0)
	require.NoError(t, err)
	require.False(t, ok, "reject of a stored hash")
	ok, _, err = s.Insert(ctx, torrent(2, "x"))
	require.NoError(t, err)
	require.False(t, ok, "insert of a rejected hash")

	_, err = s.Get(ctx, hash(2))
	require.ErrorIs(t, err, ErrNotFound)
}
