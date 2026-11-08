package ingest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"sarak.sh/mikluko/sniffarr/internal/audio"
	"sarak.sh/mikluko/sniffarr/internal/bus"
	"sarak.sh/mikluko/sniffarr/internal/orders"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

func infohash(b byte) []byte {
	h := store.Hash{b}
	return h[:]
}

// take takes up to n orders for owner, and returns them.
func (f *intakeFixture) take(t *testing.T, owner int64, n int) []*orders.Order {
	t.Helper()
	got, err := f.s.Fetches.Take(owner, n, store.PrioritySample)
	require.NoError(t, err)
	require.NoError(t, f.s.journalLeases(t.Context(), got))
	return got
}

func torrentResult(b byte, name string) *sniffarrv1.Result {
	return &sniffarrv1.Result{Infohash: infohash(b), Outcome: &sniffarrv1.Result_Torrent{Torrent: &sniffarrv1.Torrent{Name: name, Size: 8 << 30,
		Files: []*sniffarrv1.File{{Path: name + ".mkv", Size: 8 << 30}}}}}
}

func failure(b byte, f sniffarrv1.Failure) *sniffarrv1.Result {
	return &sniffarrv1.Result{Infohash: infohash(b), Outcome: &sniffarrv1.Result_Failure{Failure: f}}
}

func TestResultOutlivesStream(t *testing.T) {
	f := newIntakeFixture(t)
	f.order(t, store.PrioritySample, 1, 2)
	require.Len(t, f.take(t, 1, 2), 2)

	ended, cancel := context.WithCancel(t.Context())
	cancel()
	require.NoError(t, f.s.result(ended, 1, failure(1, sniffarrv1.Failure_FAILURE_NO_PEERS)))
	require.NoError(t, f.s.result(ended, 1, torrentResult(2, "Heat.1995.1080p.BluRay")))

	_, err := f.st.Get(t.Context(), store.Hash{2})
	require.NoError(t, err, "the torrent is stored")
	f.s.Fetches.Release(1)
	require.Empty(t, f.take(t, 2, 2), "1 is dead for having no peers, and 2 is done")
	require.Empty(t, f.queued(t), "both orders are acknowledged")
}

func TestResultAfterRelease(t *testing.T) {
	f := newIntakeFixture(t)
	f.order(t, store.PriorityAnnounce, 1)
	require.Len(t, f.take(t, 1, 1), 1)
	f.s.Fetches.Release(1)
	require.NoError(t, f.s.result(t.Context(), 2, torrentResult(1, "Heat.1995.1080p.BluRay")), "a result carried over to the next stream")
	require.Equal(t, store.Announce, f.discovery(t, 1), "stored with its order's discovery")
	got := f.take(t, 2, 1)
	require.Len(t, got, 1, "the order handed back is still on the stream")
	require.NoError(t, f.s.result(t.Context(), 2, torrentResult(1, "Heat.1995.1080p.BluRay")))
	require.Equal(t, []string{"leased/fetch", "fetched/stored", "fetched/duplicate"}, journal(t, f.st, store.Hash{1}),
		"its second fetch comes to a duplicate")
	require.Empty(t, f.queued(t))
}

func TestFetchStreamEndNaks(t *testing.T) {
	f := newIntakeFixture(t)
	f.order(t, store.PriorityArr, 1)
	stream := f.serve(t)
	require.NoError(t, stream.Send(&sniffarrv1.FetchRequest{Kind: &sniffarrv1.FetchRequest_Credit{Credit: &sniffarrv1.Credit{N: 1}}}))
	got, err := stream.Receive()
	require.NoError(t, err)
	require.Equal(t, infohash(1), got.GetTask().GetInfohash())
	require.NoError(t, stream.CloseRequest())
	require.NoError(t, stream.CloseResponse())
	var again []*orders.Order
	require.Eventually(t, func() bool {
		again = f.take(t, 9, 1)
		return len(again) == 1
	}, 5*time.Second, 10*time.Millisecond, "an order whose stream ended without a result goes back at once")
	require.Equal(t, 2, again[0].Leases)
	require.Zero(t, again[0].Attempt, "a stream that ends is no failed attempt")
}

func TestResultFailures(t *testing.T) {
	f := newIntakeFixture(t)
	f.order(t, store.PrioritySample, 1, 2)
	require.Len(t, f.take(t, 1, 2), 2)

	require.NoError(t, f.s.result(t.Context(), 2, failure(1, sniffarrv1.Failure_FAILURE_NO_PEERS)), "a report from another holder")
	require.Equal(t, 2, f.s.Fetches.Held(store.TaskFetch), "is ignored")
	require.NoError(t, f.s.result(t.Context(), 1, failure(1, sniffarrv1.Failure_FAILURE_NO_PEERS)))
	require.NoError(t, f.s.result(t.Context(), 1, failure(2, sniffarrv1.Failure_FAILURE_TIMEOUT)))

	held, err := f.st.Screen(t.Context(), []store.Hash{{1}, {2}}, f.now)
	require.NoError(t, err)
	require.Equal(t, map[store.Hash]store.Screened{{1}: {Dead: true}}, held, "no peers drops the hash at once, a timeout is retried")
	q := f.queued(t)
	require.Len(t, q, 1)
	require.Equal(t, byte(2), q[0].hash)
	require.Equal(t, bus.RetrySubject(bus.StreamFetchOrders, store.Hash{2}), q[0].subject)
	require.Equal(t, 1, q[0].attempt)
	require.Equal(t, "@at "+f.now.Add(DefaultBackoff).UTC().Format(time.RFC3339), q[0].schedule, "after the backoff")
}

