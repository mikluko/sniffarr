package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"connectrpc.com/connect"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/queue"
)

// releaseTimeout bounds freeing a finished stream's leases.
const releaseTimeout = 5 * time.Second

func (s *Service) Fetch(ctx context.Context, stream *connect.BidiStream[sniffarrv1.FetchRequest, sniffarrv1.FetchResponse]) error {
	owner := s.owners.Add(1)
	defer s.release(ctx, owner)

	msgs := make(chan *sniffarrv1.FetchRequest)
	errc := make(chan error, 1)
	go func() {
		for {
			m, err := stream.Receive()
			if err != nil {
				errc <- err
				return
			}
			select {
			case msgs <- m:
			case <-ctx.Done():
				return
			}
		}
	}()

	tick := time.NewTicker(or(s.Poll, DefaultPoll))
	defer tick.Stop()
	credit := 0
	for {
		var wake <-chan struct{}
		if credit > 0 {
			wake = s.wake.wait()
			n, err := s.lease(ctx, stream, owner, credit)
			if err != nil {
				return err
			}
			credit -= n
		}
		select {
		case <-ctx.Done():
			return nil
		case err := <-errc:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case m := <-msgs:
			switch k := m.GetKind().(type) {
			case *sniffarrv1.FetchRequest_Credit:
				n := int(k.Credit.GetN())
				if n <= 0 {
					return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("credit %d", n))
				}
				credit = min(credit+n, maxCredit)
			case *sniffarrv1.FetchRequest_Result:
				if err := s.result(ctx, owner, k.Result); err != nil {
					return err
				}
			default:
				return connect.NewError(connect.CodeInvalidArgument, errors.New("empty request"))
			}
		case <-wake:
		case <-tick.C:
		}
	}
}

// lease leases up to credit tasks to owner, sends them, and returns how many
// it sent.
func (s *Service) lease(ctx context.Context, stream *connect.BidiStream[sniffarrv1.FetchRequest, sniffarrv1.FetchResponse], owner int64, credit int) (int, error) {
	hashes, _, err := s.Queue.Lease(ctx, owner, credit, s.now(), or(s.Lease, DefaultLease))
	if err != nil {
		return 0, s.internal("lease", err)
	}
	for i, h := range hashes {
		if err := stream.Send(&sniffarrv1.FetchResponse{Task: &sniffarrv1.Task{Infohash: h[:]}}); err != nil {
			return i, err
		}
	}
	return len(hashes), nil
}

func (s *Service) result(ctx context.Context, owner int64, r *sniffarrv1.Result) error {
	h, err := hash(r.GetInfohash())
	if err != nil {
		return err
	}
	switch o := r.GetOutcome().(type) {
	case *sniffarrv1.Result_Torrent:
		return s.store(ctx, h, o.Torrent)
	case *sniffarrv1.Result_Failure:
		if o.Failure == sniffarrv1.Failure_FAILURE_UNSPECIFIED {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("unspecified failure"))
		}
		if _, err := s.Queue.Fail(ctx, queue.Hash(h), owner, s.now(), or(s.Attempts, DefaultAttempts), or(s.Backoff, DefaultBackoff)); err != nil {
			return s.internal("fail", err)
		}
		return nil
	}
	return connect.NewError(connect.CodeInvalidArgument, errors.New("result without outcome"))
}

// release frees the leases of a finished stream and wakes the others.
func (s *Service) release(ctx context.Context, owner int64) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	if err := s.Queue.Release(ctx, owner); err != nil {
		s.Log.Error("release leases", "owner", owner, "err", err)
		return
	}
	s.wake.notify()
}
