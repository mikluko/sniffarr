package sniff

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
)

// FetcherConfig tunes a Fetcher. Zero fields take the default named in their
// doc.
type FetcherConfig struct {
	Ingest sniffarrv1connect.IngestServiceClient
	// Workers is how many fetches run at once, and so the most tasks the
	// Fetcher holds; DefaultFetchers.
	Workers int
	// Backoff is the wait after a failed stream; DefaultBackoff.
	Backoff time.Duration
	// Report is the interval of progress logs; DefaultReport.
	Report time.Duration
	// Log discards everything when nil.
	Log *slog.Logger
	// Meter records metrics; the global MeterProvider when nil.
	Meter metric.MeterProvider
}

type fetchMetrics struct {
	inFlight metric.Int64UpDownCounter
	outcomes metric.Int64Counter
	duration metric.Float64Histogram
}

func newFetchMetrics(mp metric.MeterProvider) fetchMetrics {
	m := meter(mp)
	inFlight, err := m.Int64UpDownCounter("sniffarr.sniff.fetch.in_flight", metric.WithUnit("{task}"),
		metric.WithDescription("Fetch tasks received and not finished."))
	if err != nil {
		otel.Handle(err)
	}
	duration, err := m.Float64Histogram("sniffarr.sniff.fetch.duration", metric.WithUnit("s"),
		metric.WithDescription("Time to fetch a torrent's metadata, from looking for peers to the outcome."),
		metric.WithExplicitBucketBoundaries(0.5, 1, 2, 4, 8, 12, 16, 20, 25, 30, 60))
	if err != nil {
		otel.Handle(err)
	}
	return fetchMetrics{
		inFlight: inFlight,
		outcomes: counter(m, "sniffarr.sniff.fetch.outcomes", "{task}", "Fetch tasks finished, by outcome."),
		duration: duration,
	}
}

// Fetcher takes tasks from the server's Fetch, gets their metadata from a
// MetadataSource, and returns the results on the same stream. It grants the
// server one unit of credit per idle worker.
type Fetcher struct {
	cfg     FetcherConfig
	source  MetadataSource
	tasks   chan Hash
	results chan *sniffarrv1.Result
	kick    chan struct{}

	mu   sync.Mutex
	busy int // tasks received and not yet finished
	free int // idle workers not yet granted as credit on the current stream

	stats   struct{ tasks, torrents, noPeers, timeouts atomic.Int64 }
	metrics fetchMetrics
}

// NewFetcher returns a Fetcher for cfg that fetches from source.
func NewFetcher(cfg FetcherConfig, source MetadataSource) *Fetcher {
	orDefault(&cfg.Workers, DefaultFetchers)
	orDefault(&cfg.Backoff, DefaultBackoff)
	orDefault(&cfg.Report, DefaultReport)
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	return &Fetcher{
		cfg:     cfg,
		source:  source,
		tasks:   make(chan Hash, cfg.Workers),
		results: make(chan *sniffarrv1.Result, cfg.Workers),
		kick:    make(chan struct{}, 1),
		metrics: newFetchMetrics(cfg.Meter),
	}
}

// Run fetches until ctx is done, reconnecting after a failed stream. Results
// that a failed stream did not carry go on the next one; tasks in flight at
// shutdown are abandoned, and the server leases them again.
func (f *Fetcher) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for range f.cfg.Workers {
		wg.Go(func() { f.work(ctx) })
	}
	go every(ctx, f.cfg.Report, func() { f.log("fetching") })
	var pending *sniffarrv1.Result
	for ctx.Err() == nil {
		var err error
		pending, err = f.stream(ctx, pending)
		if ctx.Err() != nil {
			break
		}
		f.cfg.Log.Warn("fetch stream", "err", err)
		if sleep(ctx, f.cfg.Backoff) != nil {
			break
		}
	}
	wg.Wait()
	f.log("fetching stopped")
	return ctx.Err()
}

// stream runs one Fetch stream until it fails or ctx is done, and returns
// the result it could not send.
func (f *Fetcher) stream(ctx context.Context, pending *sniffarrv1.Result) (*sniffarrv1.Result, error) {
	stream := f.cfg.Ingest.Fetch(ctx)
	defer func() {
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
	}()
	f.mu.Lock()
	f.free = f.cfg.Workers - f.busy
	f.mu.Unlock()
	f.signal()

	errc := make(chan error, 1)
	go func() { errc <- f.receive(ctx, stream) }()
	for {
		if pending != nil {
			if err := stream.Send(&sniffarrv1.FetchRequest{Kind: &sniffarrv1.FetchRequest_Result{Result: pending}}); err != nil {
				return pending, err
			}
			pending = nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-errc:
			return nil, err
		case pending = <-f.results:
		case <-f.kick:
			if r, err := f.flush(stream); err != nil {
				return r, err
			}
			f.mu.Lock()
			n := f.free
			f.free = 0
			f.mu.Unlock()
			if n == 0 {
				continue
			}
			if err := stream.Send(&sniffarrv1.FetchRequest{Kind: &sniffarrv1.FetchRequest_Credit{Credit: &sniffarrv1.Credit{N: int32(n)}}}); err != nil {
				return nil, err
			}
		}
	}
}

