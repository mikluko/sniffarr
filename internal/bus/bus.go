// Package bus declares the pipeline's NATS JetStream topology: its streams,
// their literal subjects and their durable consumers. No subject carries an
// infohash or another per-entity token; that travels in the payload.
package bus

import (
	"context"
	"encoding/hex"
	"fmt"
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

// Subjects of the fetch and probe orders, one per priority.
const (
	SubjectFetchP0 = "sniffarr.orders.fetch.p0"
	SubjectFetchP1 = "sniffarr.orders.fetch.p1"
	SubjectFetchP2 = "sniffarr.orders.fetch.p2"
	SubjectFetchP3 = "sniffarr.orders.fetch.p3"
	SubjectProbeP0 = "sniffarr.orders.probe.p0"
	SubjectProbeP1 = "sniffarr.orders.probe.p1"
	SubjectProbeP2 = "sniffarr.orders.probe.p2"
	SubjectProbeP3 = "sniffarr.orders.probe.p3"
)

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

// Streams returns the configurations of every stream. The order and result
// streams are work queues without limits, so nothing is dropped unconsumed.
func Streams() []jetstream.StreamConfig {
	queue := func(name string, subjects ...string) jetstream.StreamConfig {
		return jetstream.StreamConfig{Name: name, Subjects: subjects, Retention: jetstream.WorkQueuePolicy, Storage: jetstream.FileStorage}
	}
	sightings := queue(StreamSightings, SubjectSightAnnounce, SubjectSightSample)
	sightings.Duplicates, sightings.MaxAge = SightingsDuplicates, SightingsMaxAge
	return []jetstream.StreamConfig{
		sightings,
		queue(StreamFetchOrders, FetchOrders[:]...),
		queue(StreamProbeOrders, ProbeOrders[:]...),
		queue(StreamFetchResults, SubjectFetchResults),
		queue(StreamProbeResults, SubjectProbeResults),
	}
}

// Consumers returns the configurations of the sightings and order consumers
// by stream.
func Consumers() map[string][]jetstream.ConsumerConfig {
	orders := func(subjects [Priorities]string) []jetstream.ConsumerConfig {
		cs := make([]jetstream.ConsumerConfig, Priorities)
		for p, subject := range subjects {
			cs[p] = jetstream.ConsumerConfig{Durable: OrderConsumers[p], FilterSubject: subject, AckPolicy: jetstream.AckExplicitPolicy}
		}
		return cs
	}
	return map[string][]jetstream.ConsumerConfig{
		StreamSightings: {
			{Durable: ConsumerAnnounce, FilterSubject: SubjectSightAnnounce, AckPolicy: jetstream.AckExplicitPolicy},
			{Durable: ConsumerSample, FilterSubject: SubjectSightSample, AckPolicy: jetstream.AckExplicitPolicy},
		},
		StreamFetchOrders: orders(FetchOrders),
		StreamProbeOrders: orders(ProbeOrders),
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
