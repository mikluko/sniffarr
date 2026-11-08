// Package ingest implements sniffarr.v1.IngestService, the work queue between
// sniffers: it queues sighted infohashes as fetch orders, hands the orders to
// fetchers, and passes the metadata they return through the rules into the
// store.
package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel/metric"

	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/audio"
	"sarak.sh/mikluko/sniffarr/internal/bus"
	"sarak.sh/mikluko/sniffarr/internal/names"
	"sarak.sh/mikluko/sniffarr/internal/orders"
	"sarak.sh/mikluko/sniffarr/internal/rules"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

const (
	DefaultAttempts = 3
	DefaultBackoff  = 10 * time.Minute
	DefaultPoll     = time.Second
)

// sightBatch is how many sightings Sight checks and publishes at once.
const sightBatch = 500

// maxCredit caps the credit one Fetch stream may hold.
const maxCredit = 10000

// Service implements sniffarrv1connect.IngestServiceHandler. Zero fields take
// the default named in their doc.
type Service struct {
	Store *store.Store
	Tasks *store.Tasks
	// Fetches hands out the fetch orders and the peer lookups.
	Fetches *orders.Queue
	Log     *slog.Logger
	// Attempts is how many timed-out fetches drop a hash; DefaultAttempts.
	// A dropped hash is not rejected; it is dead for DeadFor.
	Attempts int
	// Backoff is the wait before a timed-out hash is fetched again, doubled
	// for each timeout before; DefaultBackoff.
	Backoff time.Duration
	// DeadFor is how long a hash dropped after its last attempt is kept
	// out, store.DefaultDeadFor; NoPeersFor that of one whose fetch found no
	// peers, store.DefaultNoPeersFor.
	DeadFor, NoPeersFor time.Duration
	// Poll is how often a Fetch stream with credit looks for orders besides
	// when orders are published or handed back; DefaultPoll.
	Poll time.Duration
	// JetStream is the connection Sight publishes sightings on.
	JetStream jetstream.JetStream
	// IntakeBatch is how many sightings Intake takes at once;
	// DefaultIntakeBatch.
	IntakeBatch int
	// IntakeWait is how long Intake waits for an announced sighting once
	// none is left of either discovery; DefaultIntakeWait.
	IntakeWait time.Duration
	// Scripts are the writing systems a torrent's name may have letters of;
	// rules.DefaultScripts when nil.
	Scripts rules.Scripts
	// Languages get an audio entry in every stored torrent.
	Languages []audio.Language
	Now       func() time.Time
	// Probe tunes the probe tasks.
	Probe ProbeConfig
	// Probes has ProbeTask lease probe tasks; without it ProbeTask answers
	// none.
	Probes bool
	// Meter records the service's metrics; the global MeterProvider when
	// nil.
	Meter metric.MeterProvider

	owners      atomic.Int64
	metricsOnce sync.Once
	m           *metrics
}

func (s *Service) metrics() *metrics {
	s.metricsOnce.Do(func() { s.m = newMetrics(s.Meter) })
	return s.m
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func or[T int | time.Duration](v, d T) T {
	if v > 0 {
		return v
	}
	return d
}

func (s *Service) Sight(ctx context.Context, stream *connect.ClientStream[sniffarrv1.SightRequest]) (*connect.Response[sniffarrv1.SightResponse], error) {
	var res sniffarrv1.SightResponse
	batch := make([]store.Sighting, 0, sightBatch)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		known, stored, err := s.sight(ctx, batch)
		if err != nil {
			return err
		}
		res.New += int64(len(batch) - known)
		res.Known += int64(known)
		res.Stored = append(res.Stored, stored...)
		batch = batch[:0]
		return nil
	}
	for stream.Receive() {
		m := stream.Msg()
		h, err := hash(m.GetInfohash())
		if err != nil {
			return nil, err
		}
		d, err := discovery(m.GetSource())
		if err != nil {
			return nil, err
		}
		batch = append(batch, store.Sighting{Hash: store.Hash(h), Discovery: d})
		if len(batch) == sightBatch {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return connect.NewResponse(&res), nil
}

// sight publishes the sightings to the sightings stream and waits for the
// server's acknowledgements; it returns how many of them the stream drops as
// duplicates or the intake drops, and, per sighting in order, whether its
// hash is a stored torrent. It writes nothing to the store, and counts the
// duplicates as known sightings, which the intake never sees.
func (s *Service) sight(ctx context.Context, batch []store.Sighting) (known int, stored []bool, err error) {
	hashes := make([]store.Hash, len(batch))
	for i, g := range batch {
		hashes[i] = g.Hash
	}
	held, err := s.Store.Screen(ctx, hashes, s.now())
	if err != nil {
		return 0, nil, s.internal(ctx, "screen", err)
	}
	acks := make([]jetstream.PubAckFuture, len(batch))
	for i, g := range batch {
		m, _ := bus.SightingMsg(g.Hash, g.Discovery)
		if acks[i], err = s.JetStream.PublishMsgAsync(m); err != nil {
			return 0, nil, s.internal(ctx, "publish", err)
		}
	}
	stored = make([]bool, len(batch))
	duplicates := 0
	for i, g := range batch {
		select {
		case ack := <-acks[i].Ok():
			switch {
			case ack.Duplicate:
				duplicates++
				known++
			case drops(held[g.Hash], g.Discovery):
				known++
			}
		case err := <-acks[i].Err():
			return 0, nil, s.internal(ctx, "publish", err)
		case <-ctx.Done():
			return 0, nil, connect.NewError(connect.CodeCanceled, ctx.Err())
		}
		stored[i] = held[g.Hash].Stored
	}
	s.metrics().sightings.Add(ctx, int64(duplicates), resultKnown)
	return known, stored, nil
}

// store passes fetched metadata through the rules, stores or rejects it, and
// acknowledges its order. Metadata for a hash with no order held, or handed
// back within the lease, is dropped.
func (s *Service) store(ctx context.Context, h store.Hash, t *sniffarrv1.Torrent) error {
	if t.GetName() == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("torrent %x has no name", h))
	}
	o := s.Fetches.Done(store.TaskFetch, h)
	if o == nil {
		return nil
	}
	s.Fetches.Finished(store.TaskFetch, s.now())
	if err := s.keep(ctx, o, t); err != nil {
		s.Fetches.Nak(o)
		return err
	}
	return s.ack(ctx, o)
}

