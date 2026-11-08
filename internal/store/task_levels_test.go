package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestQueues(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	q, err := NewTasks(ctx, s.db, TaskOptions{})
	require.NoError(t, err)
	a := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	b := insertProbeable(t, s, 2, "Heat.1995.1080p", 5, "Heat.1995.1080p.mkv")
	_, _, err = s.RequestProbe(ctx, []Hash{a}, t0)
	require.NoError(t, err)
	require.NoError(t, s.WantProbe(ctx, []Hash{b}, ProbeWant{T: "movie"}, t0))

	queues, err := q.Queues(ctx, t0)
	require.NoError(t, err)
	require.Len(t, queues, 1)
	tasks := func() []int64 {
		var n []int64
		for _, l := range queues[TaskProbe].Levels {
			n = append(n, l.Tasks)
		}
		return n
	}
	require.Equal(t, []int64{1, 1, 0, 0}, tasks())
	require.Equal(t, 3, queues[TaskProbe].Levels[3].Priority)
	require.False(t, queues[TaskProbe].Levels[0].Known, "no pace yet")

	q.Finished(TaskProbe, t0.Add(30*time.Second))
	q.Finished(TaskProbe, t0.Add(30*time.Second))
	queues, err = q.Queues(ctx, t0.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, queues[TaskProbe].Levels[0].ETA, "one probe at two a minute")
	require.Equal(t, time.Minute, queues[TaskProbe].Levels[3].ETA, "a priority waits for those ahead")

	queues, err = q.Queues(ctx, t0.Add(time.Minute+PaceWindow))
	require.NoError(t, err)
	require.False(t, queues[TaskProbe].Levels[1].Known, "the pace forgets what is older than the window")
}

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
