// Package bus declares the pipeline's NATS JetStream topology: its streams,
// their subjects and their durable consumers. No subject a consumer reads
// carries an infohash or another per-entity token; that travels in the
// payload. Only the retry subjects, which no consumer reads, carry one.
package bus

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"sarak.sh/mikluko/sniffarr/internal/store"
)

// Subjects of the sightings, one per discovery, and of the fetch and probe
// results.
const (
	SubjectSightAnnounce = "sniffarr.sightings.announce"
	SubjectSightSample   = "sniffarr.sightings.sample"
	SubjectFetchResults  = "sniffarr.results.fetch"
	SubjectProbeResults  = "sniffarr.results.probe"
)

// Durable consumers of the sightings stream the API's intake takes from,
// one per discovery subject.
const (
	ConsumerAnnounce = "announce"
	ConsumerSample   = "sample"
)

// Subjects of the fetch and probe orders, one per priority, and of the peer
// lookups, at store.PriorityRequest only.
const (
	SubjectFetchP0    = "sniffarr.orders.fetch.p0"
	SubjectFetchP1    = "sniffarr.orders.fetch.p1"
	SubjectFetchP2    = "sniffarr.orders.fetch.p2"
	SubjectFetchP3    = "sniffarr.orders.fetch.p3"
	SubjectFetchPeers = "sniffarr.orders.fetch.peers"
	SubjectProbeP0    = "sniffarr.orders.probe.p0"
	SubjectProbeP1    = "sniffarr.orders.probe.p1"
	SubjectProbeP2    = "sniffarr.orders.probe.p2"
	SubjectProbeP3    = "sniffarr.orders.probe.p3"
)

// Prefixes of the subjects of the scheduled retries of fetch and probe
// orders, each completed by the infohash in hex.
const (
	SubjectFetchRetry = "sniffarr.orders.fetch.retry."
	SubjectProbeRetry = "sniffarr.orders.probe.retry."
)

// HeaderAttempt is the header of an order that counts the failed attempts at
// it before; an order without it has none.
const HeaderAttempt = "Sniffarr-Attempt"

// Priorities is the number of order priorities, store.PriorityRequest to
// store.PrioritySample.
const Priorities = store.PrioritySample + 1

// FetchOrders and ProbeOrders are the order subjects indexed by priority.
var (
	FetchOrders = [Priorities]string{SubjectFetchP0, SubjectFetchP1, SubjectFetchP2, SubjectFetchP3}
	ProbeOrders = [Priorities]string{SubjectProbeP0, SubjectProbeP1, SubjectProbeP2, SubjectProbeP3}
)

// OrderConsumers are the durable consumer names of each order stream indexed
// by priority, each filtered to that priority's subject.
var OrderConsumers = [Priorities]string{"p0", "p1", "p2", "p3"}

// ConsumerPeers is the durable consumer of the peer lookups.
const ConsumerPeers = "peers"

// Lane is one durable consumer of an order stream: the orders of one kind,
// store.TaskFetch, store.TaskPeers or store.TaskProbe, at one priority.
type Lane struct {
	Consumer string
	Subject  string
	Priority int
	Kind     string
}

// FetchLanes and ProbeLanes are the lanes of the fetch and probe order
// streams in the order they are served.
var (
	FetchLanes = []Lane{
		{ConsumerPeers, SubjectFetchPeers, store.PriorityRequest, store.TaskPeers},
		{OrderConsumers[0], SubjectFetchP0, store.PriorityRequest, store.TaskFetch},
		{OrderConsumers[1], SubjectFetchP1, store.PriorityArr, store.TaskFetch},
		{OrderConsumers[2], SubjectFetchP2, store.PriorityAnnounce, store.TaskFetch},
		{OrderConsumers[3], SubjectFetchP3, store.PrioritySample, store.TaskFetch},
	}
	ProbeLanes = []Lane{
		{OrderConsumers[0], SubjectProbeP0, store.PriorityRequest, store.TaskProbe},
		{OrderConsumers[1], SubjectProbeP1, store.PriorityArr, store.TaskProbe},
		{OrderConsumers[2], SubjectProbeP2, store.PriorityAnnounce, store.TaskProbe},
		{OrderConsumers[3], SubjectProbeP3, store.PrioritySample, store.TaskProbe},
	}
)

// Stream names.
const (
	StreamSightings    = "SIGHTINGS"
	StreamFetchOrders  = "FETCH_ORDERS"
	StreamProbeOrders  = "PROBE_ORDERS"
	StreamFetchResults = "FETCH_RESULTS"
	StreamProbeResults = "PROBE_RESULTS"
)

// SightingsDuplicates is the window in which a sighting published again with
// the same Nats-Msg-Id header is dropped as a duplicate.
const SightingsDuplicates = 5 * time.Minute

// SightingsMaxAge is how long a sighting no one consumed is kept; the DHT
// sights a live infohash again.
const SightingsMaxAge = 24 * time.Hour

// OrdersDuplicates is the window in which an order or a retry published
// again with the same Nats-Msg-Id header is dropped as a duplicate.
const OrdersDuplicates = 5 * time.Minute

// OrderAckWait is how long a delivered order neither acknowledged nor marked
// in progress waits before it is delivered again.
const OrderAckWait = 2 * time.Minute

