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

type fakeIngest struct {
	mu        sync.Mutex
	known     map[Hash]bool
	asked     []Hash
	submitted map[Hash]*sniffarrv1.Torrent
	// failUnknown and failSubmit are how many calls fail before one succeeds.
	failUnknown, failSubmit int
}

func (f *fakeIngest) Unknown(_ context.Context, req *connect.Request[sniffarrv1.UnknownRequest]) (*connect.Response[sniffarrv1.UnknownResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failUnknown > 0 {
		f.failUnknown--
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("down"))
	}
	var out [][]byte
	for _, b := range req.Msg.GetInfohashes() {
		f.asked = append(f.asked, Hash(b))
		if !f.known[Hash(b)] {
			out = append(out, b)
		}
	}
	return connect.NewResponse(&sniffarrv1.UnknownResponse{Infohashes: out}), nil
}

func (f *fakeIngest) Submit(_ context.Context, stream *connect.ClientStream[sniffarrv1.SubmitRequest]) (*connect.Response[sniffarrv1.SubmitResponse], error) {
	var got []*sniffarrv1.Torrent
	for stream.Receive() {
		got = append(got, stream.Msg().GetTorrent())
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failSubmit > 0 {
		f.failSubmit--
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("down"))
	}
	var res sniffarrv1.SubmitResponse
	for _, t := range got {
		h := Hash(t.GetInfohash())
		if _, dup := f.submitted[h]; dup {
			res.Duplicate++
			continue
		}
		f.submitted[h] = t
		res.Accepted++
	}
	return connect.NewResponse(&res), nil
}

func (f *fakeIngest) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.submitted)
}

type fakeFetcher struct {
	mu      sync.Mutex
	fetched map[Hash]int
	noPeers map[Hash]bool
}

func (f *fakeFetcher) Fetch(ctx context.Context, h Hash) (Metadata, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetched[h]++
	if f.noPeers[h] {
		return Metadata{}, ErrNoPeers
	}
	return Metadata{Name: "torrent", Size: 1 << 30, Files: []File{{Path: "a/b.mkv", Size: 1 << 30}}}, nil
}

func hash(b byte) Hash {
	return Hash{b}
}

func start(t *testing.T, ingest *fakeIngest, f Fetcher) (*Sniffer, context.CancelFunc, <-chan error) {
	t.Helper()
	if ingest.known == nil {
		ingest.known = map[Hash]bool{}
	}
	ingest.submitted = map[Hash]*sniffarrv1.Torrent{}
	mux := http.NewServeMux()
	mux.Handle(sniffarrv1connect.NewIngestServiceHandler(ingest))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s := New(Config{
		Ingest:    sniffarrv1connect.NewIngestServiceClient(srv.Client(), srv.URL),
		Fetchers:  4,
		BatchSize: 3,
		BatchWait: 10 * time.Millisecond,
		StreamFor: 20 * time.Millisecond,
		Backoff:   5 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, f) }()
	t.Cleanup(cancel)
	return s, cancel, done
}

func TestSnifferFetchesOnlyUnknown(t *testing.T) {
	ingest := &fakeIngest{known: map[Hash]bool{hash(2): true, hash(4): true}}
	fetcher := &fakeFetcher{fetched: map[Hash]int{}, noPeers: map[Hash]bool{hash(6): true}}
	s, cancel, done := start(t, ingest, fetcher)

	for _, b := range []byte{1, 2, 3, 4, 5, 6, 1, 3} {
		s.Offer(hash(b), sniffarrv1.Source_SOURCE_SAMPLE)
	}
	s.Offer(hash(7), sniffarrv1.Source_SOURCE_ANNOUNCE)

	require.Eventually(t, func() bool { return ingest.count() == 4 }, 5*time.Second, 5*time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	require.ElementsMatch(t, []Hash{hash(1), hash(2), hash(3), hash(4), hash(5), hash(6), hash(7)}, ingest.asked, "each hash is asked about once")
	require.Equal(t, map[Hash]int{hash(1): 1, hash(3): 1, hash(5): 1, hash(6): 1, hash(7): 1}, fetcher.fetched)
	require.Equal(t, sniffarrv1.Source_SOURCE_ANNOUNCE, ingest.submitted[hash(7)].GetSource())
	require.Equal(t, sniffarrv1.Source_SOURCE_SAMPLE, ingest.submitted[hash(1)].GetSource())
	require.Equal(t, "a/b.mkv", ingest.submitted[hash(1)].GetFiles()[0].GetPath())
	require.EqualValues(t, 4, s.stats.accepted.Load())
}

func TestSnifferRetries(t *testing.T) {
	ingest := &fakeIngest{failUnknown: 2, failSubmit: 2}
	fetcher := &fakeFetcher{fetched: map[Hash]int{}}
	s, cancel, done := start(t, ingest, fetcher)

	for b := range byte(5) {
		s.Offer(hash(b), sniffarrv1.Source_SOURCE_SAMPLE)
	}
	require.Eventually(t, func() bool { return ingest.count() == 5 }, 5*time.Second, 5*time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	for b := range byte(5) {
		require.Equal(t, 1, fetcher.fetched[hash(b)], "fetched once although the server failed")
	}
}

func TestSnifferFlushesOnShutdown(t *testing.T) {
	ingest := &fakeIngest{known: map[Hash]bool{}, submitted: map[Hash]*sniffarrv1.Torrent{}}
	fetcher := &fakeFetcher{fetched: map[Hash]int{}}
	mux := http.NewServeMux()
	mux.Handle(sniffarrv1connect.NewIngestServiceHandler(ingest))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := New(Config{
		Ingest:    sniffarrv1connect.NewIngestServiceClient(srv.Client(), srv.URL),
		Fetchers:  1,
		StreamFor: time.Hour,
		BatchWait: time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, fetcher) }()

	s.Offer(hash(1), sniffarrv1.Source_SOURCE_SAMPLE)
	require.Eventually(t, func() bool { return s.stats.fetched.Load() == 1 }, 5*time.Second, time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(flushTimeout + time.Second):
		t.Fatal("Run did not return after cancel")
	}
	require.Equal(t, 1, ingest.count(), "the open stream is closed and confirmed on shutdown")
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
