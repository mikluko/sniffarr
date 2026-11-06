package search

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/store"
)

func (b *Browser) RefreshPeers(ctx context.Context, req *connect.Request[sniffarrv1.RefreshPeersRequest]) (*connect.Response[sniffarrv1.RefreshPeersResponse], error) {
	n, err := b.request(ctx, req.Msg.GetReleases(), "refresh peers", b.Store.WantPeers)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&sniffarrv1.RefreshPeersResponse{Releases: int32(n)}), nil
}

func (b *Browser) RequestProbe(ctx context.Context, req *connect.Request[sniffarrv1.RequestProbeRequest]) (*connect.Response[sniffarrv1.RequestProbeResponse], error) {
	n, err := b.request(ctx, req.Msg.GetReleases(), "request probe", b.Store.RequestProbe)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&sniffarrv1.RequestProbeResponse{Releases: int32(n)}), nil
}

// request records a request of op for the stored releases r names, and
// returns how many there are, or NOT_FOUND for none.
func (b *Browser) request(ctx context.Context, r *sniffarrv1.Releases, op string, record func(context.Context, []store.Hash, time.Time) (int, error)) (int, error) {
	hashes, err := b.releases(ctx, r)
	if err != nil {
		return 0, err
	}
	n, err := record(ctx, hashes, b.now())
	if err != nil {
		return 0, b.internal(ctx, op, err)
	}
	if n == 0 {
		return 0, connect.NewError(connect.CodeNotFound, errors.New("no stored release"))
	}
	return n, nil
}

// releases returns the hashes r names: its infohash, or those of its work's
// stored releases.
func (b *Browser) releases(ctx context.Context, r *sniffarrv1.Releases) ([]store.Hash, error) {
	switch of := r.GetOf().(type) {
	case *sniffarrv1.Releases_Infohash:
		if len(of.Infohash) != len(store.Hash{}) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("infohash of %d bytes", len(of.Infohash)))
		}
		return []store.Hash{store.Hash(of.Infohash)}, nil
	case *sniffarrv1.Releases_Work:
		id, err := b.work(ctx, of.Work)
		if err != nil {
			return nil, err
		}
		hashes, err := b.Store.WorkHashes(ctx, id)
		if err != nil {
			return nil, b.internal(ctx, "work releases", err)
		}
		return hashes, nil
	}
	return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("no releases named"))
}
