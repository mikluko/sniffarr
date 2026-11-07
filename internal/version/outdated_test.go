package version

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOutdated(t *testing.T) {
	tests := []struct {
		running, latest string
		want            bool
	}{
		{"0.44.0-alpha.4", "0.43.0", false},
		{"0.44.0-alpha.4", "0.44.0", true},
		{"0.44.0-rc.1", "0.45.0", true},
		{"0.44.0", "0.45.0", false},
		{"0.44.0", "0.46.0", true},
		{"0.44.0", "1.0.0", true},
		{"1.2.0", "0.46.0", false},
		{"0.44.0", "v0.46.0", true},
		{"0.44.0", "0.46.0-rc.1", false},
		{Dev, "0.46.0", false},
		{"0.44.0", "", false},
		{"0.44", "0.46.0", false},
		{"0.044.0", "0.46.0", false},
	}
	for _, tt := range tests {
		t.Run(tt.running+" vs "+tt.latest, func(t *testing.T) {
			require.Equal(t, tt.want, Outdated(tt.running, tt.latest))
		})
	}
}

func TestPrerelease(t *testing.T) {
	require.True(t, Prerelease("0.44.0-alpha.4"))
	require.False(t, Prerelease("0.44.0"))
	require.False(t, Prerelease(Dev))
}