func TestRetryUntilDead(t *testing.T) {
	f := newIntakeFixture(t)
	f.s.Now, f.s.Attempts, f.s.Backoff, f.s.DeadFor = time.Now, 2, time.Second, time.Hour
	f.order(t, store.PriorityAnnounce, 1)
	require.Len(t, f.take(t, 1, 1), 1)
	require.NoError(t, f.s.result(t.Context(), 1, failure(1, sniffarrv1.Failure_FAILURE_TIMEOUT)))
	require.Empty(t, f.take(t, 1, 1), "the retry waits for its time")
	var retry []*orders.Order
	require.Eventually(t, func() bool {
		retry = f.take(t, 1, 1)
		return len(retry) == 1
	}, 10*time.Second, 50*time.Millisecond, "the retry fires")
	require.Equal(t, 1, retry[0].Attempt)
	require.Equal(t, store.PriorityAnnounce, retry[0].Priority)
	require.NoError(t, f.s.result(t.Context(), 1, failure(1, sniffarrv1.Failure_FAILURE_TIMEOUT)))
	require.Empty(t, f.queued(t), "the last attempt schedules none")
	held, err := f.st.Screen(t.Context(), []store.Hash{{1}}, time.Now())
	require.NoError(t, err)
	require.True(t, held[store.Hash{1}].Dead, "and keeps the hash dead")
	require.Equal(t, []string{"leased/fetch", "fetch_failed/timeout", "leased/fetch", "fetch_failed/dropped"}, journal(t, f.st, store.Hash{1}))
}

func TestFailureOfSecondOrder(t *testing.T) {
	f := newIntakeFixture(t)
	f.order(t, store.PriorityAnnounce, 1)
	require.Len(t, f.take(t, 1, 1), 1)
	require.NoError(t, f.s.result(t.Context(), 1, torrentResult(1, "Heat.1995.1080p.BluRay")))
	f.order(t, store.PrioritySample, 1)
	require.Len(t, f.take(t, 1, 1), 1, "a stale copy of a fetched hash still goes out")
	require.NoError(t, f.s.result(t.Context(), 1, failure(1, sniffarrv1.Failure_FAILURE_TIMEOUT)))
	require.Empty(t, f.queued(t), "but its failure schedules no retry of a stored hash")
}

