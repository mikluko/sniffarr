package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProbers(t *testing.T) {
	ctx := context.Background()
	q, err := NewTasks(ctx, open(t).db, TaskOptions{})
	require.NoError(t, err)
	require.Zero(t, q.Probers(t0), "none seen since the start")

	q.ProberSeen("a", t0)
	q.ProberSeen("a", t0.Add(time.Minute))
	q.ProberSeen("b", t0.Add(time.Minute))
	q.ProberSeen("", t0.Add(2*time.Minute))
	require.Equal(t, 3, q.Probers(t0.Add(2*time.Minute)), "a prober counts once, and one without an ID as one more")

	require.Equal(t, 3, q.Probers(t0.Add(time.Minute+ProberWindow)), "a prober seen exactly a window ago still counts")
	require.Equal(t, 1, q.Probers(t0.Add(time.Minute+ProberWindow+time.Second)), "those seen last before the window are gone")
	require.Zero(t, q.Probers(t0.Add(2*time.Minute+ProberWindow+time.Second)))
}
