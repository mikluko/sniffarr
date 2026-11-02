package search

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/store"
)

func TestBrowserServesSnapshot(t *testing.T) {
	ctx := context.Background()
	b := browser(t)
	at := now.Add(-time.Minute)
	b.Counts = &Counts{Store: b.Store, Now: func() time.Time { return at }}

	res, err := b.Stats(ctx, connect.NewRequest(&sniffarrv1.StatsRequest{Days: 2}))
	require.NoError(t, err)
	require.EqualValues(t, 5, res.Msg.GetTotal(), "computed on demand before any snapshot")
	require.Equal(t, at, res.Msg.GetComputedAt().AsTime())

	_, _, err = b.Store.Insert(ctx, store.Torrent{Hash: hash(6), Name: "Dune", Size: 1 << 30, Discovery: "sample", SeenAt: now})
	require.NoError(t, err)
	require.NoError(t, b.Store.AddTokens(ctx, now.Format(time.DateOnly), 50))
	require.NoError(t, b.Store.AddProbeTraffic(ctx, now.Format(time.DateOnly), store.ProbeTraffic{Rx: 1 << 30, Tx: 2 << 30}))

	res, err = b.Stats(ctx, connect.NewRequest(&sniffarrv1.StatsRequest{Days: 2}))
	require.NoError(t, err)
	require.EqualValues(t, 5, res.Msg.GetTotal(), "the snapshot, not the store")
	require.Equal(t, []int64{4, 1}, res.Msg.GetDays())
	require.EqualValues(t, 300, res.Msg.GetTokensUsedDay(), "read at the request")
	require.EqualValues(t, 4<<30, res.Msg.GetProbeBytesRxDay(), "read at the request")
	require.EqualValues(t, 3<<30, res.Msg.GetProbeBytesTxDay(), "read at the request")
	home, err := b.Home(ctx, connect.NewRequest(&sniffarrv1.HomeRequest{}))
	require.NoError(t, err)
	require.EqualValues(t, 5, home.Msg.GetTotal())
	require.EqualValues(t, 4, home.Msg.GetNewDay())
	require.EqualValues(t, 1, home.Msg.GetPending())
	require.Equal(t, at, home.Msg.GetComputedAt().AsTime())

	at = now
	_, err = b.Counts.Refresh(ctx)
	require.NoError(t, err)
	res, err = b.Stats(ctx, connect.NewRequest(&sniffarrv1.StatsRequest{Days: 2}))
	require.NoError(t, err)
	require.EqualValues(t, 6, res.Msg.GetTotal())
	require.Equal(t, now, res.Msg.GetComputedAt().AsTime())
}

func TestCountsRun(t *testing.T) {
	b := browser(t)
	c := &Counts{Store: b.Store, Log: slog.New(slog.DiscardHandler)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- c.Run(ctx, 10*time.Millisecond) }()
	defer func() {
		cancel()
		require.NoError(t, <-done)
	}()

	require.Eventually(t, func() bool { return c.latest.Load() != nil }, time.Second, time.Millisecond, "computed at start")
	first := c.latest.Load()
	require.EqualValues(t, 5, first.Stats.Total)
	_, _, err := b.Store.Insert(ctx, store.Torrent{Hash: hash(6), Name: "Dune", Size: 1 << 30, Discovery: "sample", SeenAt: now})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return c.latest.Load().Stats.Total == 6 }, time.Second, time.Millisecond, "recomputed on the interval")
	require.True(t, c.latest.Load().At.After(first.At))
}

func TestCountsLatestComputesOnce(t *testing.T) {
	b := browser(t)
	c := &Counts{Store: b.Store}
	got := make([]*Snapshot, 8)
	var wg sync.WaitGroup
	for i := range got {
		wg.Go(func() {
			s, err := c.Latest(context.Background())
			require.NoError(t, err)
			got[i] = s
		})
	}
	wg.Wait()
	for _, s := range got {
		require.Same(t, got[0], s)
	}
}
