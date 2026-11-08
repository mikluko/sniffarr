package orders

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"sarak.sh/mikluko/sniffarr/internal/bus"
	"sarak.sh/mikluko/sniffarr/internal/bus/bustest"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

// clock is a settable time.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fetches returns the started Queue of the fetch orders on a fresh bus, and
// the bus's JetStream.
func fetches(t *testing.T, cfg Config) (*Queue, jetstream.JetStream) {
	t.Helper()
	js, err := jetstream.New(bustest.Conn(t))
	require.NoError(t, err)
	cfg.Stream, cfg.Lanes = bus.StreamFetchOrders, bus.FetchLanes
	q := New(cfg)
	require.NoError(t, q.Start(t.Context(), js))
	return q, js
}

func laneOf(t *testing.T, q *Queue, kind string, p int) bus.Lane {
	t.Helper()
	l, ok := q.Lane(kind, p)
	require.True(t, ok)
	return l
}

func publish(t *testing.T, q *Queue, kind string, p int, bs ...byte) {
	t.Helper()
	items := make([]Item, len(bs))
	for i, b := range bs {
		items[i] = Item{Hash: store.Hash{b}}
	}
	n, err := q.Publish(t.Context(), laneOf(t, q, kind, p), items)
	require.NoError(t, err)
	require.Equal(t, len(bs), n)
}

func hashes(os []*Order) []byte {
	out := make([]byte, len(os))
	for i, o := range os {
		out[i] = o.Hash[0]
	}
	return out
}

func streamMsgs(t *testing.T, js jetstream.JetStream) uint64 {
	t.Helper()
	s, err := js.Stream(t.Context(), bus.StreamFetchOrders)
	require.NoError(t, err)
	info, err := s.Info(t.Context())
	require.NoError(t, err)
	return info.State.Msgs
}

func TestTakeByPriority(t *testing.T) {
	q, _ := fetches(t, Config{Batch: 2})
	publish(t, q, store.TaskFetch, store.PrioritySample, 31, 32)
	publish(t, q, store.TaskFetch, store.PriorityAnnounce, 21)
	publish(t, q, store.TaskFetch, store.PriorityArr, 11, 12, 13)
	publish(t, q, store.TaskPeers, store.PriorityRequest, 5)
	publish(t, q, store.TaskFetch, store.PriorityRequest, 1)

	first, err := q.Take(1, 2, store.PrioritySample)
	require.NoError(t, err)
	require.Equal(t, []byte{5, 1}, hashes(first), "lookups, then P0 fetches")
	require.Equal(t, store.TaskPeers, first[0].Kind)
	publish(t, q, store.TaskFetch, store.PriorityRequest, 2)
	rest, err := q.Take(1, 10, store.PrioritySample)
	require.NoError(t, err)
	require.Equal(t, []byte{2, 11, 12, 13, 21, 31, 32}, hashes(rest), "a P0 published meanwhile goes ahead of the P1 pulled already")
	require.Equal(t, store.PriorityArr, rest[1].Priority)
	require.Equal(t, 1, rest[1].Leases)
	none, err := q.Take(1, 10, store.PrioritySample)
	require.NoError(t, err)
	require.Empty(t, none, "no work is an empty answer at once")
}

func TestTakeUpToPriority(t *testing.T) {
	q, _ := fetches(t, Config{})
	publish(t, q, store.TaskFetch, store.PrioritySample, 31)
	publish(t, q, store.TaskFetch, store.PriorityArr, 11)
	got, err := q.Take(1, 10, store.PriorityArr)
	require.NoError(t, err)
	require.Equal(t, []byte{11}, hashes(got))
	got, err = q.Take(1, 10, store.PrioritySample)
	require.NoError(t, err)
	require.Equal(t, []byte{31}, hashes(got))
}

func TestAckAfterResult(t *testing.T) {
	q, js := fetches(t, Config{})
	publish(t, q, store.TaskFetch, store.PriorityAnnounce, 1)
	got, err := q.Take(7, 1, store.PrioritySample)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, uint64(1), streamMsgs(t, js), "a taken order stays until its result")
	require.Nil(t, q.DoneBy(store.TaskFetch, store.Hash{1}, 8), "another holder's order")
	require.Nil(t, q.Done(store.TaskPeers, store.Hash{1}), "another kind's order")
	o := q.Done(store.TaskFetch, store.Hash{1})
	require.NotNil(t, o)
	require.NoError(t, q.Ack(t.Context(), o))
	require.Zero(t, streamMsgs(t, js))
	require.Nil(t, q.Done(store.TaskFetch, store.Hash{1}), "done once")
}

