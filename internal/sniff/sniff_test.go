package sniff

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/torrent"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/sketch"
)

func hash(b byte) Hash {
	return Hash{b}
}

// fakeIngest is an IngestService that hands out todo as tasks, as far as each
// stream's credit allows, and records what it is told.
type fakeIngest struct {
	mu   sync.Mutex
	cond *sync.Cond

	sighted map[Hash]sniffarrv1.Source
	// stored are the hashes Sight reports as stored torrents.
	stored map[Hash]bool
	// failSight is how many Sight streams fail before one succeeds.
	failSight int

	todo    []Hash
	results map[Hash]*sniffarrv1.Result
	// credits holds the credit messages of each Fetch stream, in order.
	credits [][]int32
	// outstanding is the most tasks one stream held at once.
	outstanding int
	// failFetchAfter ends the first Fetch stream with an error after that
	// many results; 0 never does.
	failFetchAfter int

	// key and nodeID are what HashKey answers after failKey failures.
	key     sketch.Key
	nodeID  [20]byte
	failKey int
	// sketches holds the Peers requests received, by hash; a hash in
	// unknown is dropped. failPeers is how many Peers streams fail first.
	sketches  map[Hash][]*sniffarrv1.PeersRequest
	unknown   map[Hash]bool
	failPeers int
}

func newFake() *fakeIngest {
	f := &fakeIngest{sighted: map[Hash]sniffarrv1.Source{}, stored: map[Hash]bool{}, results: map[Hash]*sniffarrv1.Result{},
		sketches: map[Hash][]*sniffarrv1.PeersRequest{}, unknown: map[Hash]bool{}}
	f.cond = sync.NewCond(&f.mu)
	for i := range f.key {
		f.key[i] = byte(i)
	}
	for i := range f.nodeID {
		f.nodeID[i] = byte(0xa0 + i)
	}
	return f
}

func (f *fakeIngest) HashKey(context.Context, *connect.Request[sniffarrv1.HashKeyRequest]) (*connect.Response[sniffarrv1.HashKeyResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failKey > 0 {
		f.failKey--
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("down"))
	}
	return connect.NewResponse(&sniffarrv1.HashKeyResponse{Key: f.key[:], NodeId: f.nodeID[:]}), nil
}

func (f *fakeIngest) Peers(_ context.Context, stream *connect.ClientStream[sniffarrv1.PeersRequest]) (*connect.Response[sniffarrv1.PeersResponse], error) {
	var got []*sniffarrv1.PeersRequest
	for stream.Receive() {
		got = append(got, stream.Msg())
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPeers > 0 {
		f.failPeers--
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("down"))
	}
	var res sniffarrv1.PeersResponse
	for _, m := range got {
		h := Hash(m.GetInfohash())
		if f.unknown[h] {
			res.Dropped++
			continue
		}
		f.sketches[h] = append(f.sketches[h], m)
		res.Merged++
	}
	f.cond.Broadcast()
	return connect.NewResponse(&res), nil
}

// received returns the sketches received for h once there are at least n.
func (f *fakeIngest) received(t *testing.T, h Hash, n int) []*sniffarrv1.PeersRequest {
	t.Helper()
	deadline := time.AfterFunc(5*time.Second, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.cond.Broadcast()
	})
	defer deadline.Stop()
	f.mu.Lock()
	defer f.mu.Unlock()
	for len(f.sketches[h]) < n {
		f.cond.Wait()
		if len(f.sketches[h]) < n && !deadline.Stop() {
			t.Fatalf("%d sketches of %x, want %d", len(f.sketches[h]), h[:1], n)
		}
	}
	return f.sketches[h]
}

func (f *fakeIngest) Sight(_ context.Context, stream *connect.ClientStream[sniffarrv1.SightRequest]) (*connect.Response[sniffarrv1.SightResponse], error) {
	var got []*sniffarrv1.SightRequest
	for stream.Receive() {
		got = append(got, stream.Msg())
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failSight > 0 {
		f.failSight--
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("down"))
	}
	var res sniffarrv1.SightResponse
	for _, m := range got {
		h := Hash(m.GetInfohash())
		res.Stored = append(res.Stored, f.stored[h])
		if _, dup := f.sighted[h]; dup {
			res.Known++
			continue
		}
		f.sighted[h] = m.GetSource()
		res.New++
	}
	return connect.NewResponse(&res), nil
}

