package sniff

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/bus"
	"sarak.sh/mikluko/sniffarr/internal/bus/bustest"
)

// sightings takes up to n sightings from the consumer, by first byte of the
// hash, with their Nats-Msg-Id.
func sightings(t *testing.T, nc *nats.Conn, consumer string, n int) map[byte]string {
	t.Helper()
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	c, err := js.Consumer(t.Context(), bus.StreamSightings, consumer)
	require.NoError(t, err)
	batch, err := c.Fetch(n, jetstream.FetchMaxWait(time.Second))
	require.NoError(t, err)
	out := map[byte]string{}
	for m := range batch.Messages() {
		out[m.Data()[0]] = m.Headers().Get(jetstream.MsgIDHeader)
		require.NoError(t, m.Ack())
	}
	return out
}

func TestDiscovererPublish(t *testing.T) {
	nc := bustest.Conn(t)
	var mu sync.Mutex
	var stored []Hash
	d := NewDiscoverer(DiscovererConfig{Stored: func(h Hash) {
		mu.Lock()
		defer mu.Unlock()
		stored = append(stored, h)
	}})
	run(t, func(ctx context.Context) error { return d.Publish(ctx, nc) })

	d.Offer(hash(1), sniffarrv1.Source_SOURCE_SAMPLE)
	d.Offer(hash(2), sniffarrv1.Source_SOURCE_ANNOUNCE)
	d.Offer(hash(3), sniffarrv1.Source_SOURCE_SAMPLE)
	require.Eventually(t, func() bool { return d.stats.new.Load() == 3 }, 5*time.Second, 5*time.Millisecond)
	require.Equal(t, map[byte]string{2: "0200000000000000000000000000000000000000"}, sightings(t, nc, bus.ConsumerAnnounce, 10))
	require.Len(t, sightings(t, nc, bus.ConsumerSample, 10), 2)

	require.Eventually(t, func() bool {
		require.NoError(t, nc.Publish(bus.SubjectStored, append(hash(4).bytes(), hash(5).bytes()...)))
		mu.Lock()
		defer mu.Unlock()
		return len(stored) >= 2
	}, 5*time.Second, 10*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []Hash{hash(4), hash(5)}, stored[:2], "every hash the API names as stored is passed on")
}

func TestDiscovererPublishFlushesOnShutdown(t *testing.T) {
	nc := bustest.Conn(t)
	d := NewDiscoverer(DiscovererConfig{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	d.Offer(hash(1), sniffarrv1.Source_SOURCE_ANNOUNCE)
	require.ErrorIs(t, d.Publish(ctx, nc), context.Canceled)
	require.Equal(t, int64(1), d.stats.new.Load(), "what was queued is published and acknowledged before Publish returns")
}

func (h Hash) bytes() []byte { return h[:] }