func TestReleaseNaks(t *testing.T) {
	q, js := fetches(t, Config{})
	publish(t, q, store.TaskFetch, store.PrioritySample, 1, 2)
	got, err := q.Take(1, 1, store.PrioritySample)
	require.NoError(t, err)
	require.Equal(t, []byte{1}, hashes(got))
	_, err = q.Take(2, 1, store.PrioritySample)
	require.NoError(t, err)
	wake := q.Wait()
	q.Release(1)
	select {
	case <-wake:
	default:
		t.Fatal("a release wakes the waiters")
	}
	again, err := q.Take(3, 5, store.PrioritySample)
	require.NoError(t, err)
	require.Equal(t, []byte{1}, hashes(again), "the released order goes out again; 2 is still held")
	require.Equal(t, 2, again[0].Leases)

	q.Release(3)
	late := q.Done(store.TaskFetch, store.Hash{1})
	require.NotNil(t, late, "a result after the release still finds its order")
	require.Equal(t, store.PrioritySample, late.Priority)
	require.NoError(t, q.Ack(t.Context(), late), "but acknowledges nothing")
	require.Equal(t, uint64(2), streamMsgs(t, js))
}

func TestLeaseExpires(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)}
	q, _ := fetches(t, Config{Lease: time.Minute, Now: c.Now})
	publish(t, q, store.TaskFetch, store.PrioritySample, 1)
	_, err := q.Take(1, 1, store.PrioritySample)
	require.NoError(t, err)
	c.add(59 * time.Second)
	q.beat(t.Context())
	require.Equal(t, 1, q.Held(store.TaskFetch))
	c.add(time.Second)
	q.beat(t.Context())
	require.Zero(t, q.Held(store.TaskFetch), "held for the lease, it goes back")
	got, err := q.Take(2, 1, store.PrioritySample)
	require.NoError(t, err)
	require.Equal(t, []byte{1}, hashes(got))
	require.Nil(t, q.DoneBy(store.TaskFetch, store.Hash{1}, 1), "the first holder's failure finds nothing")
}

func TestInProgress(t *testing.T) {
	q, js := fetches(t, Config{Heartbeat: 100 * time.Millisecond})
	cfg := bus.Consumers()[bus.StreamFetchOrders][4]
	cfg.AckWait = 500 * time.Millisecond
	_, err := js.CreateOrUpdateConsumer(t.Context(), bus.StreamFetchOrders, cfg)
	require.NoError(t, err)
	publish(t, q, store.TaskFetch, store.PrioritySample, 1, 2)
	got, err := q.Take(1, 1, store.PrioritySample)
	require.NoError(t, err)
	require.Equal(t, []byte{1}, hashes(got))

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- q.Run(ctx) }()
	time.Sleep(2 * time.Second)
	c, err := js.Consumer(t.Context(), bus.StreamFetchOrders, cfg.Durable)
	require.NoError(t, err)
	info, err := c.Info(t.Context())
	require.NoError(t, err)
	require.Zero(t, info.NumRedelivered, "orders held, or pulled and waiting, are kept in progress past the ack wait")
	require.Equal(t, 2, info.NumAckPending)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	batch, err := c.FetchNoWait(5)
	require.NoError(t, err)
	var back []uint64
	for m := range batch.Messages() {
		meta, err := m.Metadata()
		require.NoError(t, err)
		back = append(back, meta.NumDelivered)
	}
	require.Equal(t, []uint64{2, 2}, back, "at stop every order goes back at once")
}

func TestSecondOrderOfHeld(t *testing.T) {
	q, js := fetches(t, Config{})
	publish(t, q, store.TaskFetch, store.PrioritySample, 1)
	_, err := q.Take(1, 1, store.PrioritySample)
	require.NoError(t, err)
	publish(t, q, store.TaskFetch, store.PriorityAnnounce, 1)
	got, err := q.Take(2, 1, store.PrioritySample)
	require.NoError(t, err)
	require.Empty(t, got, "the promoted copy of a held hash is dropped")
	require.Equal(t, uint64(1), streamMsgs(t, js))
}

func TestRetrySchedule(t *testing.T) {
	q, js := fetches(t, Config{})
	publish(t, q, store.TaskFetch, store.PriorityAnnounce, 1)
	got, err := q.Take(1, 1, store.PrioritySample)
	require.NoError(t, err)
	o := q.DoneBy(store.TaskFetch, got[0].Hash, 1)
	require.NotNil(t, o)
	require.NoError(t, q.Retry(t.Context(), o, o.Lane, time.Now().Add(2*time.Second)))

	none, err := q.Take(1, 1, store.PrioritySample)
	require.NoError(t, err)
	require.Empty(t, none, "a retry waits for its time")
	require.Equal(t, uint64(1), streamMsgs(t, js), "the schedule alone is on the stream")
	var fired []*Order
	require.Eventually(t, func() bool {
		fired, err = q.Take(1, 1, store.PrioritySample)
		require.NoError(t, err)
		return len(fired) == 1
	}, 10*time.Second, 50*time.Millisecond)
	require.Equal(t, store.PriorityAnnounce, fired[0].Priority, "it fires on the order's lane")
	require.Equal(t, 1, fired[0].Attempt)
	require.Equal(t, 1, fired[0].Leases)
	require.Equal(t, uint64(1), streamMsgs(t, js), "the schedule is purged once it fires")
}

