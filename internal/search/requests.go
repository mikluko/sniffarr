package search

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/classify"
	"sarak.sh/mikluko/sniffarr/internal/orders"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

func (b *Browser) RefreshPeers(ctx context.Context, req *connect.Request[sniffarrv1.RefreshPeersRequest]) (*connect.Response[sniffarrv1.RefreshPeersResponse], error) {
	if b.Fetches == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no fetch orders"))
	}
	n, err := b.request(ctx, req.Msg.GetReleases(), "refresh peers", func(ctx context.Context, hashes []store.Hash, at time.Time) (int, int, error) {
		stored, err := b.Store.WantPeers(ctx, hashes, at)
		if err != nil {
			return 0, 0, err
		}
		l, _ := b.Fetches.Lane(store.TaskPeers, store.PriorityRequest)
		items := make([]orders.Item, len(stored))
		for i, h := range stored {
			items[i] = orders.Item{Hash: h}
		}
		if _, err := b.Fetches.Publish(ctx, l, items); err != nil {
			return 0, 0, err
		}
		return len(stored), len(stored), nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&sniffarrv1.RefreshPeersResponse{Releases: int32(n)}), nil
}

func (b *Browser) RequestProbe(ctx context.Context, req *connect.Request[sniffarrv1.RequestProbeRequest]) (*connect.Response[sniffarrv1.RequestProbeResponse], error) {
	if !b.Probes {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("probes are off"))
	}
	n, err := b.request(ctx, req.Msg.GetReleases(), "request probe", b.Store.RequestProbe)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&sniffarrv1.RequestProbeResponse{Releases: int32(n)}), nil
}

func (b *Browser) RequestMatch(ctx context.Context, req *connect.Request[sniffarrv1.RequestMatchRequest]) (*connect.Response[sniffarrv1.RequestMatchResponse], error) {
	if b.Matcher == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("TMDB is off"))
	}
	lang, err := language(req.Msg.GetLang())
	if err != nil {
		return nil, err
	}
	hashes, err := b.releases(ctx, req.Msg.GetReleases())
	if err != nil {
		return nil, err
	}
	titles, err := b.Store.ReleaseTitles(ctx, hashes)
	if err != nil {
		return nil, b.internal(ctx, "release titles", err)
	}
	if titles.Stored == 0 {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no stored release"))
	}
	due, err := b.Matcher.Matchable(ctx, titles.Keys)
	if err != nil {
		return nil, b.internal(ctx, "matchable titles", err)
	}
	for _, k := range due {
		if err := b.Matcher.Match(ctx, k, store.TriggerRequest); err != nil {
			return nil, b.matchFailed(ctx, k.Key, err)
		}
	}
	if titles, err = b.Store.ReleaseTitles(ctx, hashes); err != nil {
		return nil, b.internal(ctx, "release titles", err)
	}
	sums, err := b.Store.Summaries(ctx, titles.Works)
	if err != nil {
		return nil, b.internal(ctx, "work summaries", err)
	}
	res := &sniffarrv1.RequestMatchResponse{Titles: int32(len(due))}
	for _, id := range titles.Works {
		if s, ok := sums[id]; ok {
			res.Works = append(res.Works, summary(s, lang))
		}
	}
	return connect.NewResponse(res), nil
}

// matchFailed is the error of RequestMatch for a lookup of key that failed
// with err: RESOURCE_EXHAUSTED for the classifier's budget, else
// UNAVAILABLE.
func (b *Browser) matchFailed(ctx context.Context, key string, err error) error {
	if errors.Is(err, classify.ErrBudget) {
		return connect.NewError(connect.CodeResourceExhausted, err)
	}
	if ctx.Err() != nil {
		return connect.NewError(connect.CodeCanceled, ctx.Err())
	}
	b.Log.WarnContext(ctx, "requested match", "key", key, "err", err)
	return connect.NewError(connect.CodeUnavailable, errors.New("TMDB lookup failed"))
}

// request records a request of op for the stored releases r names, and
// returns how many record took, or NOT_FOUND for no stored release. record
// returns how many it took and how many of the hashes are stored.
func (b *Browser) request(ctx context.Context, r *sniffarrv1.Releases, op string, record func(context.Context, []store.Hash, time.Time) (int, int, error)) (int, error) {
	hashes, err := b.releases(ctx, r)
	if err != nil {
		return 0, err
	}
	n, stored, err := record(ctx, hashes, b.now())
	if err != nil {
		return 0, b.internal(ctx, op, err)
	}
	if stored == 0 {
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
