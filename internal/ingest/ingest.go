// Package ingest implements sniffarr.v1.IngestService, the work queue between
// sniffers: it queues sighted infohashes, leases them to fetchers, and passes
// the metadata they return through the rules into the store.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/metric"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/audio"
	"github.com/mikluko/sniffarr/internal/names"
	"github.com/mikluko/sniffarr/internal/rules"
	"github.com/mikluko/sniffarr/internal/store"
)

const (
	DefaultLease    = time.Minute
	DefaultAttempts = 3
	DefaultBackoff  = 10 * time.Minute
	DefaultPoll     = time.Second
)

// sightBatch is how many sightings Sight stores in one transaction.
const sightBatch = 500

// maxCredit caps the credit one Fetch stream may hold.
const maxCredit = 10000

// Service implements sniffarrv1connect.IngestServiceHandler. Zero fields take
// the default named in their doc.
type Service struct {
	Store *store.Store
	Queue *store.Queue
	Log   *slog.Logger
	// Lease is how long a fetcher holds a task before it returns to the
	// queue; DefaultLease.
	Lease time.Duration
	// Attempts is how many timed-out fetches drop a hash from the queue;
	// DefaultAttempts. A dropped hash is not rejected; it is dead for the
	// queue's DeadFor. A fetch that finds no peers drops its hash at once.
	Attempts int
	// Backoff is the wait before a timed-out hash is leased again, doubled
	// for each timeout before; DefaultBackoff.
	Backoff time.Duration
	// Poll is how often a Fetch stream with credit looks for free tasks
	// besides when hashes are queued or released; DefaultPoll.
	Poll time.Duration
	// Scripts are the writing systems a torrent's name may have letters of;
	// rules.DefaultScripts when nil.
	Scripts rules.Scripts
	// Languages get an audio entry in every stored torrent.
	Languages []audio.Language
	Now       func() time.Time
	// Meter records the service's metrics; the global MeterProvider when
	// nil.
	Meter metric.MeterProvider

	owners      atomic.Int64
	wake        notifier
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
		queued, known, stored, err := s.sight(ctx, batch)
		if err != nil {
			return err
		}
		res.New += int64(queued)
		res.Known += int64(known)
		res.Stored = append(res.Stored, stored...)
		s.metrics().sightings.Add(ctx, int64(queued), resultNew)
		s.metrics().sightings.Add(ctx, int64(known), resultKnown)
		if queued > 0 {
			s.wake.notify()
		}
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

// sight enqueues the sightings of hashes neither stored nor rejected, and
// returns how many it queued, how many were known, and, per sighting in
// order, whether its hash is a stored torrent.
func (s *Service) sight(ctx context.Context, batch []store.Sighting) (queued, known int, stored []bool, err error) {
	hashes := make([]store.Hash, len(batch))
	for i, g := range batch {
		hashes[i] = store.Hash(g.Hash)
	}
	isStored, rejected, err := s.Store.Known(ctx, hashes, s.now())
	if err != nil {
		return 0, 0, nil, s.internal(ctx, "known", err)
	}
	stored = make([]bool, len(batch))
	fresh := batch[:0:0]
	for i, g := range batch {
		h := store.Hash(g.Hash)
		stored[i] = isStored[h]
		if isStored[h] || rejected[h] {
			known++
		} else {
			fresh = append(fresh, g)
		}
	}
	queued, queuedBefore, err := s.Queue.Enqueue(ctx, fresh, s.now())
	if err != nil {
		return 0, 0, nil, s.internal(ctx, "enqueue", err)
	}
	return queued, known + queuedBefore, stored, nil
}

// store passes fetched metadata through the rules, stores or rejects it, and
// completes its task. Metadata for a hash that is not queued is dropped.
func (s *Service) store(ctx context.Context, h store.Hash, t *sniffarrv1.Torrent) error {
	if t.GetName() == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("torrent %x has no name", h))
	}
	d, err := s.Queue.Discovery(ctx, store.Hash(h))
	if errors.Is(err, store.ErrNotQueued) {
		return nil
	}
	if err != nil {
		return s.internal(ctx, "discovery", err)
	}
	if err := s.keep(ctx, h, d, t); err != nil {
		return err
	}
	if err := s.Queue.Complete(ctx, store.Hash(h)); err != nil {
		return s.internal(ctx, "complete", err)
	}
	return nil
}

// keep stores the torrent, or rejects it when the rules say so.
func (s *Service) keep(ctx context.Context, h store.Hash, d string, t *sniffarrv1.Torrent) error {
	files := make([]rules.File, len(t.GetFiles()))
	for i, f := range t.GetFiles() {
		files[i] = rules.File{Path: f.GetPath(), Size: f.GetSize()}
	}
	if len(files) == 0 {
		files = []rules.File{{Path: t.GetName(), Size: t.GetSize()}}
	}
	now := s.now()
	if reason := rules.Check(t.GetName(), t.GetSize(), files, s.Scripts); reason != rules.Accept {
		rejected, err := s.Store.Reject(ctx, h, reason, now)
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
		Discovery: d,
		SeenAt:    now,
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

// kind sets the torrent's title key, year, season, episode and quality, and
// its kind when the name tells it or a stored torrent of the same title key
// has one.
func (s *Service) kind(ctx context.Context, t *store.Torrent) error {
	p := names.Parse(t.Name)
	t.TitleKey, t.Year, t.Season, t.Episode, t.Quality = p.Key, p.Year, p.Season, p.Episode, p.Quality
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
// files; a marker decides the probability as 1 and carries its mark.
func audioEntries(langs []audio.Language, name string, files []rules.File) []store.Audio {
	out := make([]store.Audio, len(langs))
	for i, l := range langs {
		out[i] = store.Audio{Language: l.Tag, Marker: l.Marker(name, files), ScriptHint: l.ScriptHint(name, files)}
		if out[i].Marker {
			out[i].Mark, out[i].Probability, out[i].Layer = l.Mark(name, files), new(1.0), store.LayerRules
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
