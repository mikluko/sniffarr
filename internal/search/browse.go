package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"time"

	"connectrpc.com/connect"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/rules"
	"github.com/mikluko/sniffarr/internal/store"
)

// MaxStatsDays is the most days Browser.Stats counts.
const MaxStatsDays = 3660

// Browser implements sniffarrv1connect.BrowseServiceHandler.
type Browser struct {
	Store *store.Store
	Log   *slog.Logger
	// DailyTokens is the classifier's daily token limit Stats reports; 0 is
	// none.
	DailyTokens int64
	// Languages are the audio languages Settings reports, tags in configured
	// order; nil when the audio extension is off.
	Languages []string
	// Now is time.Now when nil.
	Now func() time.Time
}

func (b *Browser) Settings(context.Context, *connect.Request[sniffarrv1.SettingsRequest]) (*connect.Response[sniffarrv1.SettingsResponse], error) {
	return connect.NewResponse(&sniffarrv1.SettingsResponse{AudioLanguages: slices.Clone(b.Languages)}), nil
}

func (b *Browser) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

var discoveries = map[string]sniffarrv1.Source{
	"sample":   sniffarrv1.Source_SOURCE_SAMPLE,
	"announce": sniffarrv1.Source_SOURCE_ANNOUNCE,
}

var rejections = map[rules.Reason]sniffarrv1.Rejection{
	rules.Adult:    sniffarrv1.Rejection_REJECTION_ADULT,
	rules.Software: sniffarrv1.Rejection_REJECTION_SOFTWARE,
	rules.Book:     sniffarrv1.Rejection_REJECTION_BOOK,
	rules.Course:   sniffarrv1.Rejection_REJECTION_COURSE,
	rules.Small:    sniffarrv1.Rejection_REJECTION_SMALL,
	rules.NoMedia:  sniffarrv1.Rejection_REJECTION_NO_MEDIA,
	rules.Script:   sniffarrv1.Rejection_REJECTION_SCRIPT,
}

// name returns the key of m whose value is v.
func name[K comparable, V comparable](m map[K]V, v V) (K, bool) {
	for k, x := range m {
		if x == v {
			return k, true
		}
	}
	var zero K
	return zero, false
}

func (b *Browser) Page(ctx context.Context, req *connect.Request[sniffarrv1.PageRequest]) (*connect.Response[sniffarrv1.PageResponse], error) {
	m := req.Msg
	lang, err := language(m.GetLang())
	if err != nil {
		return nil, err
	}
	q := store.Browse{Text: m.GetQuery(), MinSize: m.GetMinSize(), Offset: int(m.GetOffset()), Limit: int(m.GetLimit())}
	switch {
	case q.Limit < 0:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("negative limit"))
	case q.Limit == 0:
		q.Limit = DefaultLimit
	case q.Limit > MaxLimit:
		q.Limit = MaxLimit
	}
	if q.Offset < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("negative offset"))
	}
	switch {
	case m.GetUnknownKind() && m.GetKind() != sniffarrv1.Kind_KIND_UNSPECIFIED:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unknown_kind with a kind"))
	case m.GetUnknownKind():
		q.UnknownKind = true
	case m.GetKind() != sniffarrv1.Kind_KIND_UNSPECIFIED:
		kind, ok := kinds[m.GetKind()]
		if !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown kind %d", m.GetKind()))
		}
		q.Kind = kind
	default:
		q.Exclude = []string{"adult"}
	}
	if m.GetAudioMarker() || m.MinAudioProbability != nil {
		if m.GetAudioLanguage() == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("audio filter without audio_language"))
		}
		q.AudioLanguage, q.AudioMarker = m.GetAudioLanguage(), m.GetAudioMarker()
		if m.MinAudioProbability != nil {
			q.MinAudioProbability = new(float64(m.GetMinAudioProbability()))
		}
	}
	if s := m.GetSource(); s != sniffarrv1.VideoSource_VIDEO_SOURCE_UNSPECIFIED {
		source, ok := name(sources, s)
		if !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown source %d", s))
		}
		q.Source = source
	}
	if l := m.GetLayer(); l != sniffarrv1.Layer_LAYER_UNSPECIFIED {
		layer, ok := name(layers, l)
		if !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown layer %d", l))
		}
		q.KindLayer = layer
	}
	sort, ok := sorts[m.GetSort()]
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown sort %d", m.GetSort()))
	}
	q.Sort = sort
	found, total, err := b.Store.Page(ctx, q)
	if err != nil {
		b.Log.ErrorContext(ctx, "page", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("page failed"))
	}
	res := &sniffarrv1.PageResponse{Releases: make([]*sniffarrv1.Release, len(found)), Total: total}
	for i, r := range found {
		res.Releases[i] = release(r, lang)
	}
	if q.Text != "" && q.Offset == 0 {
		works, err := b.Store.Works(ctx, q.Text, b.now(), PageWorks)
		if err != nil {
			b.Log.ErrorContext(ctx, "works", "err", err)
			return nil, connect.NewError(connect.CodeInternal, errors.New("page failed"))
		}
		for _, w := range works {
			res.Works = append(res.Works, &sniffarrv1.WorkMatch{Work: workRef(w.Work), Peers: w.Peers, Releases: w.Work.Releases, Tmdb: tmdbWork(w.Work.TMDB, w.Work.Kind, lang)})
		}
	}
	return connect.NewResponse(res), nil
}