// flush sends the results waiting to be sent. A worker queues its result
// before it frees its credit, so flushing before granting credit keeps the
// server from holding more tasks than there are workers.
// It returns the result it could not send.
func (f *Fetcher) flush(stream *connect.BidiStreamForClient[sniffarrv1.FetchRequest, sniffarrv1.FetchResponse]) (*sniffarrv1.Result, error) {
	for {
		select {
		case r := <-f.results:
			if err := stream.Send(&sniffarrv1.FetchRequest{Kind: &sniffarrv1.FetchRequest_Result{Result: r}}); err != nil {
				return r, err
			}
		default:
			return nil, nil
		}
	}
}

// receive passes the stream's tasks to the workers until the stream fails.
func (f *Fetcher) receive(ctx context.Context, stream *connect.BidiStreamForClient[sniffarrv1.FetchRequest, sniffarrv1.FetchResponse]) error {
	for {
		m, err := stream.Receive()
		if err != nil {
			return err
		}
		b := m.GetTask().GetInfohash()
		if len(b) != len(Hash{}) {
			continue
		}
		f.stats.tasks.Add(1)
		f.metrics.inFlight.Add(ctx, 1)
		f.mu.Lock()
		f.busy++
		f.mu.Unlock()
		select {
		case f.tasks <- Hash(b):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (f *Fetcher) work(ctx context.Context) {
	for {
		var h Hash
		select {
		case <-ctx.Done():
			return
		case h = <-f.tasks:
		}
		start := time.Now()
		m, err := f.source.Metadata(ctx, h)
		if ctx.Err() != nil {
			return
		}
		r, outcome := f.result(h, m, err)
		f.metrics.outcomes.Add(ctx, 1, outcomeAttr(outcome))
		f.metrics.duration.Record(ctx, time.Since(start).Seconds(), outcomeAttr(outcome))
		f.metrics.inFlight.Add(ctx, -1)
		select {
		case f.results <- r:
		case <-ctx.Done():
			return
		}
		f.mu.Lock()
		f.busy--
		f.free++
		f.mu.Unlock()
		f.signal()
	}
}

func (f *Fetcher) signal() {
	select {
	case f.kick <- struct{}{}:
	default:
	}
}

// result returns the task's result and the name of its outcome. Invalid UTF-8
// in the name and paths is replaced by U+FFFD, which a proto string requires.
func (f *Fetcher) result(h Hash, m Metadata, err error) (*sniffarrv1.Result, string) {
	r := &sniffarrv1.Result{Infohash: h[:]}
	switch {
	case err == nil:
		f.stats.torrents.Add(1)
		t := &sniffarrv1.Torrent{Name: strings.ToValidUTF8(m.Name, "\uFFFD"), Size: m.Size, Files: make([]*sniffarrv1.File, len(m.Files))}
		for i, file := range m.Files {
			t.Files[i] = &sniffarrv1.File{Path: strings.ToValidUTF8(file.Path, "\uFFFD"), Size: file.Size}
		}
		r.Outcome = &sniffarrv1.Result_Torrent{Torrent: t}
		return r, "torrent"
	case errors.Is(err, ErrNoPeers):
		f.stats.noPeers.Add(1)
		r.Outcome = &sniffarrv1.Result_Failure{Failure: sniffarrv1.Failure_FAILURE_NO_PEERS}
		return r, "no_peers"
	default:
		f.stats.timeouts.Add(1)
		r.Outcome = &sniffarrv1.Result_Failure{Failure: sniffarrv1.Failure_FAILURE_TIMEOUT}
		return r, "timeout"
	}
}

func (f *Fetcher) log(msg string) {
	f.mu.Lock()
	busy := f.busy
	f.mu.Unlock()
	f.cfg.Log.Info(msg, "tasks", f.stats.tasks.Load(), "busy", busy, "torrents", f.stats.torrents.Load(),
		"no_peers", f.stats.noPeers.Load(), "timeouts", f.stats.timeouts.Load())
}
