package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/mikluko/sniffarr/internal/database"
)

func openQueue(t *testing.T, opts ...QueueOptions) *Queue {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	var o QueueOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	q, err := NewQueue(ctx, db, o)
	require.NoError(t, err)
	return q
}

func enqueueAt(t *testing.T, q *Queue, at time.Time, discovery string, bs ...byte) (int, int) {
	t.Helper()
	queued, known, err := q.Enqueue(context.Background(), sightings(discovery, bs...), at)
	require.NoError(t, err)
	return queued, known
}

func sightings(discovery string, bs ...byte) []Sighting {
	var out []Sighting
	for _, b := range bs {
		out = append(out, Sighting{Hash: hash(b), Discovery: discovery})
	}
	return out
}

func enqueue(t *testing.T, q *Queue, discovery string, bs ...byte) (int, int) {
	t.Helper()
	queued, known, err := q.Enqueue(context.Background(), sightings(discovery, bs...), t0)
	require.NoError(t, err)
	return queued, known
}

func lease(t *testing.T, q *Queue, owner int64, n int, at time.Time) []Hash {
	t.Helper()
	hs, _, err := q.Lease(context.Background(), owner, n, at, time.Minute)
	require.NoError(t, err)
	return hs
}

func TestEnqueue(t *testing.T) {
	q := openQueue(t)
	queued, known := enqueue(t, q, Sample, 1, 2, 2)
	require.Equal(t, 2, queued)
	require.Equal(t, 1, known)
	queued, known = enqueue(t, q, Announce, 1)
	require.Zero(t, queued)
	require.Equal(t, 1, known)

	d, err := q.Discovery(context.Background(), hash(1))
	require.NoError(t, err)
	require.Equal(t, Announce, d, "an announce sighting moves a queued sample ahead")
	_, err = q.Discovery(context.Background(), hash(3))
	require.ErrorIs(t, err, ErrNotQueued)
}

func TestLeaseOrder(t *testing.T) {
	q := openQueue(t)
	enqueue(t, q, Sample, 1, 2)
	enqueue(t, q, Announce, 3)
	enqueue(t, q, Sample, 4)
	enqueue(t, q, Announce, 4)

	require.Equal(t, []Hash{hash(3), hash(4), hash(1)}, lease(t, q, 1, 3, t0), "announced first, then oldest")
	require.Equal(t, []Hash{hash(2)}, lease(t, q, 2, 3, t0))
	require.Empty(t, lease(t, q, 2, 3, t0), "everything is leased")
}

func TestLeaseExpiryAndRelease(t *testing.T) {
	ctx := context.Background()
	q := openQueue(t)
	enqueue(t, q, Sample, 1, 2)
	require.Len(t, lease(t, q, 1, 2, t0), 2)

	require.Empty(t, lease(t, q, 2, 2, t0.Add(59*time.Second)))
	hs, expired, err := q.Lease(ctx, 2, 2, t0.Add(time.Minute), time.Minute)
	require.NoError(t, err)
	require.Equal(t, []Hash{hash(1), hash(2)}, hs, "expired leases are free")
	require.Equal(t, 2, expired)

	require.NoError(t, q.Release(ctx, 1))
	require.Empty(t, lease(t, q, 3, 2, t0.Add(time.Minute)), "releasing a stale owner frees nothing")
	require.NoError(t, q.Release(ctx, 2))
	require.Len(t, lease(t, q, 3, 2, t0.Add(time.Minute)), 2)

	require.NoError(t, q.Reset(ctx))
	require.Len(t, lease(t, q, 4, 2, t0.Add(time.Minute)), 2)
}

func TestFail(t *testing.T) {
	ctx := context.Background()
	q := openQueue(t)
	enqueue(t, q, Sample, 1)
	require.Len(t, lease(t, q, 1, 1, t0), 1)

	dropped, err := q.Fail(ctx, hash(1), 2, t0, 3, time.Minute)
	require.NoError(t, err)
	require.False(t, dropped)
	require.Empty(t, lease(t, q, 2, 1, t0.Add(30*time.Second)), "a failure from another owner is ignored")

	dropped, err = q.Fail(ctx, hash(1), 1, t0, 3, time.Minute)
	require.NoError(t, err)
	require.False(t, dropped)
	require.Empty(t, lease(t, q, 2, 1, t0.Add(59*time.Second)), "waits out the backoff")
	require.Len(t, lease(t, q, 2, 1, t0.Add(time.Minute)), 1)

	dropped, err = q.Fail(ctx, hash(1), 2, t0.Add(time.Minute), 3, time.Minute)
	require.NoError(t, err)
	require.False(t, dropped)
	require.Empty(t, lease(t, q, 3, 1, t0.Add(2*time.Minute+59*time.Second)), "the backoff doubles")
	require.Len(t, lease(t, q, 3, 1, t0.Add(3*time.Minute)), 1)

	dropped, err = q.Fail(ctx, hash(1), 3, t0.Add(3*time.Minute), 3, time.Minute)
	require.NoError(t, err)
	require.True(t, dropped, "the third failure drops it")
	_, err = q.Discovery(ctx, hash(1))
	require.ErrorIs(t, err, ErrNotQueued)

	queued, known := enqueueAt(t, q, t0.Add(DefaultDeadFor), Sample, 1)
	require.Zero(t, queued, "a dropped hash stays dead")
	require.Equal(t, 1, known)
	queued, _ = enqueueAt(t, q, t0.Add(3*time.Minute+DefaultDeadFor), Sample, 1)
	require.Equal(t, 1, queued, "and queues again once its time is up")
}