func TestResultJournals(t *testing.T) {
	f := newIntakeFixture(t)
	f.order(t, store.PriorityAnnounce, 1, 3)
	f.order(t, store.PrioritySample, 2, 4)
	got := f.take(t, 1, 4)
	require.Len(t, got, 4)

	for _, b := range []byte{1, 2} {
		require.NoError(t, f.s.result(t.Context(), 1, failure(b, sniffarrv1.Failure_FAILURE_TIMEOUT)))
	}
	require.NoError(t, f.s.result(t.Context(), 1, failure(3, sniffarrv1.Failure_FAILURE_NO_PEERS)))
	require.NoError(t, f.s.result(t.Context(), 1, torrentResult(4, "Heat.1995.1080p.BluRay")))

	require.Equal(t, []string{"leased/fetch", "fetch_failed/timeout"}, journal(t, f.st, store.Hash{1}))
	require.Empty(t, journal(t, f.st, store.Hash{2}), "a sample's lease and failure are not journaled")
	require.Equal(t, []string{"leased/fetch", "fetch_failed/no_peers"}, journal(t, f.st, store.Hash{3}))
	require.Equal(t, []string{"fetched/stored"}, journal(t, f.st, store.Hash{4}), "a sample's torrent is")
	e, err := f.st.Journal(t.Context(), store.Hash{4}, 1)
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"Heat.1995.1080p.BluRay","size":8589934592,"files":1,"discovery":"sample","queued_at":`+strconv.FormatInt(got[3].QueuedAt.Unix(), 10)+`,"kind":"movie","kind_layer":"rules"}`, e[0].Detail,
		"with when its order was published")
}

func TestResultStoresFeatures(t *testing.T) {
	f := newIntakeFixture(t)
	langs, err := audio.Parse("ru")
	require.NoError(t, err)
	f.s.Languages = langs
	f.order(t, store.PrioritySample, 1, 2)
	require.Len(t, f.take(t, 1, 2), 2)
	for i, name := range []string{"Heat.1995.1080p.BluRay.Rus.RHS", "The Office (US) S03E05 720p HDTV"} {
		require.NoError(t, f.s.result(t.Context(), 1, torrentResult(byte(i+1), name)))
	}

	movie, err := f.st.Get(t.Context(), store.Hash{1})
	require.NoError(t, err)
	require.Equal(t, "y:heat 1995", movie.TitleKey)
	require.Equal(t, 1995, movie.Year)
	require.Zero(t, movie.Season)
	require.Equal(t, "1080p", movie.Quality)
	require.Equal(t, []store.Audio{{Language: "ru", Marker: true, Mark: "RHS", Studios: []string{"Red Head Sound"}, Probability: new(1.0),
		Layer: store.LayerRules}}, movie.Audio, "the marker with its mark and studios")
	series, err := f.st.Get(t.Context(), store.Hash{2})
	require.NoError(t, err)
	require.Equal(t, "s:the office", series.TitleKey)
	require.Zero(t, series.Year)
	require.Equal(t, 3, series.Season)
	require.Equal(t, 5, series.Episode)
	require.Equal(t, "720p", series.Quality)
	require.Equal(t, "encode", series.Source, "the source its name gives")
	require.InDelta(t, 1, series.SourceConfidence, 0)
	require.Empty(t, movie.Source, "BluRay alone gives none")
}

// serve serves the fixture's service over HTTP/2 and returns a Fetch stream
// to it.
func (f *intakeFixture) serve(t *testing.T) *connect.BidiStreamForClient[sniffarrv1.FetchRequest, sniffarrv1.FetchResponse] {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(sniffarrv1connect.NewIngestServiceHandler(f.s))
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	stream := sniffarrv1connect.NewIngestServiceClient(srv.Client(), srv.URL).Fetch(t.Context())
	t.Cleanup(func() { _ = stream.CloseRequest(); _ = stream.CloseResponse() })
	return stream
}

func TestFetchLookups(t *testing.T) {
	f := newIntakeFixture(t)
	_, _, err := f.st.Insert(t.Context(), store.Torrent{Hash: store.Hash{1}, Name: "Heat.1995.1080p", Size: 8 << 30, Discovery: store.Sample, SeenAt: f.now})
	require.NoError(t, err)
	l, ok := f.s.Fetches.Lane(store.TaskPeers, store.PriorityRequest)
	require.True(t, ok)
	_, err = f.s.Fetches.Publish(t.Context(), l, []orders.Item{{Hash: store.Hash{1}}})
	require.NoError(t, err)
	_, err = f.s.Enqueue(t.Context(), []store.Sighting{{Hash: store.Hash{2}, Discovery: store.Arr}})
	require.NoError(t, err)

	stream := f.serve(t)
	require.NoError(t, stream.Send(&sniffarrv1.FetchRequest{Kind: &sniffarrv1.FetchRequest_Credit{Credit: &sniffarrv1.Credit{N: 3}}}))
	first, err := stream.Receive()
	require.NoError(t, err)
	require.Equal(t, infohash(1), first.GetTask().GetInfohash())
	require.True(t, first.GetTask().GetPeers(), "a lookup asks for peers only")
	second, err := stream.Receive()
	require.NoError(t, err)
	require.Equal(t, infohash(2), second.GetTask().GetInfohash())
	require.False(t, second.GetTask().GetPeers())
	require.NoError(t, stream.Send(&sniffarrv1.FetchRequest{Kind: &sniffarrv1.FetchRequest_Result{Result: &sniffarrv1.Result{Infohash: infohash(1),
		Outcome: &sniffarrv1.Result_Lookup{Lookup: &sniffarrv1.Lookup{Peers: 4}}}}}))
	require.Eventually(t, func() bool {
		return len(f.queued(t)) == 1
	}, 5*time.Second, 10*time.Millisecond, "a lookup's result acknowledges it")
	require.Equal(t, []string{"sighted/queued", "leased/fetch"}, journal(t, f.st, store.Hash{2}), "the hand-out of an arr fetch is journaled")

	require.NoError(t, stream.Send(&sniffarrv1.FetchRequest{Kind: &sniffarrv1.FetchRequest_Result{Result: torrentResult(2, "Alien.1979.1080p.BluRay")}}))
	require.Eventually(t, func() bool {
		return len(f.queued(t)) == 0
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, store.Arr, f.discovery(t, 2))
}

// discovery returns the discovery the stored torrent of the hash records.
func (f *intakeFixture) discovery(t *testing.T, b byte) string {
	t.Helper()
	var d string
	require.NoError(t, f.db.R.QueryRowContext(t.Context(), "SELECT discovery FROM torrent_releases WHERE infohash = ?", infohash(b)).Scan(&d))
	return d
}

// journal returns the journal of the hash as kind/outcome, oldest first.
func journal(t *testing.T, st *store.Store, h store.Hash) []string {
	t.Helper()
	got, err := st.Journal(context.Background(), h, 100)
	require.NoError(t, err)
	out := make([]string, len(got))
	for i, e := range got {
		out[i] = e.Kind + "/" + e.Outcome
	}
	return out
}
