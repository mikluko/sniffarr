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
	_, err = s.WantPeers(ctx, []Hash{a}, t0)
	require.NoError(t, err)
	_, _, err = s.RequestProbe(ctx, []Hash{a}, t0)
	require.NoError(t, err)
	enqueue(t, q, Arr, 11, 12)
	enqueue(t, q, Sample, 21, 22, 23, 24, 25, 26)

	queues, err := q.Queues(ctx, t0)
	require.NoError(t, err)
	require.Len(t, queues, 3)
	tasks := func(kind string) []int64 {
		var n []int64
		for _, l := range queues[kind].Levels {
			n = append(n, l.Tasks)
		}
		return n
	}
	require.Equal(t, []int64{0, 2, 0, 0, 6}, tasks(TaskFetch))
	require.Equal(t, []int64{1, 0, 0, 0, 0}, tasks(TaskPeers))
	require.Equal(t, []int64{1, 0, 0, 0, 0}, tasks(TaskProbe))
	require.Equal(t, 4, queues[TaskFetch].Levels[4].Priority)
	require.True(t, queues[TaskFetch].Levels[0].Known, "nothing queued at P0 or ahead")
	require.False(t, queues[TaskFetch].Levels[1].Known, "no pace yet")
	require.Zero(t, queues[TaskFetch].PerSecond)

	for i := range 30 {
		q.Finished(TaskFetch, t0.Add(time.Duration(i)*2*time.Second))
	}
	queues, err = q.Queues(ctx, t0.Add(time.Minute))
	require.NoError(t, err)
	require.InDelta(t, 0.5, queues[TaskFetch].PerSecond, 1e-9)
	require.Equal(t, 4*time.Second, queues[TaskFetch].Levels[1].ETA, "two fetches at 30 a minute")
	require.Equal(t, 16*time.Second, queues[TaskFetch].Levels[4].ETA, "eight at P4 or ahead")
	require.False(t, queues[TaskPeers].Levels[0].Known, "a lookup goes at its own pace, not the fetches'")
	require.False(t, queues[TaskProbe].Levels[4].Known, "a priority waits for those ahead")

	q.Finished(TaskPeers, t0)
	q.Finished(TaskProbe, t0.Add(30*time.Second))
	q.Finished(TaskProbe, t0.Add(30*time.Second))
	queues, err = q.Queues(ctx, t0.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, time.Minute, queues[TaskPeers].Levels[4].ETA, "one lookup at one a minute")
	require.Equal(t, 30*time.Second, queues[TaskProbe].Levels[0].ETA, "one probe at two a minute")
	require.Equal(t, 16*time.Second, queues[TaskFetch].Levels[4].ETA, "the fetches' pace is theirs alone")

	queues, err = q.Queues(ctx, t0.Add(time.Minute+PaceWindow))
	require.NoError(t, err)
	require.False(t, queues[TaskFetch].Levels[1].Known, "the pace forgets what is older than the window")
}
