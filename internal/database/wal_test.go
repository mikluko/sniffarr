package database

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// fill appends some 3 MiB to the WAL of db.
func fill(t *testing.T, ctx context.Context, db *DB) {
	t.Helper()
	for range 50 {
		_, err := db.W.ExecContext(ctx, "INSERT INTO t VALUES (zeroblob(65536))")
		require.NoError(t, err)
	}
}

func TestCheckpoint(t *testing.T) {
	ctx := context.Background()
	reader := sdkmetric.NewManualReader()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"), Options{Meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))})
	require.NoError(t, err)
	defer db.Close()
	_, err = db.W.ExecContext(ctx, "CREATE TABLE t (x BLOB)")
	require.NoError(t, err)
	var limit int64
	require.NoError(t, db.R.QueryRowContext(ctx, "PRAGMA journal_size_limit").Scan(&limit))
	require.Equal(t, int64(journalSizeLimit), limit)

	fill(t, ctx, db)
	size, err := db.WALSize()
	require.NoError(t, err)
	require.Positive(t, size)
	require.Equal(t, size, walSizeMetric(t, ctx, reader))

	rows, err := db.R.QueryContext(ctx, "SELECT x FROM t")
	require.NoError(t, err)
	require.True(t, rows.Next())
	fill(t, ctx, db)
	c, err := db.Checkpoint(ctx, Passive)
	require.NoError(t, err)
	require.False(t, c.Busy)
	require.Less(t, c.Backfilled, c.Frames, "a reader pins the WAL at its snapshot")
	require.NoError(t, rows.Close())

	c, err = db.Checkpoint(ctx, Truncate)
	require.NoError(t, err)
	require.Equal(t, Checkpointed{}, c, "a truncate resets the WAL before it counts")
	size, err = db.WALSize()
	require.NoError(t, err)
	require.Zero(t, size)
	require.Zero(t, walSizeMetric(t, ctx, reader))
}

func walSizeMetric(t *testing.T, ctx context.Context, reader *sdkmetric.ManualReader) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "sniffarr.db.wal.size" {
				points := m.Data.(metricdata.Gauge[int64]).DataPoints
				require.Len(t, points, 1)
				return points[0].Value
			}
		}
	}
	t.Fatal("sniffarr.db.wal.size is not reported")
	return 0
}