func TestRetryReplaces(t *testing.T) {
	q, js := fetches(t, Config{})
	l := laneOf(t, q, store.TaskFetch, store.PrioritySample)
	at := time.Now().Add(2 * time.Second)
	_, err := q.Publish(t.Context(), l, []Item{{Hash: store.Hash{1}, Attempt: 1, At: at, ID: "a"}})
	require.NoError(t, err)
	_, err = q.Publish(t.Context(), l, []Item{{Hash: store.Hash{1}, Attempt: 2, At: at.Add(time.Second), ID: "b"}})
	require.NoError(t, err)
	require.Equal(t, uint64(1), streamMsgs(t, js), "a schedule on the same hash replaces the pending one")
	var fired []*Order
	require.Eventually(t, func() bool {
		fired, err = q.Take(1, 5, store.PrioritySample)
		require.NoError(t, err)
		return len(fired) > 0
	}, 10*time.Second, 50*time.Millisecond)
	require.Len(t, fired, 1)
	require.Equal(t, 2, fired[0].Attempt)
	time.Sleep(time.Second)
	more, err := q.Take(2, 5, store.PrioritySample)
	require.NoError(t, err)
	require.Empty(t, more, "the replaced schedule never fires")
}

func TestPublishDuplicates(t *testing.T) {
	q, _ := fetches(t, Config{})
	l := laneOf(t, q, store.TaskFetch, store.PrioritySample)
	n, err := q.Publish(t.Context(), l, []Item{{Hash: store.Hash{1}, ID: "x"}, {Hash: store.Hash{2}, ID: "y"}})
	require.NoError(t, err)
	require.Equal(t, 2, n)
	n, err = q.Publish(t.Context(), l, []Item{{Hash: store.Hash{1}, ID: "x"}, {Hash: store.Hash{3}}})
	require.NoError(t, err)
	require.Equal(t, 1, n, "an ID within the window is a duplicate")
}

func TestLevels(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	q, _ := fetches(t, Config{})
	publish(t, q, store.TaskPeers, store.PriorityRequest, 1)
	publish(t, q, store.TaskFetch, store.PriorityArr, 11, 12)
	publish(t, q, store.TaskFetch, store.PrioritySample, 21, 22, 23, 24, 25, 26)
	_, err := q.Take(1, 1, store.PriorityArr)
	require.NoError(t, err)

	levels, err := q.Levels(t.Context(), now)
	require.NoError(t, err)
	require.Len(t, levels, 2)
	tasks := func(kind string) []int64 {
		var n []int64
		for _, l := range levels[kind].Levels {
			n = append(n, l.Tasks)
		}
		return n
	}
	require.Equal(t, []int64{0, 2, 0, 6}, tasks(store.TaskFetch), "delivered orders count")
	require.Equal(t, []int64{1, 0, 0, 0}, tasks(store.TaskPeers))
	require.False(t, levels[store.TaskFetch].Levels[1].Known, "no pace yet")

	for i := range 30 {
		q.Finished(store.TaskFetch, now.Add(time.Duration(i)*2*time.Second))
	}
	levels, err = q.Levels(t.Context(), now.Add(time.Minute))
	require.NoError(t, err)
	require.InDelta(t, 0.5, levels[store.TaskFetch].PerSecond, 1e-9)
	require.Equal(t, 16*time.Second, levels[store.TaskFetch].Levels[3].ETA, "eight at P3 or ahead at 30 a minute")
	require.False(t, levels[store.TaskPeers].Levels[0].Known, "a lookup goes at its own pace")
}

func TestPurge(t *testing.T) {
	q, js := fetches(t, Config{})
	publish(t, q, store.TaskFetch, store.PriorityArr, 1, 2)
	_, err := q.Publish(t.Context(), laneOf(t, q, store.TaskFetch, store.PriorityArr), []Item{{Hash: store.Hash{3}, At: time.Now().Add(time.Hour)}})
	require.NoError(t, err)
	require.NoError(t, q.Purge(t.Context()))
	s, err := js.Stream(t.Context(), bus.StreamFetchOrders)
	require.NoError(t, err)
	info, err := s.Info(t.Context())
	require.NoError(t, err)
	require.Zero(t, info.State.Msgs, "the orders and the schedule go")
	got, err := q.Take(1, 10, store.PrioritySample)
	require.NoError(t, err)
	require.Empty(t, got)
}
