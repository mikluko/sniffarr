package ingest

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/audio"
	"github.com/mikluko/sniffarr/internal/database"
	"github.com/mikluko/sniffarr/internal/rules"
	"github.com/mikluko/sniffarr/internal/store"
)

func infohash(b byte) []byte {
	h := store.Hash{b}
	return h[:]
}

func TestResultOutlivesStream(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	st, err := store.New(ctx, db, store.Options{})
	require.NoError(t, err)
	q, err := store.NewTasks(ctx, db, store.TaskOptions{})
	require.NoError(t, err)
	s := &Service{Store: st, Tasks: q, Log: slog.New(slog.DiscardHandler)}
	now := time.Now()
	_, _, err = q.Enqueue(ctx, []store.Sighting{{Hash: store.Hash{1}, Discovery: store.Sample}, {Hash: store.Hash{2}, Discovery: store.Sample}}, now)
	require.NoError(t, err)
	leased, _, err := q.Lease(ctx, 1, 2, now, time.Minute)
	require.NoError(t, err)
	require.Len(t, leased, 2)

	ended, cancel := context.WithCancel(ctx)
	cancel()
	require.NoError(t, s.result(ended, 1, &sniffarrv1.Result{Infohash: infohash(1),
		Outcome: &sniffarrv1.Result_Failure{Failure: sniffarrv1.Failure_FAILURE_NO_PEERS}}))
	require.NoError(t, s.result(ended, 1, &sniffarrv1.Result{Infohash: infohash(2),
		Outcome: &sniffarrv1.Result_Torrent{Torrent: &sniffarrv1.Torrent{Name: "Heat.1995.1080p.BluRay", Size: 8 << 30,
			Files: []*sniffarrv1.File{{Path: "Heat.1995.mkv", Size: 8 << 30}}}}}))

	_, err = st.Get(ctx, store.Hash{2})
	require.NoError(t, err, "the torrent is stored")
	require.NoError(t, q.Release(ctx, 1))
	again, _, err := q.Lease(ctx, 2, 2, now, time.Minute)
	require.NoError(t, err)
	require.Empty(t, again, "1 is dead for having no peers, and 2 is done")
}

func TestResultFailures(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	q, err := store.NewTasks(ctx, db, store.TaskOptions{})
	require.NoError(t, err)
	now := time.Now()
	s := &Service{Tasks: q, Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now }}
	_, _, err = q.Enqueue(ctx, []store.Sighting{{Hash: store.Hash{1}, Discovery: store.Sample}, {Hash: store.Hash{2}, Discovery: store.Sample}}, now)
	require.NoError(t, err)
	_, _, err = q.Lease(ctx, 1, 2, now, time.Minute)
	require.NoError(t, err)

	require.NoError(t, s.result(ctx, 1, &sniffarrv1.Result{Infohash: infohash(1),
		Outcome: &sniffarrv1.Result_Failure{Failure: sniffarrv1.Failure_FAILURE_NO_PEERS}}))
	require.NoError(t, s.result(ctx, 1, &sniffarrv1.Result{Infohash: infohash(2),
		Outcome: &sniffarrv1.Result_Failure{Failure: sniffarrv1.Failure_FAILURE_TIMEOUT}}))

	_, err = q.Discovery(ctx, store.Hash{1})
	require.ErrorIs(t, err, store.ErrNotQueued, "no peers drops the hash at once")
	_, err = q.Discovery(ctx, store.Hash{2})
	require.NoError(t, err, "a timeout is retried")
	again, _, err := q.Lease(ctx, 2, 2, now.Add(DefaultBackoff), time.Minute)
	require.NoError(t, err)
	require.Equal(t, []store.Task{{Kind: store.TaskFetch, Hash: store.Hash{2}}}, again)
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

