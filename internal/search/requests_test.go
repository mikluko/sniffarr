package search

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/store"
)

func byHash(b byte) *sniffarrv1.Releases {
	return &sniffarrv1.Releases{Of: &sniffarrv1.Releases_Infohash{Infohash: infohash(b)}}
}

func byWork(id string) *sniffarrv1.Releases {
	return &sniffarrv1.Releases{Of: &sniffarrv1.Releases_Work{Work: id}}
}

func TestBrowserRefreshPeers(t *testing.T) {
	ctx := context.Background()
	b := ranked(t)
	res, err := b.RefreshPeers(ctx, connect.NewRequest(&sniffarrv1.RefreshPeersRequest{Releases: byHash(7)}))
	require.NoError(t, err)
	require.Equal(t, int32(1), res.Msg.GetReleases())
	res, err = b.RefreshPeers(ctx, connect.NewRequest(&sniffarrv1.RefreshPeersRequest{Releases: byWork("m693134")}))
	require.NoError(t, err)
	require.Equal(t, int32(3), res.Msg.GetReleases(), "every release of the work")
	got, err := b.Store.TakePeerLookups(ctx, 10)
	require.NoError(t, err)
	require.ElementsMatch(t, []store.Hash{hash(7), hash(1), hash(2), hash(4)}, got)

	for _, tt := range []struct {
		name string
		r    *sniffarrv1.Releases
		code connect.Code
	}{
		{"not stored", byHash(99), connect.CodeNotFound},
		{"no work", byWork("m1"), connect.CodeNotFound},
		{"bad work", byWork("x1"), connect.CodeInvalidArgument},
		{"short infohash", &sniffarrv1.Releases{Of: &sniffarrv1.Releases_Infohash{Infohash: []byte{1}}}, connect.CodeInvalidArgument},
		{"none", nil, connect.CodeInvalidArgument},
	} {
		_, err := b.RefreshPeers(ctx, connect.NewRequest(&sniffarrv1.RefreshPeersRequest{Releases: tt.r}))
		require.Equal(t, tt.code, connect.CodeOf(err), tt.name)
	}
}

func TestBrowserRequestProbe(t *testing.T) {
	ctx := context.Background()
	b := ranked(t)
	require.NoError(t, b.Store.ProbeDone(ctx, hash(1), store.Probe{Outcome: store.ProbeUnknown}, now))
	res, err := b.RequestProbe(ctx, connect.NewRequest(&sniffarrv1.RequestProbeRequest{Releases: byWork("m693134")}))
	require.NoError(t, err)
	require.Equal(t, int32(3), res.Msg.GetReleases())
	c, ok, err := b.Store.NextProbe(ctx, now, false, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, hash(1), c.Hash, "a probed release again, most peers first")

	_, err = b.RequestProbe(ctx, connect.NewRequest(&sniffarrv1.RequestProbeRequest{Releases: byHash(99)}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}
