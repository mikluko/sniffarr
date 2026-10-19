// Package sniffer discovers torrents on the BitTorrent DHT, fetches their
// metadata from peers, and submits it to a sniffarr server. It never
// downloads pieces, uploads, or announces itself, and it keeps peer
// addresses only for the duration of a fetch.
package sniffer

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
)

// Hash is a BitTorrent v1 infohash.
type Hash [20]byte

// Metadata is what a torrent's peers tell about it.
type Metadata struct {
	Name  string
	Size  int64
	Files []File
}

// File is one file of a torrent.
type File struct {
	Path string
	Size int64
}

var (
	// ErrNoPeers is returned by a Fetcher that found nobody sharing the torrent.
	ErrNoPeers = errors.New("no peers")
	// ErrNoMetadata is returned by a Fetcher whose peers did not give up the
	// metadata in time.
	ErrNoMetadata = errors.New("no metadata")
)

// Fetcher gets a torrent's metadata.
type Fetcher interface {
	Fetch(ctx context.Context, h Hash) (Metadata, error)
}

const (
	DefaultFetchers  = 2000
	DefaultBatchSize = 500
	DefaultBatchWait = time.Second
	DefaultQueue     = 10000
	DefaultRemember  = 1000000
	DefaultStreamFor = time.Minute
	DefaultBackoff   = 5 * time.Second
	DefaultReport    = 30 * time.Second
)

// Config tunes a Sniffer. Zero fields take the default named in their doc.
type Config struct {
	Ingest sniffarrv1connect.IngestServiceClient
	// Fetchers is how many fetches run at once; DefaultFetchers.
	Fetchers int
	// BatchSize is the most hashes asked about in one Unknown call;
	// DefaultBatchSize.
	BatchSize int
	// BatchWait is the longest a partial batch waits; DefaultBatchWait.
	BatchWait time.Duration
	// Queue is the capacity of each queue of hashes; DefaultQueue. Hashes
	// offered to a full queue are dropped.
	Queue int
	// Remember is how many offered hashes are remembered and not offered
	// again; DefaultRemember.
	Remember int
	// StreamFor is how long one Submit stream stays open; DefaultStreamFor.
	StreamFor time.Duration
	// Backoff is the wait after a failed call to the server; DefaultBackoff.
	Backoff time.Duration
	// Report is the interval of progress logs; DefaultReport.
	Report time.Duration
	// Log discards everything when nil.
	Log *slog.Logger
}

type candidate struct {
	hash   Hash
	source sniffarrv1.Source
}

// Sniffer asks the server which offered hashes it has never seen, fetches
// metadata for those, and streams it to Submit.
type Sniffer struct {
	cfg       Config
	recent    *recent[Hash]
	offers    chan candidate
	announced chan candidate
	sampled   chan candidate
	results   chan *sniffarrv1.Torrent
	stats     stats
}

type stats struct {
	offered, dropped, unknown, fetched, noPeers, noMetadata, accepted, rejected, duplicate atomic.Int64
}

// New returns a Sniffer for cfg.
func New(cfg Config) *Sniffer {
	orDefault(&cfg.Fetchers, DefaultFetchers)
	orDefault(&cfg.BatchSize, DefaultBatchSize)
	orDefault(&cfg.Queue, DefaultQueue)
	orDefault(&cfg.Remember, DefaultRemember)
	orDefault(&cfg.BatchWait, DefaultBatchWait)
	orDefault(&cfg.StreamFor, DefaultStreamFor)
	orDefault(&cfg.Backoff, DefaultBackoff)
	orDefault(&cfg.Report, DefaultReport)
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	return &Sniffer{
		cfg:       cfg,
		recent:    newRecent[Hash](cfg.Remember),
		offers:    make(chan candidate, cfg.Queue),
		announced: make(chan candidate, cfg.Queue),
		sampled:   make(chan candidate, cfg.Queue),
		results:   make(chan *sniffarrv1.Torrent, cfg.Fetchers),
	}
}

// orDefault sets a zero or negative *v to d.
func orDefault[T int | time.Duration](v *T, d T) {
	if *v <= 0 {
		*v = d
	}
}

// Offer queues a hash for the server's Unknown check unless it was offered
// recently. It never blocks and is safe for concurrent use.
func (s *Sniffer) Offer(h Hash, source sniffarrv1.Source) {
	if !s.recent.add(h) {
		return
	}
	s.stats.offered.Add(1)
	select {
	case s.offers <- candidate{h, source}:
	default:
		s.stats.dropped.Add(1)
	}
}

