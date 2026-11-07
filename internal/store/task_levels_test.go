package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLevels(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	q, err := NewTasks(ctx, s.db, TaskOptions{})
	require.NoError(t, err)
	a := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	_, err = s.WantPeers(ctx, []Hash{a}, t0)
	require.NoError(t, err)
	_, err = s.RequestProbe(ctx, []Hash{a}, t0)
	require.NoError(t, err)
	enqueue(t, q, Arr, 11, 12)
	enqueue(t, q, Sample, 21, 22, 23, 24, 25, 26)

	levels, err := q.Levels(ctx, t0)
	require.NoError(t, err)
	require.Len(t, levels, 5)
	require.Equal(t, map[string]int64{TaskFetch: 0, TaskPeers: 1, TaskProbe: 1}, levels[0].Tasks)
	require.Equal(t, int64(2), levels[1].Tasks[TaskFetch])
	require.Equal(t, int64(6), levels[4].Tasks[TaskFetch])
	require.False(t, levels[0].Known, "no pace yet")

	for i := range 30 {
		q.Finished(TaskFetch, t0.Add(time.Duration(i)*2*time.Second))
	}
	levels, err = q.Levels(ctx, t0.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, levels[0].Known, "the probe's pace is unknown")
	require.False(t, levels[1].Known, "a priority waits for those ahead")

	q.Finished(TaskProbe, t0)
	levels, err = q.Levels(ctx, t0.Add(time.Minute))
	require.NoError(t, err)
	for _, l := range levels {
		require.True(t, l.Known, l.Priority)
	}
	require.Equal(t, 60*time.Second, levels[0].ETA, "one probe at one a minute outlasts one lookup at 30 a minute")
	for range 29 {
		q.Finished(TaskProbe, t0.Add(30*time.Second))
	}
	levels, err = q.Levels(ctx, t0.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 2*time.Second, levels[0].ETA)
	require.Equal(t, 6*time.Second, levels[1].ETA, "three fetches and lookups at P1 or ahead at 30 a minute")
	require.Equal(t, 18*time.Second, levels[4].ETA, "nine at P4 or ahead")
	levels, err = q.Levels(ctx, t0.Add(time.Minute+PaceWindow))
	require.NoError(t, err)
	require.False(t, levels[0].Known, "the pace forgets what is older than the window")
}