func (f *fakeIngest) Fetch(ctx context.Context, stream *connect.BidiStream[sniffarrv1.FetchRequest, sniffarrv1.FetchResponse]) error {
	f.mu.Lock()
	idx := len(f.credits)
	f.credits = append(f.credits, nil)
	f.mu.Unlock()
	credit, results := 0, 0
	leased := map[Hash]bool{}
	defer func() {
		f.mu.Lock()
		for h := range leased {
			f.todo = append(f.todo, h)
		}
		f.mu.Unlock()
		f.cond.Broadcast()
	}()
	failed := errors.New("")
	errc := make(chan error, 1)
	go func() {
		for {
			m, err := stream.Receive()
			if err != nil {
				errc <- err
				f.cond.Broadcast()
				return
			}
			f.mu.Lock()
			switch k := m.GetKind().(type) {
			case *sniffarrv1.FetchRequest_Credit:
				credit += int(k.Credit.GetN())
				f.credits[idx] = append(f.credits[idx], k.Credit.GetN())
			case *sniffarrv1.FetchRequest_Result:
				h := Hash(k.Result.GetInfohash())
				f.results[h] = k.Result
				delete(leased, h)
				results++
				if idx == 0 && f.failFetchAfter > 0 && results == f.failFetchAfter {
					f.mu.Unlock()
					errc <- failed
					f.cond.Broadcast()
					return
				}
			}
			f.mu.Unlock()
			f.cond.Broadcast()
		}
	}()
	for {
		f.mu.Lock()
		for (credit == 0 || len(f.todo) == 0) && len(errc) == 0 && ctx.Err() == nil {
			f.cond.Wait()
		}
		if len(errc) > 0 || ctx.Err() != nil {
			f.mu.Unlock()
			if err := <-errc; err == failed {
				return connect.NewError(connect.CodeUnavailable, errors.New("stream broken"))
			}
			return nil
		}
		h := f.todo[0]
		f.todo = f.todo[1:]
		credit--
		leased[h] = true
		f.outstanding = max(f.outstanding, len(leased))
		f.mu.Unlock()
		if err := stream.Send(&sniffarrv1.FetchResponse{Task: &sniffarrv1.Task{Infohash: h[:]}}); err != nil {
			return err
		}
	}
}

func (f *fakeIngest) client(t *testing.T) sniffarrv1connect.IngestServiceClient {
	mux := http.NewServeMux()
	mux.Handle(sniffarrv1connect.NewIngestServiceHandler(f))
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetHTTP1(true)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	t.Cleanup(srv.Close)
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	return sniffarrv1connect.NewIngestServiceClient(&http.Client{Transport: &http.Transport{Protocols: &p}}, srv.URL)
}

func (f *fakeIngest) count() (sighted, results int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sighted), len(f.results)
}

// fakeSource answers by the hash: even ones have no peers, multiples of three
// time out, the rest give metadata. It holds each answer for delay.
type fakeSource struct {
	delay time.Duration
}

func (s fakeSource) Metadata(ctx context.Context, h Hash) (Metadata, error) {
	if sleep(ctx, s.delay) != nil {
		return Metadata{}, ctx.Err()
	}
	switch {
	case h[0]%2 == 0:
		return Metadata{}, ErrNoPeers
	case h[0]%3 == 0:
		return Metadata{}, ErrNoMetadata
	}
	return Metadata{Name: "torrent", Size: 1 << 30, Files: []File{{Path: "a/b.mkv", Size: 1 << 30}}}, nil
}

func run(t *testing.T, f func(context.Context) error) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(flushTimeout + time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	return cancel
}