func (b *Browser) Card(ctx context.Context, req *connect.Request[sniffarrv1.CardRequest]) (*connect.Response[sniffarrv1.CardResponse], error) {
	lang, err := language(req.Msg.GetLang())
	if err != nil {
		return nil, err
	}
	h := req.Msg.GetInfohash()
	if len(h) != len(store.Hash{}) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("infohash of %d bytes, want 20", len(h)))
	}
	c, err := b.Store.Card(ctx, store.Hash(h), math.MaxInt32, b.now())
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if err != nil {
		b.Log.ErrorContext(ctx, "card", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("card failed"))
	}
	res := &sniffarrv1.CardResponse{Release: release(c.Release, lang), Discovery: discoveries[c.Discovery],
		Peers: &sniffarrv1.PeerCounts{Day: c.Peers.Day, Week: c.Peers.Week, Month: c.Peers.Month}}
	for _, f := range c.Files {
		res.Files = append(res.Files, &sniffarrv1.File{Path: f.Path, Size: f.Size})
	}
	if c.Work.Key != "" {
		res.Work, res.Releases, res.ReleaseRank = workRef(c.Work), c.Releases, int32(c.ReleaseRank)
	}
	return connect.NewResponse(res), nil
}

func (b *Browser) Stats(ctx context.Context, req *connect.Request[sniffarrv1.StatsRequest]) (*connect.Response[sniffarrv1.StatsResponse], error) {
	days := int(req.Msg.GetDays())
	if days < 0 || days > MaxStatsDays {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("days %d, want 0 to %d", days, MaxStatsDays))
	}
	now := b.now()
	st, err := b.Store.Stats(ctx, now, days)
	if err != nil {
		b.Log.ErrorContext(ctx, "stats", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("stats failed"))
	}
	used, err := b.Store.TokensUsed(ctx, now.UTC().Format(time.DateOnly))
	if err != nil {
		b.Log.ErrorContext(ctx, "tokens used", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("stats failed"))
	}
	res := &sniffarrv1.StatsResponse{Total: st.Total, Pending: st.Pending, Days: st.Days, TokensUsedDay: used, TokensLimitDay: b.DailyTokens}
	for _, kind := range slices.Sorted(maps.Keys(st.Kinds)) {
		k, _ := name(kinds, kind)
		res.Kinds = append(res.Kinds, &sniffarrv1.KindCount{Kind: k, Count: st.Kinds[kind]})
	}
	for _, layer := range slices.Sorted(maps.Keys(st.Layers)) {
		res.Layers = append(res.Layers, &sniffarrv1.LayerCount{Layer: layers[layer], Count: st.Layers[layer]})
	}
	for _, source := range slices.Sorted(maps.Keys(st.Sources)) {
		res.Sources = append(res.Sources, &sniffarrv1.SourceCount{Source: sources[source], Count: st.Sources[source]})
	}
	for _, lang := range slices.Sorted(maps.Keys(st.Audio)) {
		a := st.Audio[lang]
		res.Audio = append(res.Audio, &sniffarrv1.AudioCounts{Language: lang, Marker: a.Marker, Likely: a.Likely, Unlikely: a.Unlikely})
	}
	for _, reason := range slices.Sorted(maps.Keys(st.Rejected)) {
		res.Rejected = append(res.Rejected, &sniffarrv1.RejectionCount{Reason: rejections[rules.Reason(reason)], Count: st.Rejected[reason]})
	}
	return connect.NewResponse(res), nil
}
