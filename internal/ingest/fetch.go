package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"connectrpc.com/connect"

	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/orders"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

// resultTimeout bounds recording one received result.
const resultTimeout = 5 * time.Second

func (s *Service) Fetch(ctx context.Context, stream *connect.BidiStream[sniffarrv1.FetchRequest, sniffarrv1.FetchResponse]) error {
	owner := s.owners.Add(1)
	defer s.Fetches.Release(owner)
	r := newReceiver(stream)
	defer r.stop()

	tick := time.NewTicker(or(s.Poll, DefaultPoll))
	defer tick.Stop()
	credit := 0
	for {
		var wake <-chan struct{}
		if credit > 0 {
			wake = s.Fetches.Wait()
			n, err := s.hand(ctx, stream, owner, credit)
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

// hand takes up to credit orders for owner, sends them as tasks, and returns
// how many it sent. A failure to take or to journal them is logged.
func (s *Service) hand(ctx context.Context, stream *connect.BidiStream[sniffarrv1.FetchRequest, sniffarrv1.FetchResponse], owner int64, credit int) (int, error) {
	taken, err := s.Fetches.Take(owner, credit, store.PrioritySample)
	if err != nil {
		s.Log.ErrorContext(ctx, "take orders", "err", err)
	}
	if err := s.journalLeases(ctx, taken); err != nil {
		s.Log.ErrorContext(ctx, "journal leases", "err", err)
	}
	for i, o := range taken {
		if err := stream.Send(&sniffarrv1.FetchResponse{Task: &sniffarrv1.Task{Infohash: o.Hash[:], Peers: o.Kind == store.TaskPeers}}); err != nil {
			return i, err
		}
	}
	return len(taken), nil
}

// journalLeases journals the first hand-out of each fetch order of taken.
func (s *Service) journalLeases(ctx context.Context, taken []*orders.Order) error {
	now := s.now()
	var ls []store.Lease
	for _, o := range taken {
		if o.Kind == store.TaskFetch && o.Leases == 1 {
			ls = append(ls, store.Lease{Hash: o.Hash, Priority: o.Priority, Attempt: o.Attempt + 1, Until: now.Add(s.Fetches.Lease())})
		}
	}
	return s.Store.RecordLeases(ctx, ls, now)
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
	case *sniffarrv1.Result_Lookup:
		s.metrics().results.Add(ctx, 1, outcome("lookup"))
		s.Fetches.Finished(store.TaskPeers, s.now())
		if ord := s.Fetches.Done(store.TaskPeers, h); ord != nil {
			if err := s.Fetches.Ack(ctx, ord); err != nil {
				return s.internal(ctx, "ack lookup", err)
			}
		}
		return nil
	case *sniffarrv1.Result_Failure:
		switch o.Failure {
		case sniffarrv1.Failure_FAILURE_NO_PEERS:
			s.metrics().results.Add(ctx, 1, outcome("no_peers"))
			return s.failed(ctx, owner, h, true)
		case sniffarrv1.Failure_FAILURE_TIMEOUT:
			s.metrics().results.Add(ctx, 1, outcome("timeout"))
			return s.failed(ctx, owner, h, false)
		}
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("failure %v", o.Failure))
	}
	return connect.NewError(connect.CodeInvalidArgument, errors.New("result without outcome"))
}

// failed records a failed fetch of h held by owner: one that found no peers
// keeps h dead for NoPeersFor at once, and a timeout is retried after
// Backoff, doubled for each attempt before, until Attempts have failed and h
// is kept dead for DeadFor. A failure of an order owner does not hold, or of
// a hash stored, rejected or dead by now, which a second order of it met, is
// dropped.
func (s *Service) failed(ctx context.Context, owner int64, h store.Hash, noPeers bool) error {
	o := s.Fetches.DoneBy(store.TaskFetch, h, owner)
	if o == nil {
		return nil
	}
	now := s.now()
	s.Fetches.Finished(store.TaskFetch, now)
	held, err := s.Store.Screen(ctx, []store.Hash{h}, now)
	if err != nil {
		s.Fetches.Nak(o)
		return s.internal(ctx, "screen", err)
	}
	if held[h] != (store.Screened{}) {
		return s.ack(ctx, o)
	}
	f := store.FetchFailure{Hash: h, Priority: o.Priority, Attempt: o.Attempt + 1, Leases: o.Leases}
	switch {
	case noPeers:
		f.Outcome, f.Attempt, f.DeadUntil = "no_peers", 0, now.Add(or(s.NoPeersFor, store.DefaultNoPeersFor))
	case f.Attempt >= or(s.Attempts, DefaultAttempts):
		f.Outcome, f.DeadUntil = "dropped", now.Add(or(s.DeadFor, store.DefaultDeadFor))
	default:
		f.Outcome, f.RetryAt = "timeout", now.Add(or(s.Backoff, DefaultBackoff)<<(f.Attempt-1))
	}
	if err := s.Store.RecordFetchFailure(ctx, f, now); err != nil {
		s.Fetches.Nak(o)
		return s.internal(ctx, "record failure", err)
	}
	if noPeers {
		s.metrics().evictions.Add(ctx, 1, evictionAttr("no_peers"))
	}
	if f.RetryAt.IsZero() {
		return s.ack(ctx, o)
	}
	if err := s.Fetches.Retry(ctx, o, o.Lane, f.RetryAt); err != nil {
		return s.internal(ctx, "retry", err)
	}
	return nil
}

// ack acknowledges the order o.
func (s *Service) ack(ctx context.Context, o *orders.Order) error {
	if err := s.Fetches.Ack(ctx, o); err != nil {
		return s.internal(ctx, "ack", err)
	}
	return nil
}