// OrderInFlight is the most orders a consumer of an order stream has
// delivered and not acknowledged; the API holds no more of a stream in all.
const OrderInFlight = 1024

// Streams returns the configurations of every stream. The order and result
// streams are work queues without limits, so nothing is dropped unconsumed;
// the order streams take scheduled messages on their retry subjects.
func Streams() []jetstream.StreamConfig {
	queue := func(name string, subjects ...string) jetstream.StreamConfig {
		return jetstream.StreamConfig{Name: name, Subjects: subjects, Retention: jetstream.WorkQueuePolicy, Storage: jetstream.FileStorage}
	}
	orders := func(name string, lanes []Lane, retry string) jetstream.StreamConfig {
		subjects := make([]string, 0, len(lanes)+1)
		for _, l := range lanes {
			subjects = append(subjects, l.Subject)
		}
		cfg := queue(name, append(subjects, retry+"*")...)
		cfg.Duplicates, cfg.AllowMsgSchedules, cfg.AllowRollup = OrdersDuplicates, true, true
		return cfg
	}
	sightings := queue(StreamSightings, SubjectSightAnnounce, SubjectSightSample)
	sightings.Duplicates, sightings.MaxAge = SightingsDuplicates, SightingsMaxAge
	return []jetstream.StreamConfig{
		sightings,
		orders(StreamFetchOrders, FetchLanes, SubjectFetchRetry),
		orders(StreamProbeOrders, ProbeLanes, SubjectProbeRetry),
		queue(StreamFetchResults, SubjectFetchResults),
		queue(StreamProbeResults, SubjectProbeResults),
	}
}

// Consumers returns the configurations of the sightings and order consumers
// by stream.
func Consumers() map[string][]jetstream.ConsumerConfig {
	orders := func(lanes []Lane) []jetstream.ConsumerConfig {
		cs := make([]jetstream.ConsumerConfig, len(lanes))
		for i, l := range lanes {
			cs[i] = jetstream.ConsumerConfig{Durable: l.Consumer, FilterSubject: l.Subject, AckPolicy: jetstream.AckExplicitPolicy,
				AckWait: OrderAckWait, MaxAckPending: OrderInFlight}
		}
		return cs
	}
	return map[string][]jetstream.ConsumerConfig{
		StreamSightings: {
			{Durable: ConsumerAnnounce, FilterSubject: SubjectSightAnnounce, AckPolicy: jetstream.AckExplicitPolicy},
			{Durable: ConsumerSample, FilterSubject: SubjectSightSample, AckPolicy: jetstream.AckExplicitPolicy},
		},
		StreamFetchOrders: orders(FetchLanes),
		StreamProbeOrders: orders(ProbeLanes),
	}
}

// Ensure creates the streams and consumers that are missing and updates the
// others to their configurations.
func Ensure(ctx context.Context, js jetstream.JetStream) error {
	for _, cfg := range Streams() {
		if _, err := js.CreateOrUpdateStream(ctx, cfg); err != nil {
			return fmt.Errorf("stream %s: %w", cfg.Name, err)
		}
	}
	for stream, cfgs := range Consumers() {
		for _, cfg := range cfgs {
			if _, err := js.CreateOrUpdateConsumer(ctx, stream, cfg); err != nil {
				return fmt.Errorf("consumer %s of %s: %w", cfg.Durable, stream, err)
			}
		}
	}
	return nil
}

// SightingMsg returns the message of a sighting of h by the discovery,
// store.Announce or store.Sample, its Nats-Msg-Id the discovery and the
// infohash in hex, and false for another discovery. A sighting is a
// duplicate only of one by the same discovery, so an announce after a sample
// of the same infohash is kept.
func SightingMsg(h [20]byte, discovery string) (*nats.Msg, bool) {
	var subject string
	switch discovery {
	case store.Announce:
		subject = SubjectSightAnnounce
	case store.Sample:
		subject = SubjectSightSample
	default:
		return nil, false
	}
	m := &nats.Msg{Subject: subject, Header: nats.Header{}, Data: h[:]}
	m.Header.Set(jetstream.MsgIDHeader, discovery+":"+hex.EncodeToString(h[:]))
	return m, true
}

// OrderMsg returns the order of h on the lane's subject after attempt failed
// attempts, with the Nats-Msg-Id id unless id is "".
func OrderMsg(l Lane, h [20]byte, attempt int, id string) *nats.Msg {
	m := &nats.Msg{Subject: l.Subject, Header: nats.Header{}, Data: h[:]}
	if attempt > 0 {
		m.Header.Set(HeaderAttempt, strconv.Itoa(attempt))
	}
	if id != "" {
		m.Header.Set(jetstream.MsgIDHeader, id)
	}
	return m
}

// Attempt returns the failed attempts an order's headers count.
func Attempt(h nats.Header) int {
	n, err := strconv.Atoi(h.Get(HeaderAttempt))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// RetrySubject returns the subject of the scheduled retry of h among the
// orders of the stream, StreamFetchOrders or StreamProbeOrders.
func RetrySubject(stream string, h [20]byte) string {
	prefix := SubjectFetchRetry
	if stream == StreamProbeOrders {
		prefix = SubjectProbeRetry
	}
	return prefix + hex.EncodeToString(h[:])
}