func TestComplete(t *testing.T) {
	ctx := context.Background()
	q := openQueue(t)
	enqueue(t, q, Sample, 1, 2)
	require.NoError(t, q.Complete(ctx, hash(1)))
	n, err := q.Len(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestTrimBySize(t *testing.T) {
	ctx := context.Background()
	q := openQueue(t, QueueOptions{MaxSize: 3})
	enqueueAt(t, q, t0, Announce, 1)
	enqueueAt(t, q, t0.Add(time.Second), Sample, 2, 3)
	enqueueAt(t, q, t0.Add(2*time.Second), Announce, 4)
	enqueueAt(t, q, t0.Add(3*time.Second), Sample, 5, 6)
	require.Len(t, lease(t, q, 1, 1, t0), 1, "leases 1, an announced one")

	tr, err := q.Trim(ctx, t0.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, Trimmed{Oversize: 3}, tr)
	n, err := q.Len(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, n)
	require.NoError(t, q.Release(ctx, 1))
	require.Equal(t, []Hash{hash(1), hash(4), hash(6)}, lease(t, q, 2, 10, t0.Add(time.Minute)), "the oldest samples go first")

	tr, err = q.Trim(ctx, t0.Add(time.Minute))
	require.NoError(t, err)
	require.Zero(t, tr.Oversize)
	enqueueAt(t, q, t0.Add(time.Minute), Sample, 7, 8)
	require.NoError(t, q.Reset(ctx))
	_, err = q.Trim(ctx, t0.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, []Hash{hash(1), hash(4), hash(8)}, lease(t, q, 3, 10, t0.Add(time.Minute)), "announced ones stay while samples remain")
}

func TestTrimByAgeAndDeadExpiry(t *testing.T) {
	ctx := context.Background()
	q := openQueue(t, QueueOptions{MaxAge: time.Hour, DeadFor: 24 * time.Hour})
	enqueueAt(t, q, t0, Announce, 1)
	enqueueAt(t, q, t0.Add(30*time.Minute), Sample, 2)

	tr, err := q.Trim(ctx, t0.Add(time.Hour+time.Minute))
	require.NoError(t, err)
	require.Equal(t, Trimmed{Aged: 1}, tr, "an old announced hash ages out too")
	queued, known := enqueueAt(t, q, t0.Add(2*time.Hour), Sample, 1)
	require.Zero(t, queued, "an evicted hash is dead")
	require.Equal(t, 1, known)

	tr, err = q.Trim(ctx, t0.Add(26*time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, tr.Expired)
	require.Equal(t, 1, tr.Aged, "2 aged meanwhile")
	queued, _ = enqueueAt(t, q, t0.Add(26*time.Hour), Sample, 1)
	require.Equal(t, 1, queued, "a forgotten dead hash queues again")
}

func TestTrimMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	q := openQueue(t, QueueOptions{MaxSize: 1, Meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))})
	enqueueAt(t, q, t0, Sample, 1, 2, 3)
	_, err := q.Trim(context.Background(), t0)
	require.NoError(t, err)

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	got := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, p := range d.DataPoints {
					why, _ := p.Attributes.Value("sniffarr.queue.eviction")
					got[m.Name+"/"+why.AsString()] += p.Value
				}
			case metricdata.Gauge[int64]:
				for _, p := range d.DataPoints {
					got[m.Name] += p.Value
				}
			}
		}
	}
	require.EqualValues(t, 2, got["sniffarr.queue.evictions/size"])
	require.EqualValues(t, 2, got["sniffarr.queue.dead"])
	require.EqualValues(t, 1, got["sniffarr.queue.depth"])
}
