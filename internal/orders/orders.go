// Package orders hands the orders of one of the bus's order streams out to
// the API's workers: it pulls them from the stream's lanes in batches,
// highest priority first, holds each until its result comes back, and
// publishes new orders and scheduled retries. Nothing outside the API
// process touches the stream.
package orders

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"sarak.sh/mikluko/sniffarr/internal/bus"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

const (
	// DefaultBatch is the default Config.Batch.
	DefaultBatch = 32
	// DefaultHeartbeat is the default Config.Heartbeat, a quarter of
	// bus.OrderAckWait.
	DefaultHeartbeat = bus.OrderAckWait / 4
	// DefaultLease is the default Config.Lease.
	DefaultLease = time.Minute
)

// Config is what New needs. Zero fields take the default named in their doc.
type Config struct {
	// Stream is the order stream, bus.StreamFetchOrders or
	// bus.StreamProbeOrders, and Lanes its lanes in the order they are
	// served.
	Stream string
	Lanes  []bus.Lane
	// Batch is how many orders one pull from a lane asks for; DefaultBatch.
	Batch int
	// Lease is how long a holder may keep an order before it goes back to
	// the stream; DefaultLease.
	Lease time.Duration
	// Heartbeat is how often the orders held are marked in progress, well
	// within bus.OrderAckWait; DefaultHeartbeat.
	Heartbeat time.Duration
	Now       func() time.Time
	// Meter records the queue's metrics; the global MeterProvider when nil.
	Meter metric.MeterProvider
}

// Order is one order handed out.
type Order struct {
	Hash store.Hash
	bus.Lane
	// Attempt is how many attempts at the order failed before this one.
	Attempt int
	// Leases is how many times the stream has delivered the order.
	Leases int
	// QueuedAt is when the order was published, or when its retry fired.
	QueuedAt time.Time

	msg   jetstream.Msg
	seq   uint64
	owner int64
	// since is when the order was handed out, beat when it was last
	// delivered or marked in progress.
	since, beat time.Time
}

type key struct {
	kind string
	hash store.Hash
}

type lane struct {
	bus.Lane
	consumer jetstream.Consumer
	// buf holds the orders pulled and not handed out, oldest first.
	buf []*Order
}

// Queue is the hand-out of one order stream. It is safe for concurrent use
// once Start has returned.
type Queue struct {
	cfg   Config
	js    jetstream.JetStream
	lanes []lane

	mu   sync.Mutex
	held map[key]*Order
	// back holds the orders handed back without a result, for a late
	// result within the lease.
	back map[key]*Order
	// pulled is the orders pulled and not acknowledged or handed back.
	pulled int
	wake   notifier
	pace   pace
	m      metrics
}

// New returns the Queue of cfg; Start binds it to a stream.
func New(cfg Config) *Queue {
	if cfg.Batch <= 0 {
		cfg.Batch = DefaultBatch
	}
	if cfg.Lease <= 0 {
		cfg.Lease = DefaultLease
	}
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = DefaultHeartbeat
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Queue{cfg: cfg, held: map[key]*Order{}, back: map[key]*Order{}}
}

// Start binds q to the consumers of its lanes on js and registers its
// metrics.
func (q *Queue) Start(ctx context.Context, js jetstream.JetStream) error {
	q.js = js
	q.lanes = make([]lane, len(q.cfg.Lanes))
	for i, l := range q.cfg.Lanes {
		c, err := js.Consumer(ctx, q.cfg.Stream, l.Consumer)
		if err != nil {
			return fmt.Errorf("orders: consumer %s of %s: %w", l.Consumer, q.cfg.Stream, err)
		}
		q.lanes[i] = lane{Lane: l, consumer: c}
	}
	return q.observe()
}

// Lane returns the lane of the kind at the priority, and false for none.
func (q *Queue) Lane(kind string, priority int) (bus.Lane, bool) {
	for _, l := range q.cfg.Lanes {
		if l.Kind == kind && l.Priority == priority {
			return l, true
		}
	}
	return bus.Lane{}, false
}

// Wait returns a channel closed when orders are published or handed back.
func (q *Queue) Wait() <-chan struct{} {
	return q.wake.wait()
}

