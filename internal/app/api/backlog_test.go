package api

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"sarak.sh/mikluko/libarr/database"
	"sarak.sh/mikluko/sniffarr/internal/bus"
	"sarak.sh/mikluko/sniffarr/internal/bus/bustest"
	"sarak.sh/mikluko/sniffarr/internal/orders"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

func TestMoveBacklog(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{Service: "sniffarr"})
	require.NoError(t, err)
	defer db.Close()
	s, err := store.New(ctx, db, store.Options{})
	require.NoError(t, err)
	later := time.Now().Add(time.Hour)
	for _, r := range []struct {
		kind               string
		hash               byte
		priority, attempts int
		notBefore          int64
	}{
		{store.TaskFetch, 3, store.PrioritySample, 0, 0},
		{store.TaskFetch, 1, store.PriorityArr, 2, 0},
		{store.TaskFetch, 4, store.PriorityAnnounce, 1, later.UnixMilli()},
		{store.TaskPeers, 2, store.PriorityRequest, 0, 0},
		{"unknown", 5, store.PriorityArr, 0, 0},
	} {
		h := store.Hash{r.hash}
		_, err := db.W.ExecContext(ctx, "INSERT INTO task_backlog (kind, infohash, priority, attempts, not_before) VALUES (?, ?, ?, ?, ?)",
			r.kind, h[:], r.priority, r.attempts, r.notBefore)
		require.NoError(t, err)
	}
	js, err := jetstream.New(bustest.Conn(t))
	require.NoError(t, err)
	fetches := orders.New(orders.Config{Stream: bus.StreamFetchOrders, Lanes: bus.FetchLanes})
	require.NoError(t, fetches.Start(ctx, js))
	log := slog.New(slog.DiscardHandler)

	require.NoError(t, moveBacklog(ctx, s, log, fetches))
	left, err := s.Backlog(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, left, "the backlog is moved and dropped")
	require.NoError(t, moveBacklog(ctx, s, log, fetches), "a second start finds none")

	got, err := fetches.Take(1, 10, store.PrioritySample)
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Equal(t, [3]byte{2, 1, 3}, [3]byte{got[0].Hash[0], got[1].Hash[0], got[2].Hash[0]}, "by priority, the lookup first")
	require.Equal(t, store.TaskPeers, got[0].Kind)
	require.Equal(t, 2, got[1].Attempt, "the failed attempts carry over")
	stream, err := js.Stream(ctx, bus.StreamFetchOrders)
	require.NoError(t, err)
	m, err := stream.GetLastMsgForSubject(ctx, bus.RetrySubject(bus.StreamFetchOrders, store.Hash{4}))
	require.NoError(t, err)
	require.Equal(t, "@at "+later.UTC().Format(time.RFC3339), m.Header.Get(jetstream.ScheduleHeader), "a fetch waiting out its backoff is scheduled")
	require.Equal(t, bus.SubjectFetchP2, m.Header.Get(jetstream.ScheduleTargetHeader))
	require.Equal(t, 1, bus.Attempt(m.Header))
}
