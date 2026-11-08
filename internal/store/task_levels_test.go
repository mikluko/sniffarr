package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLevels(t *testing.T) {
	q := Levels([]int64{0, 2, 0, 6}, 0.5)
	require.InDelta(t, 0.5, q.PerSecond, 1e-9)
	require.Len(t, q.Levels, 4)
	require.True(t, q.Levels[0].Known, "nothing at P0 or ahead")
	require.Equal(t, 4*time.Second, q.Levels[1].ETA)
	require.Equal(t, 16*time.Second, q.Levels[3].ETA)
	none := Levels(nil, 0)
	require.Zero(t, none.Levels[3].Tasks)
	require.True(t, none.Levels[3].Known, "an empty queue is done")
	unknown := Levels([]int64{1, 0, 0, 0}, 0)
	require.False(t, unknown.Levels[0].Known, "no pace")
}