// Take hands up to n orders to owner, from the lanes up to maxPriority in
// order, each lane's oldest first, without waiting for any; it returns none
// when there are none. A second order of the kind and hash of one held is
// acknowledged and dropped.
func (q *Queue) Take(owner int64, n int, maxPriority int) ([]*Order, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.cfg.Now()
	var out []*Order
	for i := range q.lanes {
		if q.lanes[i].Priority > maxPriority || len(out) == n {
			break
		}
		got, err := q.takeLane(&q.lanes[i], owner, n-len(out), now)
		out = append(out, got...)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// takeLane hands up to n orders of l to owner at now, pulling whenever l's
// buffer is empty, until a pull brings none.
func (q *Queue) takeLane(l *lane, owner int64, n int, now time.Time) ([]*Order, error) {
	var out []*Order
	for len(out) < n {
		if len(l.buf) == 0 {
			room := bus.OrderInFlight - q.pulled
			if room <= 0 {
				return out, nil
			}
			if err := q.pull(l, min(q.cfg.Batch, room), now); err != nil || len(l.buf) == 0 {
				return out, err
			}
		}
		o := l.buf[0]
		l.buf = l.buf[1:]
		if q.hold(o, owner, now) {
			out = append(out, o)
		}
	}
	return out, nil
}

// hold has owner hold o from now and reports true; for an order of the kind
// and hash of one held, it reports false, after taking a redelivery of the
// held order's message in its place or acknowledging a second order.
func (q *Queue) hold(o *Order, owner int64, now time.Time) bool {
	k := key{o.Kind, o.Hash}
	if h, ok := q.held[k]; ok {
		q.pulled--
		if h.seq == o.seq {
			h.msg, h.Leases, h.beat = o.msg, o.Leases, o.beat
		} else {
			_ = o.msg.Ack()
		}
		return false
	}
	o.owner, o.since = owner, now
	q.held[k] = o
	return true
}

// pull appends up to n orders of l to its buffer, terminating a message that
// is no order.
func (q *Queue) pull(l *lane, n int, now time.Time) error {
	batch, err := l.consumer.FetchNoWait(n)
	if err != nil {
		return fmt.Errorf("orders: pull %s: %w", l.Consumer, err)
	}
	for m := range batch.Messages() {
		meta, err := m.Metadata()
		if err != nil || len(m.Data()) != len(store.Hash{}) {
			_ = m.Term()
			continue
		}
		l.buf = append(l.buf, &Order{Hash: store.Hash(m.Data()), Lane: l.Lane, Attempt: bus.Attempt(m.Headers()), Leases: int(meta.NumDelivered),
			QueuedAt: meta.Timestamp, msg: m, seq: meta.Sequence.Stream, beat: now})
		q.pulled++
	}
	if err := batch.Error(); err != nil && !errors.Is(err, nats.ErrTimeout) && !errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("orders: pull %s: %w", l.Consumer, err)
	}
	return nil
}

// Done takes the order of the kind for h out of those held, whoever holds
// it, or else out of those handed back without a result within a lease
// before, and returns it; nil for neither.
func (q *Queue) Done(kind string, h store.Hash) *Order {
	q.mu.Lock()
	defer q.mu.Unlock()
	k := key{kind, h}
	if o, ok := q.held[k]; ok {
		delete(q.held, k)
		return o
	}
	if o, ok := q.back[k]; ok {
		delete(q.back, k)
		return o
	}
	return nil
}

// DoneBy takes the order of the kind for h out of those owner holds and
// returns it, or nil when owner holds none.
func (q *Queue) DoneBy(kind string, h store.Hash, owner int64) *Order {
	q.mu.Lock()
	defer q.mu.Unlock()
	k := key{kind, h}
	o, ok := q.held[k]
	if !ok || o.owner != owner {
		return nil
	}
	delete(q.held, k)
	return o
}

// Ack acknowledges o, taken by Done or DoneBy; an order handed back already
// is not acknowledged.
func (q *Queue) Ack(ctx context.Context, o *Order) error {
	if o.msg == nil {
		return nil
	}
	q.mu.Lock()
	q.pulled--
	q.mu.Unlock()
	return o.msg.DoubleAck(ctx)
}

// Nak hands o, taken by Done or DoneBy, back to the stream; an order handed
// back already is left as it is.
func (q *Queue) Nak(o *Order) {
	if o.msg == nil {
		return
	}
	q.mu.Lock()
	q.pulled--
	q.mu.Unlock()
	_ = o.msg.Nak()
	q.wake.notify()
}

// Lease returns how long a holder may keep an order.
func (q *Queue) Lease() time.Duration {
	return q.cfg.Lease
}

// Retry acknowledges o, taken by Done or DoneBy, and schedules its next
// attempt on to at at.
func (q *Queue) Retry(ctx context.Context, o *Order, to bus.Lane, at time.Time) error {
	if err := q.Ack(ctx, o); err != nil {
		return err
	}
	_, err := q.Publish(ctx, to, []Item{{Hash: o.Hash, Attempt: o.Attempt + 1, At: at,
		ID: "retry:" + hex.EncodeToString(o.Hash[:]) + ":" + strconv.Itoa(o.Attempt+1)}})
	return err
}

// Release hands every order owner holds back to the stream.
func (q *Queue) Release(owner int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.cfg.Now()
	n := 0
	for k, o := range q.held {
		if o.owner == owner {
			q.handBack(k, o, now)
			n++
		}
	}
	if n > 0 {
		q.wake.notify()
	}
}

// handBack naks o, held under k, at now, and keeps it in back for a late
// result.
func (q *Queue) handBack(k key, o *Order, now time.Time) {
	_ = o.msg.Nak()
	q.pulled--
	delete(q.held, k)
	o.msg, o.beat = nil, now
	q.back[k] = o
}

// Finished counts an order of the kind as finished at the time, for the pace
// Levels reads.
func (q *Queue) Finished(kind string, at time.Time) {
	q.pace.add(kind, at)
}

// Held returns how many orders of the kind are handed out.
func (q *Queue) Held(kind string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for k := range q.held {
		if k.kind == kind {
			n++
		}
	}
	return n
}

// Item is an order to publish.
type Item struct {
	Hash    store.Hash
	Attempt int
	// ID is the order's Nats-Msg-Id; none when "".
	ID string
	// At, when after now, has the order published then, by a schedule on
	// its hash's retry subject that replaces any pending one.
	At time.Time
}

// Publish publishes the items on the lane, waits for the stream to take
// them, and returns how many it took that were no duplicates within
// bus.OrdersDuplicates.
func (q *Queue) Publish(ctx context.Context, l bus.Lane, items []Item) (int, error) {
	now := q.cfg.Now()
	acks := make([]jetstream.PubAckFuture, len(items))
	for i, it := range items {
		m := bus.OrderMsg(l, it.Hash, it.Attempt, it.ID)
		if it.At.After(now) {
			m.Subject = bus.RetrySubject(q.cfg.Stream, it.Hash)
			m.Header.Set(jetstream.ScheduleHeader, "@at "+it.At.UTC().Format(time.RFC3339))
			m.Header.Set(jetstream.ScheduleTargetHeader, l.Subject)
		}
		var err error
		if acks[i], err = q.js.PublishMsgAsync(m); err != nil {
			return 0, fmt.Errorf("orders: publish: %w", err)
		}
	}
	took := 0
	for _, a := range acks {
		select {
		case ack := <-a.Ok():
			if !ack.Duplicate {
				took++
			}
		case err := <-a.Err():
			return took, fmt.Errorf("orders: publish: %w", err)
		case <-ctx.Done():
			return took, ctx.Err()
		}
	}
	if took > 0 {
		q.wake.notify()
	}
	return took, nil
}

// Run marks the orders held, and those pulled and not handed out, in
// progress every Heartbeat, and hands back those held longer than the lease,
// until ctx ends; then it hands every one back.
func (q *Queue) Run(ctx context.Context) error {
	t := time.NewTicker(q.cfg.Heartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			q.stop()
			return ctx.Err()
		case <-t.C:
			q.beat(ctx)
		}
	}
}

