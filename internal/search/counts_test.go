package search

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

func TestBrowserServesSnapshot(t *testing.T) {
	ctx := context.Background()
	b := browser(t)
	at := now.Add(-time.Minute)
	res, err := b.Stats(ctx, connect.NewRequest(&sniffarrv1.StatsRequest{Hours: 2}))
	require.NoError(t, err)
	require.Nil(t, res.Msg.GetCountsInterval(), "no interval without Counts")
	b.Counts = &Counts{Store: b.Store, Now: func() time.Time { return at }, Interval: 5 * time.Minute}

	res, err = b.Stats(ctx, connect.NewRequest(&sniffarrv1.StatsRequest{Hours: 2}))
	require.NoError(t, err)
	require.EqualValues(t, 5, res.Msg.GetTotal(), "computed on demand before any snapshot")
	require.Equal(t, at, res.Msg.GetComputedAt().AsTime())
	require.Equal(t, 5*time.Minute, res.Msg.GetCountsInterval().AsDuration())

	_, _, err = b.Store.Insert(ctx, store.Torrent{Hash: hash(6), Name: "Dune", Size: 1 << 30, Discovery: "sample", SeenAt: now})
	require.NoError(t, err)
	require.NoError(t, b.Store.AddTokens(ctx, now.Format(time.DateOnly), 50))
	require.NoError(t, b.Store.AddProbeTraffic(ctx, now.Format(time.DateOnly), store.ProbeTraffic{Rx: 1 << 30, Tx: 2 << 30}))

	res, err = b.Stats(ctx, connect.NewRequest(&sniffarrv1.StatsRequest{Hours: 2}))
	require.NoError(t, err)
	require.EqualValues(t, 5, res.Msg.GetTotal(), "the snapshot, not the store")
	require.Equal(t, []int64{2, 1}, res.Msg.GetHours(), "the snapshot's hour, 11:00, with the sighting a minute past it")
	require.EqualValues(t, 300, res.Msg.GetTokensUsedDay(), "read at the request")
	require.EqualValues(t, 4<<30, res.Msg.GetProbeBytesRxDay(), "read at the request")
	require.EqualValues(t, 3<<30, res.Msg.GetProbeBytesTxDay(), "read at the request")
	tor, err := b.Torrents(ctx, connect.NewRequest(&sniffarrv1.TorrentsRequest{}))
	require.NoError(t, err)
	require.EqualValues(t, 4, tor.Msg.GetTotal(), "the snapshot's total less the adult release, not a count")
	require.Len(t, tor.Msg.GetRows(), 5, "the page itself is live")
	tor, err = b.Torrents(ctx, connect.NewRequest(&sniffarrv1.TorrentsRequest{Kind: sniffarrv1.Kind_KIND_MOVIE}))
	require.NoError(t, err)
	require.EqualValues(t, 1, tor.Msg.GetTotal(), "a filtered page counts")
	home, err := b.Home(ctx, connect.NewRequest(&sniffarrv1.HomeRequest{}))
	require.NoError(t, err)
	require.EqualValues(t, 5, home.Msg.GetTotal())
	require.EqualValues(t, 4, home.Msg.GetNewDay())
	require.EqualValues(t, 1, home.Msg.GetPending())
	require.Equal(t, at, home.Msg.GetComputedAt().AsTime())

	at = now
	_, err = b.Counts.Refresh(ctx)
	require.NoError(t, err)
	res, err = b.Stats(ctx, connect.NewRequest(&sniffarrv1.StatsRequest{Hours: 2}))
	require.NoError(t, err)
	require.EqualValues(t, 6, res.Msg.GetTotal())
	require.Equal(t, now, res.Msg.GetComputedAt().AsTime())
}

func TestCountsRun(t *testing.T) {
	b := browser(t)
	c := &Counts{Store: b.Store, Log: slog.New(slog.DiscardHandler), Interval: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- c.Run(ctx) }()
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

// A restart is ready with the counts the last computation kept, at their own
// time, before it computes anew.
func TestCountsLoadKept(t *testing.T) {
	ctx := context.Background()
	b := browser(t)
	c := &Counts{Store: b.Store, Now: func() time.Time { return now }}
	_, err := c.Refresh(ctx)
	require.NoError(t, err)

	restarted := &Counts{Store: b.Store}
	require.False(t, restarted.Ready())
	ok, err := restarted.Load(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, restarted.Ready())
	s, err := restarted.Latest(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 5, s.Stats.Total)
	require.True(t, s.At.Equal(now), "the time they were computed, not of the load")

	ok, err = restarted.Load(ctx)
	require.NoError(t, err)
	require.False(t, ok, "a snapshot already there stays")

	empty := &Counts{Store: browser(t).Store}
	ok, err = empty.Load(ctx)
	require.NoError(t, err)
	require.False(t, ok, "nothing kept, nothing loaded")
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
