package sniff

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/sketch"
)

// addrs returns n peers numbered from first.
func addrs(first, n int) []netip.AddrPort {
	out := make([]netip.AddrPort, n)
	for i := range out {
		j := first + i
		out[i] = netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, byte(j >> 16), byte(j >> 8), byte(j)}), 6881)
	}
	return out
}

// cardinality returns the distinct peers of the sketches together.
func cardinality(t *testing.T, ms ...*sniffarrv1.PeersRequest) uint64 {
	t.Helper()
	all := sketch.New()
	for _, m := range ms {
		s, err := sketch.Parse(m.GetSketch())
		require.NoError(t, err)
		require.NoError(t, all.Union(s))
	}
	return all.Cardinality()
}

// clock is a settable time.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) set(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = at
}

var hour = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func newPeers(t *testing.T, ingest *fakeIngest, cfg PeersConfig) (*Peers, *clock) {
	t.Helper()
	c := &clock{at: hour.Add(10 * time.Minute)}
	cfg.Ingest, cfg.Now = ingest.client(t), c.now
	if cfg.Flush == 0 {
		cfg.Flush = 20 * time.Millisecond
	}
	if cfg.Backoff == 0 {
		cfg.Backoff = 5 * time.Millisecond
	}
	p := NewPeers(cfg)
	run(t, p.Run)
	return p, c
}

func TestPeersFlush(t *testing.T) {
	ingest := newFake()
	ingest.unknown[hash(2)] = true
	p, _ := newPeers(t, ingest, PeersConfig{})
	<-p.Ready()
	p.Add(hash(1), addrs(0, 100))
	p.Add(hash(1), addrs(50, 100))
	p.Add(hash(2), addrs(0, 10))
	got := ingest.received(t, hash(1), 1)
	require.Equal(t, hour.Unix(), got[0].GetHour(), "the sketch is of the hour the peers were seen in")
	require.InDelta(t, 150, cardinality(t, got[0]), 15, "both additions are in one sketch, hashed with the server's key")
	want := sketch.New()
	for _, a := range addrs(0, 150) {
		want.Add(ingest.key.Hash(a))
	}
	require.Equal(t, want.Cardinality(), cardinality(t, got[0]))
	require.Eventually(t, func() bool { return p.stats.dropped.Load() == 1 }, 5*time.Second, time.Millisecond, "the server dropped the unknown hash")
	require.Equal(t, int64(1), p.stats.merged.Load())
}

func TestPeersNewHourAndRetry(t *testing.T) {
	ingest := newFake()
	ingest.failPeers = 1
	p, c := newPeers(t, ingest, PeersConfig{Flush: time.Hour})
	<-p.Ready()
	p.Add(hash(1), addrs(0, 10))
	c.set(hour.Add(70 * time.Minute))
	p.Add(hash(1), addrs(10, 10))
	p.flush(context.Background())
	got := ingest.received(t, hash(1), 2)
	require.Equal(t, []int64{hour.Unix(), hour.Add(time.Hour).Unix()}, []int64{got[0].GetHour(), got[1].GetHour()}, "a new hour starts a new sketch; the failed send was retried")
	require.InDelta(t, 20, cardinality(t, got...), 2)
}

func TestPeersCap(t *testing.T) {
	ingest := newFake()
	p, _ := newPeers(t, ingest, PeersConfig{Flush: time.Hour, Cap: 2})
	<-p.Ready()
	p.Add(hash(1), addrs(0, 1))
	p.Add(hash(2), addrs(0, 1))
	p.Add(hash(3), addrs(0, 1))
	for _, h := range []Hash{hash(1), hash(2), hash(3)} {
		ingest.received(t, h, 1)
	}
	require.Eventually(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.waiting() == 0
	}, 5*time.Second, time.Millisecond, "a full hour is sent at once, with what came meanwhile")

	ingest.mu.Lock()
	ingest.failPeers = 1 << 30
	ingest.mu.Unlock()
	p.Add(hash(4), addrs(0, 1))
	batches := p.take()
	require.Error(t, p.send(context.Background(), batches))
	p.untake(batches)
	p.Add(hash(5), addrs(0, 1))
	p.Add(hash(6), addrs(0, 1))
	require.Equal(t, int64(1), p.stats.evicted.Load(), "with the server down, the unsent batch is dropped for new sketches")
	p.mu.Lock()
	waiting := p.waiting()
	p.mu.Unlock()
	require.Equal(t, 2, waiting)
}

func TestPeersBeforeKey(t *testing.T) {
	ingest := newFake()
	ingest.failKey = 1000
	c := &clock{at: hour}
	p := NewPeers(PeersConfig{Ingest: ingest.client(t), Now: c.now, Backoff: time.Millisecond})
	cancel := run(t, p.Run)
	p.Add(hash(1), addrs(0, 5))
	require.Equal(t, int64(5), p.stats.unkeyed.Load(), "peers seen before the key is known are dropped")
	cancel()
}

func TestPeersFlushOnShutdown(t *testing.T) {
	ingest := newFake()
	c := &clock{at: hour}
	p := NewPeers(PeersConfig{Ingest: ingest.client(t), Now: c.now, Flush: time.Hour, Backoff: time.Millisecond})
	cancel := run(t, p.Run)
	<-p.Ready()
	p.Add(hash(1), addrs(0, 5))
	cancel()
	got := ingest.received(t, hash(1), 1)
	require.InDelta(t, 5, cardinality(t, got...), 1)
}

func TestPeersAnnouncedNeedsAdmission(t *testing.T) {
	ingest := newFake()
	p, _ := newPeers(t, ingest, PeersConfig{Flush: time.Hour})
	<-p.Ready()
	p.Announced(hash(1), addrs(0, 1)[0])
	require.Equal(t, int64(1), p.stats.unknown.Load(), "an announce of a hash the server has not reported as stored is dropped")
	p.mu.Lock()
	require.Empty(t, p.cur)
	p.mu.Unlock()

	p.Admit(hash(1))
	p.Announced(hash(1), addrs(1, 1)[0])
	p.Announced(hash(1), addrs(2, 1)[0])
	p.Add(hash(2), addrs(0, 3))
	p.flush(context.Background())
	got := ingest.received(t, hash(1), 1)
	require.EqualValues(t, 2, cardinality(t, got...), "admitted, the announces count; the first one is lost")
	got = ingest.received(t, hash(2), 1)
	require.EqualValues(t, 3, cardinality(t, got...), "Add counts without admission")
	require.Equal(t, int64(1), p.stats.unknown.Load())
}