func TestResultJournals(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	st, err := store.New(ctx, db, store.Options{})
	require.NoError(t, err)
	q, err := store.NewTasks(ctx, db, store.TaskOptions{})
	require.NoError(t, err)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	s := &Service{Store: st, Tasks: q, Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now }}
	_, _, err = q.Enqueue(ctx, []store.Sighting{{Hash: store.Hash{1}, Discovery: store.Announce}, {Hash: store.Hash{2}, Discovery: store.Sample},
		{Hash: store.Hash{3}, Discovery: store.Announce}, {Hash: store.Hash{4}, Discovery: store.Sample}}, now)
	require.NoError(t, err)
	_, _, err = q.Lease(ctx, 1, 4, now, time.Minute)
	require.NoError(t, err)

	for _, b := range []byte{1, 2} {
		require.NoError(t, s.result(ctx, 1, &sniffarrv1.Result{Infohash: infohash(b), Outcome: &sniffarrv1.Result_Failure{Failure: sniffarrv1.Failure_FAILURE_TIMEOUT}}))
	}
	require.NoError(t, s.result(ctx, 1, &sniffarrv1.Result{Infohash: infohash(3), Outcome: &sniffarrv1.Result_Failure{Failure: sniffarrv1.Failure_FAILURE_NO_PEERS}}))
	require.NoError(t, s.result(ctx, 1, &sniffarrv1.Result{Infohash: infohash(4),
		Outcome: &sniffarrv1.Result_Torrent{Torrent: &sniffarrv1.Torrent{Name: "Heat.1995.1080p.BluRay", Size: 8 << 30,
			Files: []*sniffarrv1.File{{Path: "Heat.1995.mkv", Size: 8 << 30}}}}}))

	require.Equal(t, []string{"sighted/queued", "leased/fetch", "fetch_failed/timeout"}, journal(t, st, store.Hash{1}))
	require.Empty(t, journal(t, st, store.Hash{2}), "a sample's failure is not journaled")
	require.Equal(t, []string{"sighted/queued", "leased/fetch", "fetch_failed/no_peers"}, journal(t, st, store.Hash{3}))
	require.Equal(t, []string{"fetched/stored"}, journal(t, st, store.Hash{4}), "a sample's torrent is")
	got, err := st.Journal(ctx, store.Hash{4}, 1)
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"Heat.1995.1080p.BluRay","size":8589934592,"files":1,"discovery":"sample","queued_at":`+strconv.FormatInt(now.Unix(), 10)+`,"kind":"movie","kind_layer":"rules"}`, got[0].Detail)

	_, err = st.WantPeers(ctx, []store.Hash{{4}}, now)
	require.NoError(t, err)
	tasks, _, err := q.Lease(ctx, 1, 1, now, time.Minute)
	require.NoError(t, err)
	require.Equal(t, []store.Task{{Kind: store.TaskPeers, Hash: store.Hash{4}}}, tasks)
	require.NoError(t, s.result(ctx, 1, &sniffarrv1.Result{Infohash: infohash(4), Outcome: &sniffarrv1.Result_Lookup{Lookup: &sniffarrv1.Lookup{Peers: 3}}}))
	require.Equal(t, []string{"fetched/stored"}, journal(t, st, store.Hash{4}), "a peer lookup is not journaled, though the web UI asked for it")
}

func TestSightStoredFlags(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	st, err := store.New(ctx, db, store.Options{})
	require.NoError(t, err)
	q, err := store.NewTasks(ctx, db, store.TaskOptions{})
	require.NoError(t, err)
	now := time.Now()
	_, _, err = st.Insert(ctx, store.Torrent{Hash: store.Hash{1}, Name: "Heat.1995.1080p", Size: 8 << 30, Discovery: store.Sample, SeenAt: now})
	require.NoError(t, err)
	_, err = st.Reject(ctx, store.Hash{2}, rules.Adult, "xxx", 1<<30, now)
	require.NoError(t, err)
	s := &Service{Store: st, Tasks: q, Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now }}

	batch := []store.Sighting{{Hash: store.Hash{3}, Discovery: store.Sample}, {Hash: store.Hash{1}, Discovery: store.Sample},
		{Hash: store.Hash{2}, Discovery: store.Sample}, {Hash: store.Hash{1}, Discovery: store.Announce}}
	queued, known, stored, err := s.sight(ctx, batch)
	require.NoError(t, err)
	require.Equal(t, 1, queued)
	require.Equal(t, 3, known)
	require.Equal(t, []bool{false, true, false, true}, stored, "stored torrents are flagged, in order; rejected and queued are not")
	_, _, stored, err = s.sight(ctx, batch[:1])
	require.NoError(t, err)
	require.Equal(t, []bool{false}, stored, "a queued hash is known but not stored")
}

func TestResultStoresFeatures(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	st, err := store.New(ctx, db, store.Options{Languages: []string{"ru"}})
	require.NoError(t, err)
	q, err := store.NewTasks(ctx, db, store.TaskOptions{})
	require.NoError(t, err)
	langs, err := audio.Parse("ru")
	require.NoError(t, err)
	s := &Service{Store: st, Tasks: q, Log: slog.New(slog.DiscardHandler), Languages: langs}
	now := time.Now()
	_, _, err = q.Enqueue(ctx, []store.Sighting{{Hash: store.Hash{1}, Discovery: store.Sample}, {Hash: store.Hash{2}, Discovery: store.Sample}}, now)
	require.NoError(t, err)
	leased, _, err := q.Lease(ctx, 1, 2, now, time.Minute)
	require.NoError(t, err)
	require.Len(t, leased, 2)
	for i, name := range []string{"Heat.1995.1080p.BluRay.Rus.RHS", "The Office (US) S03E05 720p"} {
		require.NoError(t, s.result(ctx, 1, &sniffarrv1.Result{Infohash: infohash(byte(i + 1)),
			Outcome: &sniffarrv1.Result_Torrent{Torrent: &sniffarrv1.Torrent{Name: name, Size: 8 << 30,
				Files: []*sniffarrv1.File{{Path: name + ".mkv", Size: 8 << 30}}}}}))
	}

	movie, err := st.Get(ctx, store.Hash{1})
	require.NoError(t, err)
	require.Equal(t, "y:heat 1995", movie.TitleKey)
	require.Equal(t, 1995, movie.Year)
	require.Zero(t, movie.Season)
	require.Equal(t, "1080p", movie.Quality)
	require.Equal(t, []store.Audio{{Language: "ru", Marker: true, Mark: "RHS", Studios: []string{"Red Head Sound"}, Probability: new(1.0),
		Layer: store.LayerRules}}, movie.Audio, "the marker with its mark and studios")
	series, err := st.Get(ctx, store.Hash{2})
	require.NoError(t, err)
	require.Equal(t, "s:the office", series.TitleKey)
	require.Zero(t, series.Year)
	require.Equal(t, 3, series.Season)
	require.Equal(t, 5, series.Episode)
	require.Equal(t, "720p", series.Quality)
}

func TestFetchLookups(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	st, err := store.New(ctx, db, store.Options{})
	require.NoError(t, err)
	q, err := store.NewTasks(ctx, db, store.TaskOptions{})
	require.NoError(t, err)
	now := time.Now()
	_, _, err = st.Insert(ctx, store.Torrent{Hash: store.Hash{1}, Name: "Heat.1995.1080p", Size: 8 << 30, Discovery: store.Sample, SeenAt: now})
	require.NoError(t, err)
	_, err = st.WantPeers(ctx, []store.Hash{{1}}, now)
	require.NoError(t, err)
	_, _, err = q.Enqueue(ctx, []store.Sighting{{Hash: store.Hash{2}, Discovery: store.Arr}}, now)
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.Handle(sniffarrv1connect.NewIngestServiceHandler(&Service{Store: st, Tasks: q, Log: slog.New(slog.DiscardHandler)}))
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	stream := sniffarrv1connect.NewIngestServiceClient(srv.Client(), srv.URL).Fetch(ctx)
	t.Cleanup(func() { _ = stream.CloseRequest(); _ = stream.CloseResponse() })
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
		var n int
		require.NoError(t, db.R.QueryRowContext(ctx, "SELECT count(*) FROM task_queue WHERE kind = 'peers'").Scan(&n))
		return n == 0
	}, 5*time.Second, 10*time.Millisecond, "a lookup's result completes it")
}
