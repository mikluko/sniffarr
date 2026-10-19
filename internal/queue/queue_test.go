package queue

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/database"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func open(t *testing.T) *Queue {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	q, err := New(ctx, db)
	require.NoError(t, err)
	return q
}

func hash(b byte) Hash {
	return Hash{b}
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
	queued, known, err := q.Enqueue(context.Background(), sightings(discovery, bs...))
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
	q := open(t)
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
	q := open(t)
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
	q := open(t)
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
	q := open(t)
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

	queued, _ := enqueue(t, q, Sample, 1)
	require.Equal(t, 1, queued, "a dropped hash queues again")
}

func TestComplete(t *testing.T) {
	ctx := context.Background()
	q := open(t)
	enqueue(t, q, Sample, 1, 2)
	require.NoError(t, q.Complete(ctx, hash(1)))
	n, err := q.Len(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}
