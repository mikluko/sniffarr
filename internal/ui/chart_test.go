package ui

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A bar's height is in percent of the chart's; the right half's tooltips
// hang to the left.
func TestBarChartPercent(t *testing.T) {
	c := NewBarChart([]int64{50, 100}, []string{"a", "b"}, 1, true, 80)
	require.Len(t, c.Bars, 2)
	first, last := c.Bars[0], c.Bars[1]
	require.Equal(t, 50.0, first.Height())
	require.False(t, first.RightHalf())
	require.Equal(t, 100.0, last.Height())
	require.True(t, last.RightHalf())
	require.Zero(t, first.ProjHeight())
	require.Equal(t, 80.0, last.ProjHeight())
}