func TestDiscoverer(t *testing.T) {
	ingest := newFake()
	ingest.failSight = 2
	ingest.stored[hash(3)] = true
	var mu sync.Mutex
	var stored []Hash
	d := NewDiscoverer(DiscovererConfig{Ingest: ingest.client(t), StreamFor: 20 * time.Millisecond, Backoff: 5 * time.Millisecond,
		Stored: func(h Hash) {
			mu.Lock()
			defer mu.Unlock()
			stored = append(stored, h)
		}})
	run(t, d.Run)

	for _, b := range []byte{1, 2, 1, 3} {
		d.Offer(hash(b), sniffarrv1.Source_SOURCE_SAMPLE)
	}
	d.Offer(hash(4), sniffarrv1.Source_SOURCE_ANNOUNCE)
	d.Offer(hash(4), sniffarrv1.Source_SOURCE_SAMPLE)

	require.Eventually(t, func() bool { n, _ := ingest.count(); return n == 4 }, 5*time.Second, 5*time.Millisecond)
	require.EqualValues(t, 4, d.stats.offered.Load(), "a recently offered hash is not offered again")
	require.Equal(t, sniffarrv1.Source_SOURCE_ANNOUNCE, ingest.sighted[hash(4)])
	require.Equal(t, sniffarrv1.Source_SOURCE_SAMPLE, ingest.sighted[hash(1)])
	require.Eventually(t, func() bool { return d.stats.new.Load() == 4 }, 5*time.Second, 5*time.Millisecond, "failed streams are resent")
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []Hash{hash(3)}, stored, "the hash the server reports as stored is passed on, once")
}

func TestDiscovererFlushesOnShutdown(t *testing.T) {
	ingest := newFake()
	d := NewDiscoverer(DiscovererConfig{Ingest: ingest.client(t), StreamFor: time.Hour})
	cancel := run(t, d.Run)
	d.Offer(hash(1), sniffarrv1.Source_SOURCE_SAMPLE)
	require.Eventually(t, func() bool { return len(d.offers) == 0 }, 5*time.Second, time.Millisecond)
	cancel()
	require.Eventually(t, func() bool { n, _ := ingest.count(); return n == 1 }, flushTimeout, time.Millisecond)
}

func TestFetcherCredit(t *testing.T) {
	ingest := newFake()
	for b := byte(1); b <= 12; b++ {
		ingest.todo = append(ingest.todo, hash(b))
	}
	f := NewFetcher(FetcherConfig{Ingest: ingest.client(t), Workers: 3}, fakeSource{delay: 10 * time.Millisecond})
	run(t, f.Run)

	require.Eventually(t, func() bool { _, n := ingest.count(); return n == 12 }, 5*time.Second, 5*time.Millisecond)
	ingest.mu.Lock()
	defer ingest.mu.Unlock()
	require.Equal(t, int32(3), ingest.credits[0][0], "the first credit is one per worker")
	require.LessOrEqual(t, ingest.outstanding, 3, "never more tasks than workers")
	require.NotNil(t, ingest.results[hash(1)].GetTorrent())
	require.Equal(t, "a/b.mkv", ingest.results[hash(1)].GetTorrent().GetFiles()[0].GetPath())
	require.Equal(t, sniffarrv1.Failure_FAILURE_NO_PEERS, ingest.results[hash(2)].GetFailure())
	require.Equal(t, sniffarrv1.Failure_FAILURE_TIMEOUT, ingest.results[hash(3)].GetFailure())
}

// recorder is a slog.Handler that keeps the records of the fetch outcomes.
type recorder struct {
	mu      sync.Mutex
	fetched []map[string]any
}

func (r *recorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *recorder) Handle(_ context.Context, rec slog.Record) error {
	if rec.Message != "fetched" || rec.Level != slog.LevelDebug {
		return nil
	}
	attrs := map[string]any{}
	rec.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fetched = append(r.fetched, attrs)
	return nil
}

func (r *recorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *recorder) WithGroup(string) slog.Handler      { return r }

func TestFetcherDebugLog(t *testing.T) {
	ingest := newFake()
	ingest.todo = []Hash{hash(1), hash(2), hash(3)}
	rec := &recorder{}
	f := NewFetcher(FetcherConfig{Ingest: ingest.client(t), Workers: 1, Log: slog.New(rec)}, fakeSource{})
	run(t, f.Run)

	require.Eventually(t, func() bool { _, n := ingest.count(); return n == 3 }, 5*time.Second, 5*time.Millisecond)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.Equal(t, []map[string]any{
		{"infohash": "0100000000000000000000000000000000000000", "outcome": "torrent", "name": "torrent", "size": int64(1 << 30), "files": int64(1)},
		{"infohash": "0200000000000000000000000000000000000000", "outcome": "no_peers"},
		{"infohash": "0300000000000000000000000000000000000000", "outcome": "timeout"},
	}, rec.fetched)
}

