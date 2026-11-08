package sniff

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/bus"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

// DefaultPublishPending is how many published sightings may await the
// server's acknowledgement at once.
const DefaultPublishPending = 4096

// publishTimeout bounds the wait for the server to acknowledge a sighting.
const publishTimeout = 30 * time.Second

// Publish publishes the offered hashes to the sightings stream on nc, until
// ctx is done, then what is still queued, waiting at most flushTimeout for
// the acknowledgements; Stored gets every hash named on bus.SubjectStored. A
// sighting the server does not acknowledge is dropped.
func (d *Discoverer) Publish(ctx context.Context, nc *nats.Conn) error {
	if d.cfg.Stored != nil {
		sub, err := nc.Subscribe(bus.SubjectStored, d.stored)
		if err != nil {
			return fmt.Errorf("publish: %w", err)
		}
		defer func() { _ = sub.Unsubscribe() }()
	}
	js, err := jetstream.New(nc,
		jetstream.WithPublishAsyncMaxPending(d.cfg.PublishPending),
		jetstream.WithPublishAsyncTimeout(publishTimeout),
		jetstream.WithPublishAsyncAckHandler(d.acked),
		jetstream.WithPublishAsyncErrHandler(d.failed),
	)
	if err != nil {
		return fmt.Errorf("publish: %w", err)
	}
	go every(ctx, d.cfg.Report, func() { d.log("discovery") })
	for {
		select {
		case m := <-d.offers:
			d.publish(js, m)
		case <-ctx.Done():
			d.flush(js)
			d.log("discovery stopped")
			return ctx.Err()
		}
	}
}

// flush publishes what is queued and waits at most flushTimeout for every
// acknowledgement.
func (d *Discoverer) flush(js jetstream.JetStream) {
	for len(d.offers) > 0 {
		d.publish(js, <-d.offers)
	}
	select {
	case <-js.PublishAsyncComplete():
	case <-time.After(flushTimeout):
	}
}

// stored hands Stored each hash a message on bus.SubjectStored names.
func (d *Discoverer) stored(m *nats.Msg) {
	for b := m.Data; len(b) >= len(Hash{}); b = b[len(Hash{}):] {
		d.cfg.Stored(Hash(b))
	}
}

// acked counts a sighting the server acknowledged: new, or known when a
// duplicate within the window.
func (d *Discoverer) acked(_ jetstream.JetStream, _ *nats.Msg, ack *jetstream.PubAck) {
	if ack.Duplicate {
		d.stats.known.Add(1)
	} else {
		d.stats.new.Add(1)
	}
}

// failed counts a sighting the server did not acknowledge.
func (d *Discoverer) failed(_ jetstream.JetStream, _ *nats.Msg, err error) {
	d.stats.failed.Add(1)
	d.cfg.Log.Debug("publish", "err", err)
}

// publish publishes the sighting without waiting for its acknowledgement.
func (d *Discoverer) publish(js jetstream.JetStream, m *sniffarrv1.SightRequest) {
	discovery := store.Sample
	if m.GetSource() == sniffarrv1.Source_SOURCE_ANNOUNCE {
		discovery = store.Announce
	}
	msg, _ := bus.SightingMsg(Hash(m.GetInfohash()), discovery)
	if _, err := js.PublishMsgAsync(msg); err != nil {
		d.stats.failed.Add(1)
		d.cfg.Log.Debug("publish", "err", err)
	}
}
