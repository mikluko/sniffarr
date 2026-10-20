package ingest

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/sketch"
	"github.com/mikluko/sniffarr/internal/store"
)

// peersBatch is how many sketches Peers merges in one transaction.
const peersBatch = 500

func (s *Service) HashKey(ctx context.Context, _ *connect.Request[sniffarrv1.HashKeyRequest]) (*connect.Response[sniffarrv1.HashKeyResponse], error) {
	k, err := s.Store.HashKey(ctx)
	if err != nil {
		return nil, s.internal(ctx, "hash key", err)
	}
	id, err := s.Store.NodeID(ctx)
	if err != nil {
		return nil, s.internal(ctx, "node id", err)
	}
	return connect.NewResponse(&sniffarrv1.HashKeyResponse{Key: k[:], NodeId: id[:]}), nil
}

func (s *Service) Peers(ctx context.Context, stream *connect.ClientStream[sniffarrv1.PeersRequest]) (*connect.Response[sniffarrv1.PeersResponse], error) {
	var res sniffarrv1.PeersResponse
	batch := make([]store.PeerSketch, 0, peersBatch)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		merged, dropped, err := s.Store.MergePeers(ctx, batch)
		if err != nil {
			return s.internal(ctx, "merge peers", err)
		}
		res.Merged += int64(merged)
		res.Dropped += int64(dropped)
		s.metrics().sketches.Add(ctx, int64(merged), sketchMerged)
		s.metrics().sketches.Add(ctx, int64(dropped), sketchDropped)
		batch = batch[:0]
		return nil
	}
	for stream.Receive() {
		m := stream.Msg()
		h, err := hash(m.GetInfohash())
		if err != nil {
			return nil, err
		}
		if _, err := sketch.Parse(m.GetSketch()); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("sketch of %x: %w", h, err))
		}
		batch = append(batch, store.PeerSketch{Hash: h, Hour: time.Unix(m.GetHour(), 0).UTC(), Sketch: m.GetSketch()})
		if len(batch) == peersBatch {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return connect.NewResponse(&res), nil
}
