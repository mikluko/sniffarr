package version

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOutdated(t *testing.T) {
	tests := []struct {
		running, latest string
		policy          Policy
		want            bool
	}{
		{"0.44.0-alpha.4", "0.43.0", Lazy, false},
		{"0.44.0-alpha.4", "0.44.0", Lazy, true},
		{"0.44.0-rc.1", "0.45.0", Lazy, true},
		{"0.44.0", "0.45.0", Lazy, false},
		{"0.44.0", "0.46.0", Lazy, true},
		{"0.44.0", "1.0.0", Lazy, true},
		{"1.2.0", "0.46.0", Lazy, false},
		{"0.44.0", "v0.46.0", Lazy, true},
		{"0.44.0", "0.46.0-rc.1", Lazy, false},
		{Dev, "0.46.0", Lazy, false},
		{"0.44.0", "", Lazy, false},
		{"0.44", "0.46.0", Lazy, false},
		{"0.044.0", "0.46.0", Lazy, false},
		{"0.44.0", "0.44.1", Eager, true},
		{"0.44.0", "0.45.0", Eager, true},
		{"0.44.1", "0.44.1", Eager, false},
		{"0.44.1", "0.44.0", Eager, false},
		{"0.44.0-alpha.4", "0.43.0", Eager, false},
		{"0.44.0-alpha.4", "0.44.0", Eager, true},
		{"0.44.0", "0.45.0-rc.1", Eager, false},
		{Dev, "0.46.0", Eager, false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.running, " vs ", tt.latest, " ", tt.policy), func(t *testing.T) {
			require.Equal(t, tt.want, Outdated(tt.running, tt.latest, tt.policy))
		})
	}
}

func TestPrerelease(t *testing.T) {
	require.True(t, Prerelease("0.44.0-alpha.4"))
	require.False(t, Prerelease("0.44.0"))
	require.False(t, Prerelease(Dev))
}
