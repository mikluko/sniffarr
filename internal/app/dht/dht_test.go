package dht

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/fx/fxtest"
)

func TestTempSeed(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	lc := fxtest.NewLifecycle(t)
	dir, err := tempSeed(lc)
	require.NoError(t, err)
	require.DirExists(t, dir)
	require.Contains(t, filepath.Base(dir), "sniffarr-probe-")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "piece"), []byte("x"), 0o600))

	other, err := tempSeed(fxtest.NewLifecycle(t))
	require.NoError(t, err)
	require.NotEqual(t, dir, other, "each start gets a fresh directory")

	require.NoError(t, lc.Start(context.Background()))
	require.NoError(t, lc.Stop(context.Background()))
	require.NoDirExists(t, dir, "removed at stop with what it holds")
}