// Run sniffs until ctx is done, then flushes fetched metadata to the server
// for at most a few seconds and returns.
func (s *Sniffer) Run(ctx context.Context, f Fetcher) error {
	var wg sync.WaitGroup
	var fetchers sync.WaitGroup
	wg.Go(func() { s.batch(ctx) })
	for range s.cfg.Fetchers {
		fetchers.Go(func() { s.fetch(ctx, f) })
	}
	submitted := make(chan struct{})
	go func() {
		defer close(submitted)
		s.submit(ctx)
	}()
	wg.Go(func() { s.report(ctx) })
	wg.Wait()
	fetchers.Wait()
	close(s.results)
	<-submitted
	s.log("stopped")
	return ctx.Err()
}

// batch passes offered hashes through Unknown in batches and queues the
// unknown ones for fetching, announced ones apart from sampled.
func (s *Sniffer) batch(ctx context.Context) {
	batch := make(map[Hash]sniffarrv1.Source, s.cfg.BatchSize)
	timer := time.NewTimer(s.cfg.BatchWait)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case c := <-s.offers:
			if old, ok := batch[c.hash]; !ok || old != sniffarrv1.Source_SOURCE_ANNOUNCE {
				batch[c.hash] = c.source
			}
			if len(batch) < s.cfg.BatchSize {
				continue
			}
		case <-timer.C:
		}
		if len(batch) > 0 {
			s.ask(ctx, batch)
			clear(batch)
		}
		timer.Reset(s.cfg.BatchWait)
	}
}

// ask calls Unknown with the batch until it succeeds or ctx is done.
func (s *Sniffer) ask(ctx context.Context, batch map[Hash]sniffarrv1.Source) {
	req := &sniffarrv1.UnknownRequest{Infohashes: make([][]byte, 0, len(batch))}
	for h := range batch {
		req.Infohashes = append(req.Infohashes, h[:])
	}
	for {
		res, err := s.cfg.Ingest.Unknown(ctx, connect.NewRequest(req))
		if err == nil {
			for _, b := range res.Msg.GetInfohashes() {
				if len(b) != len(Hash{}) {
					continue
				}
				h := Hash(b)
				source, ok := batch[h]
				if !ok {
					continue
				}
				s.stats.unknown.Add(1)
				q := s.sampled
				if source == sniffarrv1.Source_SOURCE_ANNOUNCE {
					q = s.announced
				}
				select {
				case q <- candidate{h, source}:
				default:
					s.stats.dropped.Add(1)
				}
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		s.cfg.Log.Warn("unknown", "err", err)
		if sleep(ctx, s.cfg.Backoff) != nil {
			return
		}
	}
}

// next returns the next hash to fetch, an announced one if any is waiting.
func (s *Sniffer) next(ctx context.Context) (candidate, bool) {
	select {
	case c := <-s.announced:
		return c, true
	default:
	}
	select {
	case <-ctx.Done():
		return candidate{}, false
	case c := <-s.announced:
		return c, true
	case c := <-s.sampled:
		return c, true
	}
}

func (s *Sniffer) fetch(ctx context.Context, f Fetcher) {
	for {
		c, ok := s.next(ctx)
		if !ok {
			return
		}
		m, err := f.Fetch(ctx, c.hash)
		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, ErrNoPeers):
			s.stats.noPeers.Add(1)
			continue
		case err != nil:
			s.stats.noMetadata.Add(1)
			continue
		}
		s.stats.fetched.Add(1)
		s.results <- message(c, m, time.Now())
	}
}

func message(c candidate, m Metadata, seen time.Time) *sniffarrv1.Torrent {
	t := &sniffarrv1.Torrent{
		Infohash: c.hash[:],
		Name:     m.Name,
		Size:     m.Size,
		Files:    make([]*sniffarrv1.File, len(m.Files)),
		SeenAt:   timestamppb.New(seen),
		Source:   c.source,
	}
	for i, f := range m.Files {
		t.Files[i] = &sniffarrv1.File{Path: f.Path, Size: f.Size}
	}
	return t
}

// flushTimeout bounds the final Submit after Run's context is done.
const flushTimeout = 5 * time.Second

