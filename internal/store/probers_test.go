package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProbers(t *testing.T) {
	var q Probers
	require.Zero(t, q.Count(t0), "none seen since the start")

	q.Seen("a", t0)
	q.Seen("a", t0.Add(time.Minute))
	q.Seen("b", t0.Add(time.Minute))
	q.Seen("", t0.Add(2*time.Minute))
	require.Equal(t, 3, q.Count(t0.Add(2*time.Minute)), "a prober counts once, and one without an ID as one more")

	require.Equal(t, 3, q.Count(t0.Add(time.Minute+ProberWindow)), "a prober seen exactly a window ago still counts")
	require.Equal(t, 1, q.Count(t0.Add(time.Minute+ProberWindow+time.Second)), "those seen last before the window are gone")
	require.Zero(t, q.Count(t0.Add(2*time.Minute+ProberWindow+time.Second)))
}