// beat marks in progress the orders not delivered or marked within the last
// Heartbeat, hands back those held beyond the lease, and forgets those
// handed back a lease ago.
func (q *Queue) beat(ctx context.Context) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.cfg.Now()
	expired := map[string]int64{}
	for k, o := range q.held {
		if now.Sub(o.since) >= q.cfg.Lease {
			q.handBack(k, o, now)
			expired[k.kind]++
			continue
		}
		q.progress(o, now)
	}
	for _, l := range q.lanes {
		for _, o := range l.buf {
			q.progress(o, now)
		}
	}
	for k, o := range q.back {
		if now.Sub(o.beat) >= q.cfg.Lease {
			delete(q.back, k)
		}
	}
	for kind, n := range expired {
		q.m.expiries.Add(ctx, n, kindAttr(kind))
	}
	if len(expired) > 0 {
		q.wake.notify()
	}
}

func (q *Queue) progress(o *Order, now time.Time) {
	if now.Sub(o.beat) >= q.cfg.Heartbeat {
		_ = o.msg.InProgress()
		o.beat = now
	}
}

// stop hands every order held or pulled back to the stream.
func (q *Queue) stop() {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.cfg.Now()
	for k, o := range q.held {
		q.handBack(k, o, now)
	}
	for i := range q.lanes {
		for _, o := range q.lanes[i].buf {
			_ = o.msg.Nak()
			q.pulled--
		}
		q.lanes[i].buf = nil
	}
}

