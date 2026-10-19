package sniffer

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/metric"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
)

// DiscovererConfig tunes a Discoverer. Zero fields take the default named in
// their doc.
type DiscovererConfig struct {
	Ingest sniffarrv1connect.IngestServiceClient
	// Queue is how many offered hashes wait to be sent; DefaultQueue.
	// Hashes offered to a full queue are dropped.
	Queue int
	// Remember is how many offered hashes are remembered and not offered
	// again; DefaultRemember.
	Remember int
	// StreamFor is how long one Sight stream stays open; DefaultStreamFor.
	StreamFor time.Duration
	// Backoff is the wait after a failed stream; DefaultBackoff.
	Backoff time.Duration
	// Report is the interval of progress logs; DefaultReport.
	Report time.Duration
	// Log discards everything when nil.
	Log *slog.Logger
	// Meter records metrics; the global MeterProvider when nil.
	Meter metric.MeterProvider
}

// Discoverer streams the hashes offered to it to the server's Sight.
type Discoverer struct {
	cfg       DiscovererConfig
	recent    *recent[Hash]
	offers    chan *sniffarrv1.SightRequest
	stats     struct{ offered, dropped, new, known atomic.Int64 }
	sightings metric.Int64Counter
}

// NewDiscoverer returns a Discoverer for cfg.
func NewDiscoverer(cfg DiscovererConfig) *Discoverer {
	orDefault(&cfg.Queue, DefaultQueue)
	orDefault(&cfg.Remember, DefaultRemember)
	orDefault(&cfg.StreamFor, DefaultStreamFor)
	orDefault(&cfg.Backoff, DefaultBackoff)
	orDefault(&cfg.Report, DefaultReport)
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	return &Discoverer{
		cfg:       cfg,
		recent:    newRecent[Hash](cfg.Remember),
		offers:    make(chan *sniffarrv1.SightRequest, cfg.Queue),
		sightings: counter(meter(cfg.Meter), "sniffarr.sniffer.sightings", "{infohash}", "Infohashes sighted and not sighted recently, by how."),
	}
}

// Offer queues a hash for Sight unless it was offered recently. It never
// blocks and is safe for concurrent use.
func (d *Discoverer) Offer(h Hash, source sniffarrv1.Source) {
	if !d.recent.add(h) {
		return
	}
	d.stats.offered.Add(1)
	d.sightings.Add(context.Background(), 1, sourceAttr(source))
	select {
	case d.offers <- &sniffarrv1.SightRequest{Infohash: h[:], Source: source}:
	default:
		d.stats.dropped.Add(1)
	}
}

// Run streams offered hashes to Sight, one stream per StreamFor, until ctx is
// done, then sends what is still queued for at most a few seconds. A stream
// that fails is retried with everything sent on it.
func (d *Discoverer) Run(ctx context.Context) error {
	go every(ctx, d.cfg.Report, func() { d.log("discovery") })
	sender := sightSender{d: d}
	tick := time.NewTicker(d.cfg.StreamFor)
	defer tick.Stop()
	for {
		select {
		case m := <-d.offers:
			sender.pending = append(sender.pending, m)
			sender.send(ctx)
		case <-tick.C:
			sender.close(ctx)
		case <-ctx.Done():
			flush, cancel := context.WithTimeout(context.WithoutCancel(ctx), flushTimeout)
			defer cancel()
			for drained := false; !drained; {
				select {
				case m := <-d.offers:
					sender.pending = append(sender.pending, m)
				default:
					drained = true
				}
			}
			sender.close(flush)
			d.log("discovery stopped")
			return ctx.Err()
		}
	}
}

func (d *Discoverer) log(msg string) {
	d.cfg.Log.Info(msg, "offered", d.stats.offered.Load(), "dropped", d.stats.dropped.Load(),
		"queue", len(d.offers), "new", d.stats.new.Load(), "known", d.stats.known.Load())
}

// sightSender holds one Sight stream and the sightings sent on it since it
// opened.
type sightSender struct {
	d       *Discoverer
	stream  *connect.ClientStreamForClient[sniffarrv1.SightRequest, sniffarrv1.SightResponse]
	cancel  context.CancelFunc
	sent    int
	pending []*sniffarrv1.SightRequest
}

// send sends the pending sightings not yet sent, opening a stream if none is
// open, and retries on a new stream after a failure.
func (u *sightSender) send(ctx context.Context) {
	for u.sent < len(u.pending) {
		if u.stream == nil {
			sctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
			u.stream, u.cancel = u.d.cfg.Ingest.Sight(sctx), cancel
			u.sent = 0
		}
		if err := u.stream.Send(u.pending[u.sent]); err != nil {
			_, err := u.stream.CloseAndReceive()
			u.drop()
			u.d.cfg.Log.Warn("sight", "err", err)
			if sleep(ctx, u.d.cfg.Backoff) != nil {
				return
			}
			continue
		}
		u.sent++
	}
}

// close sends what is pending and closes the stream, keeping the pending
// sightings for the next stream if the server did not confirm them.
func (u *sightSender) close(ctx context.Context) {
	for len(u.pending) > 0 {
		u.send(ctx)
		if u.stream == nil {
			return
		}
		res, err := u.stream.CloseAndReceive()
		u.drop()
		if err == nil {
			u.d.stats.new.Add(res.Msg.GetNew())
			u.d.stats.known.Add(res.Msg.GetKnown())
			u.pending = u.pending[:0]
			return
		}
		u.d.cfg.Log.Warn("sight", "err", err)
		if sleep(ctx, u.d.cfg.Backoff) != nil {
			return
		}
	}
}

func (u *sightSender) drop() {
	if u.cancel != nil {
		u.cancel()
	}
	u.stream, u.cancel, u.sent = nil, nil, 0
}
