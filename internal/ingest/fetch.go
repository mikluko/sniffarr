package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"connectrpc.com/connect"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/store"
)

// releaseTimeout bounds freeing a finished stream's leases.
const releaseTimeout = 5 * time.Second

// resultTimeout bounds recording one received result.
const resultTimeout = 5 * time.Second

func (s *Service) Fetch(ctx context.Context, stream *connect.BidiStream[sniffarrv1.FetchRequest, sniffarrv1.FetchResponse]) error {
	owner := s.owners.Add(1)
	defer s.release(ctx, owner)
	r := newReceiver(stream)
	defer r.stop()

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
		case err := <-r.errc:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case m := <-r.msgs:
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
			r.next <- struct{}{}
		case <-wake:
		case <-tick.C:
		}
	}
}

// receiver receives a stream's messages in a goroutine of its own, one at a
// time: it calls Receive again only after the previous message is handled,
// so that the handler can return between messages without a Receive in
// progress.
type receiver struct {
	msgs   chan *sniffarrv1.FetchRequest
	next   chan struct{}
	errc   chan error
	quit   chan struct{}
	exited chan struct{}
}

func newReceiver(stream *connect.BidiStream[sniffarrv1.FetchRequest, sniffarrv1.FetchResponse]) *receiver {
	r := &receiver{
		msgs:   make(chan *sniffarrv1.FetchRequest),
		next:   make(chan struct{}),
		errc:   make(chan error, 1),
		quit:   make(chan struct{}),
		exited: make(chan struct{}),
	}
	go func() {
		defer close(r.exited)
		for {
			m, err := stream.Receive()
			if err != nil {
				r.errc <- err
				return
			}
			select {
			case r.msgs <- m:
			case <-r.quit:
				return
			}
			select {
			case <-r.next:
			case <-r.quit:
				return
			}
		}
	}()
	return r
}

// stop ends the goroutine and waits for it. A Receive in progress ends when
// the request does, which a cancelled context or a broken stream brings
// about.
func (r *receiver) stop() {
	close(r.quit)
	<-r.exited
}

// lease leases up to credit tasks to owner, sends them, and returns how many
// it sent. A failure to lease is logged and leases nothing.
func (s *Service) lease(ctx context.Context, stream *connect.BidiStream[sniffarrv1.FetchRequest, sniffarrv1.FetchResponse], owner int64, credit int) (int, error) {
	hashes, expired, err := s.Queue.Lease(ctx, owner, credit, s.now(), or(s.Lease, DefaultLease))
	if err != nil {
		s.Log.ErrorContext(ctx, "lease", "err", err)
		return 0, nil
	}
	s.metrics().expiries.Add(ctx, int64(expired))
	for i, h := range hashes {
		if err := stream.Send(&sniffarrv1.FetchResponse{Task: &sniffarrv1.Task{Infohash: h[:]}}); err != nil {
			return i, err
		}
	}
	return len(hashes), nil
}

// result records a received result, to completion even when the stream ends
// meanwhile.
func (s *Service) result(ctx context.Context, owner int64, r *sniffarrv1.Result) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resultTimeout)
	defer cancel()
	h, err := hash(r.GetInfohash())
	if err != nil {
		return err
	}
	switch o := r.GetOutcome().(type) {
	case *sniffarrv1.Result_Torrent:
		s.metrics().results.Add(ctx, 1, outcome("torrent"))
		return s.store(ctx, h, o.Torrent)
	case *sniffarrv1.Result_Failure:
		switch o.Failure {
		case sniffarrv1.Failure_FAILURE_NO_PEERS:
			s.metrics().results.Add(ctx, 1, outcome("no_peers"))
			if err := s.Queue.NoPeers(ctx, store.Hash(h), owner, s.now()); err != nil {
				return s.internal(ctx, "no peers", err)
			}
			return nil
		case sniffarrv1.Failure_FAILURE_TIMEOUT:
			s.metrics().results.Add(ctx, 1, outcome("timeout"))
			if _, err := s.Queue.Fail(ctx, store.Hash(h), owner, s.now(), or(s.Attempts, DefaultAttempts), or(s.Backoff, DefaultBackoff)); err != nil {
				return s.internal(ctx, "fail", err)
			}
			return nil
		}
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("failure %v", o.Failure))
	}
	return connect.NewError(connect.CodeInvalidArgument, errors.New("result without outcome"))
}

// release frees the leases of a finished stream and wakes the others.
func (s *Service) release(ctx context.Context, owner int64) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	if err := s.Queue.Release(ctx, owner); err != nil {
		s.Log.ErrorContext(ctx, "release leases", "owner", owner, "err", err)
		return
	}
	s.wake.notify()
}
