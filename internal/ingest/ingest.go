// Package ingest implements sniffarr.v1.IngestService: it filters submitted
// torrents through the rules and stores or rejects them.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/rules"
	"github.com/mikluko/sniffarr/internal/store"
)

// MaxUnknown is the most infohashes one Unknown request may carry.
const MaxUnknown = 10000

// Service implements sniffarrv1connect.IngestServiceHandler.
type Service struct {
	Store *store.Store
	Log   *slog.Logger
	Now   func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) Unknown(ctx context.Context, req *connect.Request[sniffarrv1.UnknownRequest]) (*connect.Response[sniffarrv1.UnknownResponse], error) {
	in := req.Msg.GetInfohashes()
	if len(in) > MaxUnknown {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("more than %d infohashes", MaxUnknown))
	}
	hashes := make([]store.Hash, len(in))
	for i, b := range in {
		h, err := hash(b)
		if err != nil {
			return nil, err
		}
		hashes[i] = h
	}
	unknown, err := s.Store.Unknown(ctx, hashes, s.now())
	if err != nil {
		return nil, s.internal("unknown", err)
	}
	out := make([][]byte, len(unknown))
	for i, h := range unknown {
		out[i] = h[:]
	}
	return connect.NewResponse(&sniffarrv1.UnknownResponse{Infohashes: out}), nil
}

func (s *Service) Submit(ctx context.Context, stream *connect.ClientStream[sniffarrv1.SubmitRequest]) (*connect.Response[sniffarrv1.SubmitResponse], error) {
	var res sniffarrv1.SubmitResponse
	for stream.Receive() {
		t := stream.Msg().GetTorrent()
		if t == nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("no torrent"))
		}
		outcome, err := s.submit(ctx, t)
		if err != nil {
			return nil, err
		}
		switch outcome {
		case accepted:
			res.Accepted++
		case rejected:
			res.Rejected++
		case duplicate:
			res.Duplicate++
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	return connect.NewResponse(&res), nil
}

type outcome int

const (
	accepted outcome = iota
	rejected
	duplicate
)

func (s *Service) submit(ctx context.Context, t *sniffarrv1.Torrent) (outcome, error) {
	h, err := hash(t.GetInfohash())
	if err != nil {
		return 0, err
	}
	if t.GetName() == "" {
		return 0, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("torrent %x has no name", h))
	}
	seen := s.now()
	if at := t.GetSeenAt(); at != nil && at.IsValid() && at.AsTime().Before(seen) {
		seen = at.AsTime()
	}
	known, err := s.Store.Seen(ctx, h, seen)
	if err != nil {
		return 0, s.internal("seen", err)
	}
	if known {
		return duplicate, nil
	}
	files := make([]rules.File, len(t.GetFiles()))
	for i, f := range t.GetFiles() {
		files[i] = rules.File{Path: f.GetPath(), Size: f.GetSize()}
	}
	if len(files) == 0 {
		files = []rules.File{{Path: t.GetName(), Size: t.GetSize()}}
	}
	if reason := rules.Check(t.GetName(), t.GetSize(), files); reason != rules.Accept {
		ok, err := s.Store.Reject(ctx, h, reason, seen)
		if err != nil {
			return 0, s.internal("reject", err)
		}
		if !ok {
			return duplicate, nil
		}
		return rejected, nil
	}
	ok, err := s.Store.Insert(ctx, store.Torrent{
		Hash:          h,
		Name:          t.GetName(),
		Size:          t.GetSize(),
		Files:         files,
		Discovery:     discovery(t.GetSource()),
		SeenAt:        seen,
		RussianMarker: rules.RussianMarker(t.GetName(), files),
	})
	if err != nil {
		return 0, s.internal("insert", err)
	}
	if !ok {
		return duplicate, nil
	}
	return accepted, nil
}

func discovery(s sniffarrv1.Source) string {
	switch s {
	case sniffarrv1.Source_SOURCE_SAMPLE:
		return "sample"
	case sniffarrv1.Source_SOURCE_ANNOUNCE:
		return "announce"
	}
	return ""
}

func hash(b []byte) (store.Hash, error) {
	if len(b) != len(store.Hash{}) {
		return store.Hash{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("infohash of %d bytes, want 20", len(b)))
	}
	return store.Hash(b), nil
}

func (s *Service) internal(op string, err error) error {
	s.Log.Error(op, "err", err)
	return connect.NewError(connect.CodeInternal, fmt.Errorf("%s failed", op))
}
