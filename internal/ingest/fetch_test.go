package ingest

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
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
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	st, err := store.New(ctx, db, store.Options{})
	require.NoError(t, err)
	q, err := store.NewQueue(ctx, db, store.QueueOptions{})
	require.NoError(t, err)
	s := &Service{Store: st, Queue: q, Log: slog.New(slog.DiscardHandler)}
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
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	q, err := store.NewQueue(ctx, db, store.QueueOptions{})
	require.NoError(t, err)
	now := time.Now()
	s := &Service{Queue: q, Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now }}
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
	require.Equal(t, []store.Hash{{2}}, again)
}

func TestSightStoredFlags(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	st, err := store.New(ctx, db, store.Options{})
	require.NoError(t, err)
	q, err := store.NewQueue(ctx, db, store.QueueOptions{})
	require.NoError(t, err)
	now := time.Now()
	_, _, err = st.Insert(ctx, store.Torrent{Hash: store.Hash{1}, Name: "Heat.1995.1080p", Size: 8 << 30, Discovery: store.Sample, SeenAt: now})
	require.NoError(t, err)
	_, err = st.Reject(ctx, store.Hash{2}, rules.Adult, now)
	require.NoError(t, err)
	s := &Service{Store: st, Queue: q, Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now }}

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
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	st, err := store.New(ctx, db, store.Options{Languages: []string{"ru"}})
	require.NoError(t, err)
	q, err := store.NewQueue(ctx, db, store.QueueOptions{})
	require.NoError(t, err)
	langs, err := audio.Parse("ru")
	require.NoError(t, err)
	s := &Service{Store: st, Queue: q, Log: slog.New(slog.DiscardHandler), Languages: langs}
	now := time.Now()
	_, _, err = q.Enqueue(ctx, []store.Sighting{{Hash: store.Hash{1}, Discovery: store.Sample}, {Hash: store.Hash{2}, Discovery: store.Sample}}, now)
	require.NoError(t, err)
	leased, _, err := q.Lease(ctx, 1, 2, now, time.Minute)
	require.NoError(t, err)
	require.Len(t, leased, 2)
	for i, name := range []string{"Heat.1995.1080p.BluRay.Rus", "The Office (US) S03E05 720p"} {
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
	require.Equal(t, []store.Audio{{Language: "ru", Marker: true, Mark: "Rus", Probability: new(1.0), Layer: store.LayerRules}}, movie.Audio,
		"the marker with its mark")
	series, err := st.Get(ctx, store.Hash{2})
	require.NoError(t, err)
	require.Equal(t, "s:the office", series.TitleKey)
	require.Zero(t, series.Year)
	require.Equal(t, 3, series.Season)
	require.Equal(t, 5, series.Episode)
	require.Equal(t, "720p", series.Quality)
}