// fixedSource gives every hash the same metadata.
type fixedSource Metadata

func (s fixedSource) Metadata(context.Context, Hash) (Metadata, error) {
	return Metadata(s), nil
}

func TestFetcherInvalidUTF8(t *testing.T) {
	ingest := newFake()
	ingest.todo = []Hash{hash(1)}
	src := fixedSource{Name: "Movie.\xff2020", Size: 1 << 30, Files: []File{{Path: "a/\xfe\xfeb.mkv", Size: 1 << 30}}}
	f := NewFetcher(FetcherConfig{Ingest: ingest.client(t), Workers: 1}, src)
	run(t, f.Run)

	require.Eventually(t, func() bool { _, n := ingest.count(); return n == 1 }, 5*time.Second, 5*time.Millisecond)
	ingest.mu.Lock()
	defer ingest.mu.Unlock()
	tr := ingest.results[hash(1)].GetTorrent()
	require.Equal(t, "Movie.\uFFFD2020", tr.GetName())
	require.Equal(t, "a/\uFFFDb.mkv", tr.GetFiles()[0].GetPath(), "a run of invalid bytes is one replacement")
	require.Len(t, ingest.credits, 1, "the stream survives")
}

func TestFetcherMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	ingest := newFake()
	for b := byte(1); b <= 12; b++ {
		ingest.todo = append(ingest.todo, hash(b))
	}
	f := NewFetcher(FetcherConfig{Ingest: ingest.client(t), Workers: 3, Meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))}, fakeSource{})
	run(t, f.Run)
	require.Eventually(t, func() bool { _, n := ingest.count(); return n == 12 }, 5*time.Second, 5*time.Millisecond)

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	outcomes := map[string]int64{}
	var fetches uint64
	var inFlight int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch m.Name {
			case "sniffarr.sniff.fetch.outcomes":
				for _, p := range m.Data.(metricdata.Sum[int64]).DataPoints {
					o, _ := p.Attributes.Value("sniffarr.fetch.outcome")
					outcomes[o.AsString()] = p.Value
				}
			case "sniffarr.sniff.fetch.duration":
				require.Equal(t, "s", m.Unit)
				for _, p := range m.Data.(metricdata.Histogram[float64]).DataPoints {
					fetches += p.Count
				}
			case "sniffarr.sniff.fetch.in_flight":
				for _, p := range m.Data.(metricdata.Sum[int64]).DataPoints {
					inFlight += p.Value
				}
			}
		}
	}
	require.Equal(t, map[string]int64{"torrent": 4, "no_peers": 6, "timeout": 2}, outcomes)
	require.EqualValues(t, 12, fetches)
	require.Zero(t, inFlight)
}

func TestFetcherReconnects(t *testing.T) {
	ingest := newFake()
	ingest.failFetchAfter = 2
	for b := byte(1); b <= 6; b++ {
		ingest.todo = append(ingest.todo, hash(b))
	}
	f := NewFetcher(FetcherConfig{Ingest: ingest.client(t), Workers: 2, Backoff: 5 * time.Millisecond}, fakeSource{})
	run(t, f.Run)

	require.Eventually(t, func() bool { _, n := ingest.count(); return n == 6 }, 5*time.Second, 5*time.Millisecond, "the leases of the broken stream are fetched again")
	ingest.mu.Lock()
	defer ingest.mu.Unlock()
	require.GreaterOrEqual(t, len(ingest.credits), 2, "a second stream")
	require.LessOrEqual(t, ingest.outstanding, 2)
}

func TestFetcherStopsWithTasksInFlight(t *testing.T) {
	ingest := newFake()
	ingest.todo = []Hash{hash(1), hash(3)}
	f := NewFetcher(FetcherConfig{Ingest: ingest.client(t), Workers: 2}, fakeSource{delay: time.Hour})
	cancel := run(t, f.Run)
	require.Eventually(t, func() bool { return f.stats.tasks.Load() == 2 }, 5*time.Second, time.Millisecond)
	cancel()
}

// fakeSwarm answers a lookup with the batches of peers it holds for the hash,
// each delay after the one before, and gives metadata to any fetch.
type fakeSwarm struct {
	batches map[Hash][]dht.PeersValues
	delay   time.Duration

	mu sync.Mutex
	// fetched is how many peers each fetch was given, by hash.
	fetched map[Hash]int
}

