package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSaveLoadCounts(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	_, _, ok, err := s.LoadCounts(ctx)
	require.NoError(t, err)
	require.False(t, ok, "none before the first save")

	st := Stats{Total: 3, Kinds: map[string]int64{"movie": 2, "": 1}, Audio: map[string]AudioCounts{"ru": {Marker: 1, Likely: 1}},
		Rejected: map[string]int64{"adult": 4}, Hours: []int64{1, 0, 2}, NewDay: 3}
	require.NoError(t, s.SaveCounts(ctx, t0, st))
	require.NoError(t, s.SaveCounts(ctx, t0.Add(-1), Stats{Total: 1}))
	require.NoError(t, s.SaveCounts(ctx, t0, st))

	got, at, ok, err := s.LoadCounts(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, st, got)
	require.True(t, at.Equal(t0))
}
