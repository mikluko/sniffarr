package sniffer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
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
}

func newFake() *fakeIngest {
	f := &fakeIngest{sighted: map[Hash]sniffarrv1.Source{}, results: map[Hash]*sniffarrv1.Result{}}
	f.cond = sync.NewCond(&f.mu)
	return f
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
	d := NewDiscoverer(DiscovererConfig{Ingest: ingest.client(t), StreamFor: 20 * time.Millisecond, Backoff: 5 * time.Millisecond})
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
