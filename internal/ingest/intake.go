package ingest

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"sarak.sh/mikluko/sniffarr/internal/bus"
	"sarak.sh/mikluko/sniffarr/internal/orders"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

const (
	// DefaultIntakeBatch is how many sightings the intake takes at once.
	DefaultIntakeBatch = 500
	// DefaultIntakeWait is how long the intake waits for an announced
	// sighting once both discoveries' sightings are drained.
	DefaultIntakeWait = time.Second
)

// intakeBackoff is the wait after the intake fails to take or handle a
// batch.
const intakeBackoff = time.Second

// Intake queues the fetches of the sightings in js's sightings stream until
// ctx ends, a batch at a time, the announced ones before any sampled one.
// A batch it fails to handle is redelivered.
func (s *Service) Intake(ctx context.Context, js jetstream.JetStream) error {
	announce, err := js.Consumer(ctx, bus.StreamSightings, bus.ConsumerAnnounce)
	if err != nil {
		return fmt.Errorf("intake: %w", err)
	}
	sample, err := js.Consumer(ctx, bus.StreamSightings, bus.ConsumerSample)
	if err != nil {
		return fmt.Errorf("intake: %w", err)
	}
	n, wait := or(s.IntakeBatch, DefaultIntakeBatch), or(s.IntakeWait, DefaultIntakeWait)
	for ctx.Err() == nil {
		discovery := store.Announce
		msgs, err := take(announce.FetchNoWait(n))
		if err == nil && len(msgs) == 0 {
			discovery = store.Sample
			msgs, err = take(sample.FetchNoWait(n))
		}
		if err == nil && len(msgs) == 0 {
			discovery = store.Announce
			wctx, cancel := context.WithTimeout(ctx, wait)
			msgs, err = take(announce.Fetch(n, jetstream.FetchContext(wctx)))
			cancel()
		}
		if err == nil && len(msgs) > 0 {
			err = s.intake(ctx, discovery, msgs)
		}
		if err != nil && ctx.Err() == nil {
			s.Log.WarnContext(ctx, "intake", "err", err)
			if !sleep(ctx, intakeBackoff) {
				break
			}
		}
	}
	return ctx.Err()
}

// take returns the messages of a fetch; a fetch that ends at its deadline
// with none is not an error.
func take(batch jetstream.MessageBatch, err error) ([]jetstream.Msg, error) {
	if err != nil {
		return nil, err
	}
	var msgs []jetstream.Msg
	for m := range batch.Messages() {
		msgs = append(msgs, m)
	}
	if err := batch.Error(); err != nil && !errors.Is(err, nats.ErrTimeout) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		return msgs, err
	}
	return msgs, nil
}

// intake handles one batch of sightings by the discovery: it moves the last
// sighting of the stored hashes, queues the fetches of those drops keeps,
// and then acknowledges the batch. A message that is
// no infohash is terminated; on an error the batch is left for redelivery.
func (s *Service) intake(ctx context.Context, discovery string, msgs []jetstream.Msg) error {
	now := s.now()
	hashes := make([]store.Hash, 0, len(msgs))
	valid := msgs[:0:0]
	for _, m := range msgs {
		if len(m.Data()) != len(store.Hash{}) {
			_ = m.Term()
			continue
		}
		hashes = append(hashes, store.Hash(m.Data()))
		valid = append(valid, m)
	}
	held, err := s.Store.Screen(ctx, hashes, now)
	if err != nil {
		return fmt.Errorf("screen: %w", err)
	}
	var stored []store.Hash
	for _, h := range hashes {
		if held[h].Stored {
			stored = append(stored, h)
		}
	}
	if len(stored) > 0 {
		if err := s.Store.Seen(ctx, stored, now); err != nil {
			return fmt.Errorf("seen: %w", err)
		}
	}
	queued, err := s.queue(ctx, fresh(hashes, discovery, held), now)
	if err != nil {
		return err
	}
	s.metrics().sightings.Add(ctx, int64(queued), resultNew)
	s.metrics().sightings.Add(ctx, int64(len(hashes)-queued), resultKnown)
	for _, m := range valid {
		if err := m.Ack(); err != nil {
			return fmt.Errorf("ack: %w", err)
		}
	}
	return nil
}

// drops reports whether a sighting by the discovery of a hash screened as g
// queues nothing: stored, rejected, or dead and only sampled.
func drops(g store.Screened, discovery string) bool {
	return g.Stored || g.Rejected || g.Dead && discovery == store.Sample
}

// fresh returns the fetches the sightings of hashes by the discovery queue,
// each hash screened in held.
func fresh(hashes []store.Hash, discovery string, held map[store.Hash]store.Screened) []store.Queued {
	out := make([]store.Queued, 0, len(hashes))
	for _, h := range hashes {
		if g := held[h]; !drops(g, discovery) {
			out = append(out, store.Queued{Hash: h, Discovery: discovery, Revived: g.Dead})
		}
	}
	return out
}

// Enqueue queues a fetch of each sighted hash that drops keeps, reviving the
// dead ones, and returns how many it queued.
func (s *Service) Enqueue(ctx context.Context, sightings []store.Sighting) (int, error) {
	now := s.now()
	hashes := make([]store.Hash, len(sightings))
	for i, g := range sightings {
		hashes[i] = g.Hash
	}
	held, err := s.Store.Screen(ctx, hashes, now)
	if err != nil {
		return 0, fmt.Errorf("screen: %w", err)
	}
	var qs []store.Queued
	for _, g := range sightings {
		qs = append(qs, fresh([]store.Hash{g.Hash}, g.Discovery, held)...)
	}
	return s.queue(ctx, qs, now)
}

// queue publishes a fetch order of each of qs at its discovery's priority,
// the Nats-Msg-Id the discovery and the infohash in hex, then records them,
// and returns how many orders the stream took that were no duplicates.
func (s *Service) queue(ctx context.Context, qs []store.Queued, now time.Time) (int, error) {
	byLane := map[bus.Lane][]orders.Item{}
	for _, q := range qs {
		l, ok := s.Fetches.Lane(store.TaskFetch, store.PriorityOf(q.Discovery))
		if !ok {
			return 0, fmt.Errorf("no fetch lane for %s", q.Discovery)
		}
		byLane[l] = append(byLane[l], orders.Item{Hash: q.Hash, ID: q.Discovery + ":" + hex.EncodeToString(q.Hash[:])})
	}
	took := 0
	for l, items := range byLane {
		n, err := s.Fetches.Publish(ctx, l, items)
		if err != nil {
			return 0, fmt.Errorf("publish: %w", err)
		}
		took += n
	}
	if err := s.Store.RecordQueued(ctx, qs, now); err != nil {
		return 0, fmt.Errorf("record queued: %w", err)
	}
	return took, nil
}

// sleep waits d and reports whether ctx was still going at its end.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