// submit streams results to Submit, one stream per StreamFor. A stream that
// fails is retried with everything sent on it, so nothing fetched is lost
// short of shutdown; the server counts the repeats as duplicates.
func (s *Sniffer) submit(ctx context.Context) {
	sub := submitter{s: s}
	tick := time.NewTicker(s.cfg.StreamFor)
	defer tick.Stop()
	for {
		select {
		case t, ok := <-s.results:
			if !ok {
				flush, cancel := context.WithTimeout(context.WithoutCancel(ctx), flushTimeout)
				sub.close(flush)
				cancel()
				return
			}
			sub.pending = append(sub.pending, t)
			if ctx.Err() == nil {
				sub.send(ctx)
			}
		case <-tick.C:
			if ctx.Err() == nil {
				sub.close(ctx)
			}
		}
	}
}

// submitter holds one Submit stream and the torrents sent on it since it
// opened.
type submitter struct {
	s       *Sniffer
	stream  *connect.ClientStreamForClient[sniffarrv1.SubmitRequest, sniffarrv1.SubmitResponse]
	cancel  context.CancelFunc
	sent    int
	pending []*sniffarrv1.Torrent
}

// send sends the pending torrents not yet sent, opening a stream if none is
// open, and retries on a new stream after a failure.
func (u *submitter) send(ctx context.Context) {
	for u.sent < len(u.pending) {
		if u.stream == nil {
			sctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
			u.stream, u.cancel = u.s.cfg.Ingest.Submit(sctx), cancel
			u.sent = 0
		}
		if err := u.stream.Send(&sniffarrv1.SubmitRequest{Torrent: u.pending[u.sent]}); err != nil {
			_, err := u.stream.CloseAndReceive()
			u.drop()
			u.s.cfg.Log.Warn("submit", "err", err)
			if sleep(ctx, u.s.cfg.Backoff) != nil {
				return
			}
			continue
		}
		u.sent++
	}
}

// close sends what is pending and closes the stream, keeping the pending
// torrents for the next stream if the server did not confirm them.
func (u *submitter) close(ctx context.Context) {
	for len(u.pending) > 0 {
		u.send(ctx)
		if u.stream == nil {
			return
		}
		res, err := u.stream.CloseAndReceive()
		u.drop()
		if err == nil {
			u.s.stats.accepted.Add(res.Msg.GetAccepted())
			u.s.stats.rejected.Add(res.Msg.GetRejected())
			u.s.stats.duplicate.Add(res.Msg.GetDuplicate())
			u.pending = u.pending[:0]
			return
		}
		u.s.cfg.Log.Warn("submit", "err", err)
		if sleep(ctx, u.s.cfg.Backoff) != nil {
			return
		}
	}
}

func (u *submitter) drop() {
	if u.cancel != nil {
		u.cancel()
	}
	u.stream, u.cancel, u.sent = nil, nil, 0
}

func (s *Sniffer) report(ctx context.Context) {
	tick := time.NewTicker(s.cfg.Report)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.log("progress")
		}
	}
}

func (s *Sniffer) log(msg string) {
	st := &s.stats
	s.cfg.Log.Info(msg,
		"offered", st.offered.Load(), "dropped", st.dropped.Load(), "unknown", st.unknown.Load(),
		"announced_queue", len(s.announced), "sampled_queue", len(s.sampled),
		"fetched", st.fetched.Load(), "no_peers", st.noPeers.Load(), "no_metadata", st.noMetadata.Load(),
		"accepted", st.accepted.Load(), "rejected", st.rejected.Load(), "duplicate", st.duplicate.Load())
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// recent is a bounded set: once it holds size keys it forgets the older
// half.
type recent[K comparable] struct {
	mu        sync.Mutex
	size      int
	cur, prev map[K]struct{}
}

func newRecent[K comparable](size int) *recent[K] {
	return &recent[K]{size: size, cur: map[K]struct{}{}, prev: map[K]struct{}{}}
}

// add adds h and reports whether it was absent.
func (r *recent[K]) add(h K) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.cur[h]; ok {
		return false
	}
	if _, ok := r.prev[h]; ok {
		return false
	}
	if len(r.cur) >= r.size/2 {
		r.prev, r.cur = r.cur, make(map[K]struct{}, len(r.cur))
	}
	r.cur[h] = struct{}{}
	return true
}