// keep stores the torrent of the order o, or rejects it when the rules say
// so.
func (s *Service) keep(ctx context.Context, o *orders.Order, t *sniffarrv1.Torrent) error {
	h := o.Hash
	files := make([]rules.File, len(t.GetFiles()))
	for i, f := range t.GetFiles() {
		files[i] = rules.File{Path: f.GetPath(), Size: f.GetSize()}
	}
	if len(files) == 0 {
		files = []rules.File{{Path: t.GetName(), Size: t.GetSize()}}
	}
	now := s.now()
	if reason := rules.Check(t.GetName(), t.GetSize(), files, s.Scripts); reason != rules.Accept {
		rejected, err := s.Store.Reject(ctx, h, reason, t.GetName(), t.GetSize(), now)
		if err != nil {
			return s.internal(ctx, "reject", err)
		}
		if rejected {
			s.metrics().rejections.Add(ctx, 1, reasonAttr(string(reason)))
		}
		return nil
	}
	tr := store.Torrent{
		Hash:      h,
		Name:      t.GetName(),
		Size:      t.GetSize(),
		Files:     files,
		Discovery: store.DiscoveryOf(o.Priority),
		SeenAt:    now,
		QueuedAt:  o.QueuedAt,
		Leases:    o.Leases,
		Audio:     audioEntries(s.Languages, t.GetName(), files),
	}
	if err := s.kind(ctx, &tr); err != nil {
		return err
	}
	stored, copied, err := s.Store.Insert(ctx, tr)
	if err != nil {
		return s.internal(ctx, "insert", err)
	}
	if stored {
		s.metrics().stored.Add(ctx, 1)
		if tr.Kind != "" {
			s.metrics().classifications.Add(ctx, 1, layerAttrs(tr.KindLayer, tr.Kind))
			s.metrics().classifications.Add(ctx, int64(copied), layerAttrs(store.LayerReuse, tr.Kind))
		}
	}
	return nil
}

// kind sets the torrent's title key, year, season, episode, quality and source, and
// its kind when the name tells it or a stored torrent of the same title key
// has one.
func (s *Service) kind(ctx context.Context, t *store.Torrent) error {
	p := names.Parse(t.Name)
	t.TitleKey, t.Year, t.Season, t.Episode, t.Quality = p.Key, p.Year, p.Season, p.Episode, p.Quality
	t.Source = names.Source(t.Name)
	if p.Kind != "" {
		t.Kind, t.KindLayer = p.Kind, store.LayerRules
		return nil
	}
	if p.Key == "" {
		return nil
	}
	k, err := s.Store.KindByKey(ctx, p.Key)
	if err != nil {
		return s.internal(ctx, "kind by key", err)
	}
	if k != "" {
		t.Kind, t.KindLayer = k, store.LayerReuse
	}
	return nil
}

// audioEntries returns an entry per language for a torrent of the name and
// files; a marker decides the probability as 1 and carries its mark and
// studios.
func audioEntries(langs []audio.Language, name string, files []rules.File) []store.Audio {
	out := make([]store.Audio, len(langs))
	for i, l := range langs {
		out[i] = store.Audio{Language: l.Tag, Marker: l.Marker(name, files), ScriptHint: l.ScriptHint(name, files)}
		if out[i].Marker {
			out[i].Mark, out[i].Studios, out[i].Probability, out[i].Layer = l.Mark(name, files), l.StudiosIn(name, files), new(1.0), store.LayerRules
		}
	}
	return out
}

func discovery(src sniffarrv1.Source) (string, error) {
	switch src {
	case sniffarrv1.Source_SOURCE_SAMPLE:
		return store.Sample, nil
	case sniffarrv1.Source_SOURCE_ANNOUNCE:
		return store.Announce, nil
	}
	return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("source %v", src))
}

func hash(b []byte) (store.Hash, error) {
	if len(b) != len(store.Hash{}) {
		return store.Hash{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("infohash of %d bytes, want 20", len(b)))
	}
	return store.Hash(b), nil
}

func (s *Service) internal(ctx context.Context, op string, err error) error {
	s.Log.ErrorContext(ctx, op, "err", err)
	return connect.NewError(connect.CodeInternal, fmt.Errorf("%s failed", op))
}
