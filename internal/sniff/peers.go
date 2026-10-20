package sniff

import (
	"context"
	"log/slog"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/sketch"
)

const (
	DefaultPeersCap   = 100000
	DefaultPeersFlush = 3 * time.Minute
)

// PeerSink takes the peers seen sharing an infohash.
type PeerSink interface {
	Add(h Hash, peers []netip.AddrPort)
}

// PeersConfig tunes a Peers. Zero fields take the default named in their
// doc.
type PeersConfig struct {
	Ingest sniffarrv1connect.IngestServiceClient
	// Cap is the most sketches waiting to be sent, the current hour's and
	// earlier batches together; DefaultPeersCap. A current hour that
	// reaches it is sent at once; while the server is unreachable, the
	// oldest unsent batch is dropped instead.
	Cap int
	// Flush is how often the sketches are sent; DefaultPeersFlush.
	Flush time.Duration
	// Backoff is the wait after a failed send; DefaultBackoff.
	Backoff time.Duration
	// Report is the interval of progress logs; DefaultReport.
	Report time.Duration
	// Log discards everything when nil.
	Log *slog.Logger
	// Meter records metrics; the global MeterProvider when nil.
	Meter metric.MeterProvider
	// Now is time.Now when nil.
	Now func() time.Time
}

// Peers counts the peers seen per infohash in a sketch of the current UTC
// hour and sends the sketches to the server's Peers. A peer is kept as its
// hash only. The zero value is not usable; use NewPeers.
type Peers struct {
	cfg PeersConfig

	mu      sync.Mutex
	key     sketch.Key
	keyed   bool
	ready   chan struct{}
	hour    int64
	cur     map[Hash]*sketch.Sketch
	pending []peerBatch
	early   chan struct{}

	stats    struct{ peers, unkeyed, evicted, merged, dropped atomic.Int64 }
	peers    metric.Int64Counter
	sketches metric.Int64Counter
}

// peerBatch is the sketches of one hour, taken from the current map.
type peerBatch struct {
	hour     int64
	sketches map[Hash]*sketch.Sketch
}

// NewPeers returns a Peers for cfg.
func NewPeers(cfg PeersConfig) *Peers {
	orDefault(&cfg.Cap, DefaultPeersCap)
	orDefault(&cfg.Flush, DefaultPeersFlush)
	orDefault(&cfg.Backoff, DefaultBackoff)
	orDefault(&cfg.Report, DefaultReport)
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	m := meter(cfg.Meter)
	return &Peers{
		cfg:      cfg,
		cur:      map[Hash]*sketch.Sketch{},
		ready:    make(chan struct{}),
		early:    make(chan struct{}, 1),
		peers:    counter(m, "sniffarr.sniff.peers", "{peer}", "Peers added to sketches."),
		sketches: counter(m, "sniffarr.sniff.sketches", "{sketch}", "Sketches sent, by the server's verdict, or evicted unsent."),
	}
}

// Ready returns a channel closed once Run has the key and Add counts.
func (p *Peers) Ready() <-chan struct{} {
	return p.ready
}

// Add counts the peers as seen sharing h now. It never blocks on the
// network and is safe for concurrent use; peers seen before Run has the key
// are dropped.
func (p *Peers) Add(h Hash, peers []netip.AddrPort) {
	if len(peers) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.keyed {
		p.stats.unkeyed.Add(int64(len(peers)))
		return
	}
	if hour := p.cfg.Now().Truncate(time.Hour).Unix(); hour != p.hour {
		p.rotate()
		p.hour = hour
	}
	s, ok := p.cur[h]
	if !ok {
		if p.waiting() >= p.cfg.Cap {
			p.evict()
		}
		fresh := sketch.New()
		s = &fresh
		p.cur[h] = s
	}
	for _, peer := range peers {
		s.Add(p.key.Hash(peer))
	}
	p.stats.peers.Add(int64(len(peers)))
	p.peers.Add(context.Background(), int64(len(peers)))
}

// waiting counts the sketches not yet taken for sending.
func (p *Peers) waiting() int {
	n := len(p.cur)
	for _, b := range p.pending {
		n += len(b.sketches)
	}
	return n
}

// rotate moves the current sketches, if any, behind the pending batches.
func (p *Peers) rotate() {
	if len(p.cur) == 0 {
		return
	}
	p.pending = append(p.pending, peerBatch{hour: p.hour, sketches: p.cur})
	p.cur = map[Hash]*sketch.Sketch{}
}

