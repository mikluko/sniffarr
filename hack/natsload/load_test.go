package main

import (
	"log/slog"
	"testing"
	"time"

	"github.com/google/pprof/profile"
	"github.com/stretchr/testify/require"

	"sarak.sh/mikluko/sniffarr/internal/bus"
)

func openTest(t *testing.T, dir string) *node {
	t.Helper()
	n, _, err := open(t.Context(), dir, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	return n
}

func TestDrain(t *testing.T) {
	dir := t.TempDir()
	n := openTest(t, dir)
	const orders = 2000
	require.NoError(t, fill(t.Context(), n, orders, 64, []int{1, 4, 15, 80}))
	left, err := n.msgs(t.Context())
	require.NoError(t, err)
	require.Equal(t, uint64(orders), left)

	st, err := drain(t.Context(), n, drainSpec{workers: 2, batch: 16, prios: bus.Priorities, nak: 20, nakDelay: time.Hour, stall: 5 * time.Second})
	require.NoError(t, err)
	require.False(t, st.Stalled)
	require.Equal(t, uint64(orders), st.Total)
	require.Equal(t, int64(orders), st.Delivered)
	require.Equal(t, int64(orders), st.Acked+st.Naked)
	require.InDelta(t, orders/5, st.Naked, orders/20)
	require.Equal(t, int(st.Naked), st.AckPending, "naked orders wait out their delay as pending acks")
	_, err = n.close(t.Context())
	require.NoError(t, err)

	n = openTest(t, dir)
	left, err = n.msgs(t.Context())
	require.NoError(t, err)
	require.Equal(t, uint64(st.Naked), left)
	_, err = n.close(t.Context())
	require.NoError(t, err)
}

func TestDrainStallsOnNakedPending(t *testing.T) {
	n := openTest(t, t.TempDir())
	require.NoError(t, fill(t.Context(), n, 1000, 64, []int{0, 0, 0, 1}))
	st, err := drain(t.Context(), n, drainSpec{workers: 2, batch: 4, prios: bus.Priorities, maxAckPending: 8, nak: 50, nakDelay: time.Hour, stall: time.Second})
	require.NoError(t, err)
	require.True(t, st.Stalled, "MaxAckPending fills with naked orders")
	require.Less(t, st.Delivered, int64(100))
	require.Equal(t, 8, st.AckPending)
	_, err = n.close(t.Context())
	require.NoError(t, err)
}

func TestNaked(t *testing.T) {
	k := 0
	for seq := range uint64(10000) {
		if naked(seq, 5) {
			k++
		}
	}
	require.InDelta(t, 500, k, 100)
	require.False(t, naked(1, 0))
	require.True(t, naked(1, 100))
}

func TestShares(t *testing.T) {
	fn := func(name string) *profile.Location {
		return &profile.Location{Line: []profile.Line{{Function: &profile.Function{Name: name}}}}
	}
	check := fn("github.com/nats-io/nats-server/v2/server.(*consumer).checkPending")
	remove := fn("github.com/nats-io/nats-server/v2/server.(*fileStore).removeMsg")
	block := fn("github.com/nats-io/nats-server/v2/server.(*fileStore).removeMsgBlock")
	prof := &profile.Profile{
		SampleType: []*profile.ValueType{{Type: "samples"}, {Type: "cpu"}},
		Sample: []*profile.Sample{
			{Location: []*profile.Location{check, check}, Value: []int64{1, 30}},
			{Location: []*profile.Location{block, remove}, Value: []int64{1, 20}},
			{Location: []*profile.Location{fn("runtime.mallocgc")}, Value: []int64{1, 50}},
		},
	}
	got := shares(prof)
	require.InDelta(t, 30, got["checkPending"], 1e-9, "a recursive frame counts once")
	require.InDelta(t, 20, got["removeMsg"], 1e-9)
	require.InDelta(t, 20, got["removeMsgBlock"], 1e-9)
	require.Zero(t, got["loadMsgs"])
}

func TestParse(t *testing.T) {
	o, err := parse([]string{"-weights", "0,0,1,1", "-batches", "4", "-naks", "0,50"}, nil)
	require.NoError(t, err)
	require.Equal(t, []int{0, 0, 1, 1}, o.weights)
	require.Equal(t, []int{4}, o.batches)
	require.Equal(t, []int{0, 50}, o.naks)
	_, err = parse([]string{"-weights", "1,2"}, nil)
	require.Error(t, err)
	_, err = parse([]string{"-naks", "-1"}, nil)
	require.Error(t, err)
}