// Levels returns the orders queued, delivered or not, of each kind of q's
// lanes by priority, from store.PriorityRequest to store.PrioritySample,
// with the kind's pace at now.
func (q *Queue) Levels(ctx context.Context, now time.Time) (map[string]store.Queue, error) {
	counts, err := q.counts(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]store.Queue{}
	for kind, c := range counts {
		out[kind] = store.Levels(c, q.pace.perSecond(now, kind))
	}
	return out, nil
}

// counts returns the orders of each kind by priority, from the consumers'
// pending and unacknowledged messages.
func (q *Queue) counts(ctx context.Context) (map[string][]int64, error) {
	out := map[string][]int64{}
	for _, l := range q.lanes {
		info, err := l.consumer.Info(ctx)
		if err != nil {
			return nil, fmt.Errorf("orders: consumer %s: %w", l.Consumer, err)
		}
		if out[l.Kind] == nil {
			out[l.Kind] = make([]int64, bus.Priorities)
		}
		out[l.Kind][l.Priority] += int64(info.NumPending) + int64(info.NumAckPending)
	}
	return out, nil
}

type metrics struct {
	expiries metric.Int64Counter
}

func kindAttr(kind string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("sniffarr.task.kind", kind))
}

// observe registers the queue's metrics.
func (q *Queue) observe() error {
	mp := q.cfg.Meter
	if mp == nil {
		mp = otel.GetMeterProvider()
	}
	m := mp.Meter("sarak.sh/mikluko/sniffarr/internal/orders")
	var err error
	if q.m.expiries, err = m.Int64Counter("sniffarr.queue.lease_expiries", metric.WithUnit("{lease}"),
		metric.WithDescription("Orders handed back for being held beyond the lease, by kind.")); err != nil {
		return err
	}
	depth, err := m.Int64ObservableGauge("sniffarr.queue.depth", metric.WithUnit("{order}"),
		metric.WithDescription("Orders queued, delivered or not, by kind and priority."))
	if err != nil {
		return err
	}
	leases, err := m.Int64ObservableGauge("sniffarr.queue.leases", metric.WithUnit("{lease}"),
		metric.WithDescription("Orders handed out to workers, by kind."))
	if err != nil {
		return err
	}
	kinds := map[string]bool{}
	for _, l := range q.cfg.Lanes {
		kinds[l.Kind] = true
	}
	_, err = m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		counts, err := q.counts(ctx)
		if err != nil {
			return err
		}
		for kind, c := range counts {
			for p, n := range c {
				o.ObserveInt64(depth, n, metric.WithAttributes(attribute.String("sniffarr.task.kind", kind), attribute.Int("sniffarr.priority", p)))
			}
		}
		for kind := range kinds {
			o.ObserveInt64(leases, int64(q.Held(kind)), kindAttr(kind))
		}
		return nil
	}, depth, leases)
	return err
}

// notifier wakes every waiter at once.
type notifier struct {
	mu sync.Mutex
	ch chan struct{}
}

// wait returns a channel closed by the next notify.
func (n *notifier) wait() <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.ch == nil {
		n.ch = make(chan struct{})
	}
	return n.ch
}

func (n *notifier) notify() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.ch != nil {
		close(n.ch)
		n.ch = nil
	}
}