// evict makes room for a sketch: it drops the oldest pending batch, or,
// with none pending, rotates the current hour and asks for an early send.
func (p *Peers) evict() {
	if len(p.pending) > 0 {
		n := len(p.pending[0].sketches)
		p.pending = p.pending[1:]
		p.stats.evicted.Add(int64(n))
		p.sketches.Add(context.Background(), int64(n), sketchResult("evicted"))
		return
	}
	p.rotate()
	select {
	case p.early <- struct{}{}:
	default:
	}
}

// take returns every batch waiting to be sent, the current hour's last, and
// leaves none waiting.
func (p *Peers) take() []peerBatch {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rotate()
	out := p.pending
	p.pending = nil
	return out
}

// untake puts batches that could not be sent back in front of the pending
// ones.
func (p *Peers) untake(batches []peerBatch) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pending = append(batches, p.pending...)
}

// Run gets the hash key from the server, then sends the sketches every
// Flush and whenever the current hour fills, until ctx is done; it then sends
// what is left for at most a few seconds. A send that fails is retried with
// everything it carried.
func (p *Peers) Run(ctx context.Context) error {
	if err := p.obtainKey(ctx); err != nil {
		return err
	}
	go every(ctx, p.cfg.Report, func() { p.log("peers") })
	tick := time.NewTicker(p.cfg.Flush)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
		case <-p.early:
		case <-ctx.Done():
			flush, cancel := context.WithTimeout(context.WithoutCancel(ctx), flushTimeout)
			defer cancel()
			if err := p.send(flush, p.take()); err != nil {
				p.cfg.Log.Warn("peers", "err", err)
			}
			p.log("peers stopped")
			return ctx.Err()
		}
		p.flush(ctx)
	}
}

// obtainKey asks the server for the hash key until it answers or ctx is
// done.
func (p *Peers) obtainKey(ctx context.Context) error {
	for {
		res, err := p.cfg.Ingest.HashKey(ctx, connect.NewRequest(&sniffarrv1.HashKeyRequest{}))
		if err == nil {
			k, err := sketch.ParseKey(res.Msg.GetKey())
			if err == nil {
				p.mu.Lock()
				p.key, p.keyed = k, true
				p.mu.Unlock()
				close(p.ready)
				return nil
			}
			p.cfg.Log.Error("hash key", "err", err)
		} else {
			p.cfg.Log.Warn("hash key", "err", err)
		}
		if err := sleep(ctx, p.cfg.Backoff); err != nil {
			return err
		}
	}
}

// flush sends what is waiting, retrying after Backoff until it is sent or
// ctx is done.
func (p *Peers) flush(ctx context.Context) {
	batches := p.take()
	for len(batches) > 0 {
		err := p.send(ctx, batches)
		if err == nil {
			return
		}
		p.cfg.Log.Warn("peers", "err", err)
		p.untake(batches)
		if sleep(ctx, p.cfg.Backoff) != nil {
			return
		}
		batches = p.take()
	}
}

// send sends the batches on one Peers stream.
func (p *Peers) send(ctx context.Context, batches []peerBatch) error {
	if len(batches) == 0 {
		return nil
	}
	stream := p.cfg.Ingest.Peers(ctx)
	for _, b := range batches {
		for h, s := range b.sketches {
			if err := stream.Send(&sniffarrv1.PeersRequest{Infohash: h[:], Hour: b.hour, Sketch: s.Bytes()}); err != nil {
				_, err := stream.CloseAndReceive()
				return err
			}
		}
	}
	res, err := stream.CloseAndReceive()
	if err != nil {
		return err
	}
	p.stats.merged.Add(res.Msg.GetMerged())
	p.stats.dropped.Add(res.Msg.GetDropped())
	p.sketches.Add(ctx, res.Msg.GetMerged(), sketchResult("merged"))
	p.sketches.Add(ctx, res.Msg.GetDropped(), sketchResult("dropped"))
	return nil
}

func (p *Peers) log(msg string) {
	p.mu.Lock()
	waiting := p.waiting()
	p.mu.Unlock()
	p.cfg.Log.Info(msg, "peers", p.stats.peers.Load(), "unkeyed", p.stats.unkeyed.Load(), "waiting", waiting,
		"merged", p.stats.merged.Load(), "dropped", p.stats.dropped.Load(), "evicted", p.stats.evicted.Load())
}

func sketchResult(r string) metric.AddOption {
	return metric.WithAttributes(attribute.String("sniffarr.sketch.result", r))
}
