package ingest

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/database"
	"github.com/mikluko/sniffarr/internal/queue"
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
	q, err := queue.New(ctx, db, queue.Options{})
	require.NoError(t, err)
	s := &Service{Store: st, Queue: q, Log: slog.New(slog.DiscardHandler)}
	now := time.Now()
	_, _, err = q.Enqueue(ctx, []queue.Sighting{{Hash: queue.Hash{1}, Discovery: queue.Sample}, {Hash: queue.Hash{2}, Discovery: queue.Sample}}, now)
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
	require.Empty(t, again, "the failure's backoff holds 1, and 2 is done")
}
