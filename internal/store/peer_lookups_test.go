package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPeerLookups(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	a := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	b := insertProbeable(t, s, 2, "Heat.1995.1080p", 5, "Heat.1995.1080p.mkv")

	n, err := s.WantPeers(ctx, []Hash{b, hash(99)}, t0)
	require.NoError(t, err)
	require.Equal(t, 1, n, "only stored releases")
	n, err = s.WantPeers(ctx, []Hash{a, b}, t0.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 2, n)

	got, err := s.TakePeerLookups(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, []Hash{b}, got, "a release asked for again keeps its place")
	got, err = s.TakePeerLookups(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, []Hash{a}, got)
	got, err = s.TakePeerLookups(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, got, "a lookup is taken once")
}
