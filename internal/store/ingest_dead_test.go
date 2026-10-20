package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func evictions(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	got := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "sniffarr.queue.evictions" {
				continue
			}
			for _, p := range m.Data.(metricdata.Sum[int64]).DataPoints {
				why, _ := p.Attributes.Value("sniffarr.queue.eviction")
				got[why.AsString()] += p.Value
			}
		}
	}
	return got
}

func TestNoPeers(t *testing.T) {
	ctx := context.Background()
	reader := sdkmetric.NewManualReader()
	q := openQueue(t, QueueOptions{NoPeersFor: time.Hour, Meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))})
	enqueue(t, q, Sample, 1)
	require.Len(t, lease(t, q, 1, 1, t0), 1)

	require.NoError(t, q.NoPeers(ctx, hash(1), 2, t0))
	_, err := q.Discovery(ctx, hash(1))
	require.NoError(t, err, "a report from another owner is ignored")

	require.NoError(t, q.NoPeers(ctx, hash(1), 1, t0))
	_, err = q.Discovery(ctx, hash(1))
	require.ErrorIs(t, err, ErrNotQueued, "dropped at the first report")
	require.EqualValues(t, 1, evictions(t, reader)["no_peers"])

	queued, known := enqueueAt(t, q, t0.Add(59*time.Minute), Sample, 1)
	require.Zero(t, queued, "a sample leaves it dead")
	require.Equal(t, 1, known)
	queued, _ = enqueueAt(t, q, t0.Add(time.Hour), Sample, 1)
	require.Equal(t, 1, queued, "for NoPeersFor, not DeadFor")
}

func TestAnnounceRevives(t *testing.T) {
	ctx := context.Background()
	q := openQueue(t)
	enqueue(t, q, Sample, 1, 2)
	require.Len(t, lease(t, q, 1, 2, t0), 2)
	require.NoError(t, q.NoPeers(ctx, hash(1), 1, t0))
	dropped, err := q.Fail(ctx, hash(2), 1, t0, 1, time.Minute)
	require.NoError(t, err)
	require.True(t, dropped)

	queued, known := enqueueAt(t, q, t0.Add(time.Minute), Announce, 1, 2)
	require.Equal(t, 2, queued, "an announce revives any dead hash")
	require.Zero(t, known)
	d, err := q.Discovery(ctx, hash(1))
	require.NoError(t, err)
	require.Equal(t, Announce, d)
	require.Equal(t, []Hash{hash(1), hash(2)}, lease(t, q, 2, 2, t0.Add(time.Minute)), "revived with no attempts behind it")
}
