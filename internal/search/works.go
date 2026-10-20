package search

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/store"
)

const (
	// DefaultTopLimit is the number of entries Top returns when asked for
	// none in particular.
	DefaultTopLimit = 20
	// PageWorks is the most works Page returns.
	PageWorks = 10
)

var windows = map[sniffarrv1.Window]store.Window{
	sniffarrv1.Window_WINDOW_UNSPECIFIED: store.WindowWeek,
	sniffarrv1.Window_WINDOW_DAY:         store.WindowDay,
	sniffarrv1.Window_WINDOW_WEEK:        store.WindowWeek,
	sniffarrv1.Window_WINDOW_MONTH:       store.WindowMonth,
}

var voices = map[sniffarrv1.Voice]store.Voice{
	sniffarrv1.Voice_VOICE_UNSPECIFIED: store.VoiceAny,
	sniffarrv1.Voice_VOICE_LIKELY:      store.VoiceLikely,
	sniffarrv1.Voice_VOICE_MARKER:      store.VoiceMarker,
}

var sorts = map[sniffarrv1.Sort]string{
	sniffarrv1.Sort_SORT_UNSPECIFIED: "",
	sniffarrv1.Sort_SORT_PEERS:       store.SortPeers,
	sniffarrv1.Sort_SORT_LAST_SEEN:   store.SortLastSeen,
	sniffarrv1.Sort_SORT_SIZE:        store.SortSize,
}

// windowVoice reads a window and a voice.
func windowVoice(w sniffarrv1.Window, v sniffarrv1.Voice) (store.Window, store.Voice, error) {
	window, ok := windows[w]
	if !ok {
		return store.Window{}, "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown window %d", w))
	}
	voice, ok := voices[v]
	if !ok {
		return store.Window{}, "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown voice %d", v))
	}
	return window, voice, nil
}

func (b *Browser) Home(ctx context.Context, _ *connect.Request[sniffarrv1.HomeRequest]) (*connect.Response[sniffarrv1.HomeResponse], error) {
	h, err := b.Store.Home(ctx, b.now())
	if err != nil {
		b.Log.ErrorContext(ctx, "home", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("home failed"))
	}
	return connect.NewResponse(&sniffarrv1.HomeResponse{Total: h.Total, NewDay: h.NewDay, Pending: h.Pending}), nil
}

func (b *Browser) Top(ctx context.Context, req *connect.Request[sniffarrv1.TopRequest]) (*connect.Response[sniffarrv1.TopResponse], error) {
	m := req.Msg
	kind, ok := kinds[m.GetKind()]
	if !ok || !slices.Contains(store.TopKinds, kind) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("kind %v has no top", m.GetKind()))
	}
	window, voice, err := windowVoice(m.GetWindow(), m.GetVoice())
	if err != nil {
		return nil, err
	}
	limit, offset := int(m.GetLimit()), int(m.GetOffset())
	switch {
	case limit < 0 || offset < 0:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("negative limit or offset"))
	case limit == 0:
		limit = DefaultTopLimit
	case limit > store.TopSize:
		limit = store.TopSize
	}
	entries, total, at, err := b.Store.Top(ctx, kind, window, voice, limit, offset)
	if err != nil {
		b.Log.ErrorContext(ctx, "top", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("top failed"))
	}
	res := &sniffarrv1.TopResponse{Total: total}
	if !at.IsZero() {
		res.ComputedAt = timestamppb.New(at)
	}
	for _, e := range entries {
		pe := &sniffarrv1.TopEntry{Rank: int32(e.Rank), Work: work(e.Work), Peers: e.Peers}
		if e.Delta != nil {
			pe.Delta = new(float32(*e.Delta))
		}
		res.Entries = append(res.Entries, pe)
	}
	return connect.NewResponse(res), nil
}

func (b *Browser) Work(ctx context.Context, req *connect.Request[sniffarrv1.WorkRequest]) (*connect.Response[sniffarrv1.WorkResponse], error) {
	m := req.Msg
	if m.GetKey() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("empty key"))
	}
	window, voice, err := windowVoice(m.GetWindow(), m.GetVoice())
	if err != nil {
		return nil, err
	}
	v, err := b.Store.Work(ctx, m.GetKey(), window, voice, b.now())
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if err != nil {
		b.Log.ErrorContext(ctx, "work", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("work failed"))
	}
	res := &sniffarrv1.WorkResponse{Work: work(v.Work), Peers: v.Peers, Rank: int32(v.Rank)}
	if v.Delta != nil {
		res.Delta = new(float32(*v.Delta))
	}
	for _, r := range v.Releases {
		res.Releases = append(res.Releases, &sniffarrv1.WorkRelease{Release: release(r.Release), Peers: r.Peers})
	}
	return connect.NewResponse(res), nil
}

func workRef(w store.Work) *sniffarrv1.WorkRef {
	ref := &sniffarrv1.WorkRef{Key: w.Key, Title: w.Title, Year: int32(w.Year), Season: int32(w.Season), Rank: int32(w.Rank)}
	for k, name := range kinds {
		if name == w.Kind {
			ref.Kind = k
		}
	}
	return ref
}

func work(w store.Work) *sniffarrv1.Work {
	out := &sniffarrv1.Work{Ref: workRef(w), Releases: w.Releases, FirstSeen: timestamppb.New(w.FirstSeen)}
	for _, a := range w.Audio {
		pa := &sniffarrv1.Audio{Language: a.Language, Marker: a.Marker, ScriptHint: a.ScriptHint}
		if a.Probability != nil {
			pa.Probability = new(float32(*a.Probability))
		}
		out.Audio = append(out.Audio, pa)
	}
	for _, s := range w.Sources {
		out.Sources = append(out.Sources, sources[s])
	}
	return out
}