// batch is a get_peers answer naming peers on the given ports of one address.
func batch(ports ...int) dht.PeersValues {
	var v dht.PeersValues
	for _, p := range ports {
		v.Peers = append(v.Peers, dht.Peer{IP: net.IPv4(192, 0, 2, 1), Port: p})
	}
	return v
}

func (s *fakeSwarm) lookup(h Hash) (<-chan dht.PeersValues, func(), error) {
	c := make(chan dht.PeersValues)
	done := make(chan struct{})
	go func() {
		for _, b := range s.batches[h] {
			select {
			case <-time.After(s.delay):
			case <-done:
				return
			}
			select {
			case c <- b:
			case <-done:
				return
			}
		}
	}()
	return c, sync.OnceFunc(func() { close(done) }), nil
}

func (s *fakeSwarm) fetch(_ context.Context, h Hash, peers []torrent.PeerInfo) (Metadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetched[h] = len(peers)
	return Metadata{Name: "torrent", Size: 1 << 30}, nil
}

func fakeNetwork(cfg NetworkConfig, s *fakeSwarm) *Network {
	s.fetched = map[Hash]int{}
	n := New(cfg, nil, nil)
	n.swarm = s
	close(n.ready)
	return n
}

func TestFetcherMinPeers(t *testing.T) {
	ingest := newFake()
	ingest.todo = []Hash{hash(1), hash(2), hash(3)}
	s := &fakeSwarm{batches: map[Hash][]dht.PeersValues{
		hash(1): {batch(1, 2)},
		hash(2): {batch(1, 2), batch(3)},
		hash(3): {batch(1, 1), batch(2, 2)},
	}}
	f := NewFetcher(FetcherConfig{Ingest: ingest.client(t), Workers: 3}, fakeNetwork(NetworkConfig{MinPeers: 3, PeerWait: 100 * time.Millisecond}, s))
	run(t, f.Run)

	require.Eventually(t, func() bool { _, n := ingest.count(); return n == 3 }, 5*time.Second, 5*time.Millisecond)
	ingest.mu.Lock()
	defer ingest.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Equal(t, sniffarrv1.Failure_FAILURE_NO_PEERS, ingest.results[hash(1)].GetFailure(), "below MinPeers")
	require.NotNil(t, ingest.results[hash(2)].GetTorrent(), "at MinPeers")
	require.Equal(t, sniffarrv1.Failure_FAILURE_NO_PEERS, ingest.results[hash(3)].GetFailure(), "a peer named twice counts once")
	require.Equal(t, map[Hash]int{hash(2): 3}, s.fetched, "no fetch below MinPeers")
}

func TestFindPeersWaitsForMinPeers(t *testing.T) {
	for _, tt := range []struct {
		name     string
		minPeers int
		want     int
	}{
		{name: "default", want: enoughPeers},
		{name: "above enough", minPeers: enoughPeers + 5, want: enoughPeers + 5},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ports := make([]int, enoughPeers+5)
			for i := range ports {
				ports[i] = 1000 + i
			}
			ingest := newFake()
			ingest.todo = []Hash{hash(1)}
			s := &fakeSwarm{batches: map[Hash][]dht.PeersValues{hash(1): {batch(ports[:enoughPeers]...), batch(ports[enoughPeers:]...)}}, delay: 20 * time.Millisecond}
			f := NewFetcher(FetcherConfig{Ingest: ingest.client(t), Workers: 1}, fakeNetwork(NetworkConfig{MinPeers: tt.minPeers, PeerWait: time.Minute}, s))
			run(t, f.Run)

			require.Eventually(t, func() bool { _, n := ingest.count(); return n == 1 }, 5*time.Second, 5*time.Millisecond, "the search ends once it has its peers")
			s.mu.Lock()
			defer s.mu.Unlock()
			require.Equal(t, tt.want, s.fetched[hash(1)])
		})
	}
}

func TestRecent(t *testing.T) {
	r := newRecent[Hash](4)
	require.True(t, r.add(hash(1)))
	require.False(t, r.add(hash(1)))
	for b := byte(2); b <= 5; b++ {
		require.True(t, r.add(hash(b)))
	}
	require.False(t, r.add(hash(4)), "the newer half is remembered")
	require.True(t, r.add(hash(1)), "the oldest are forgotten")
}
