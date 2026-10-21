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

// TitleSearch searches TMDB for works and stores what it finds;
// tmdb.Matcher is one.
type TitleSearch interface {
	SearchWorks(ctx context.Context, typ, query, lang string) ([]int64, error)
}

// Browser implements sniffarrv1connect.BrowseServiceHandler.
type Browser struct {
	Store *store.Store
	// TMDB, when set, has SearchTitles search TMDB as well as the store.
	TMDB TitleSearch
	Log  *slog.Logger
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

// others are the kinds the UI shows as other.
var others = []string{"other", "music", "book", "software"}

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

// enum returns the store's name of the API's value v by m, "" for the
// unspecified value, or an INVALID_ARGUMENT error naming what for a value
// absent from m.
func enum[V comparable](m map[string]V, v V, what string) (string, error) {
	var zero V
	if v == zero {
		return "", nil
	}
	n, ok := name(m, v)
	if !ok {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown %s %v", what, v))
	}
	return n, nil
}

// pageLimit reads a page's limit and offset: DefaultLimit for none, at most
// MaxLimit.
func pageLimit(limit, offset int32) (int, int, error) {
	if limit < 0 || offset < 0 {
		return 0, 0, connect.NewError(connect.CodeInvalidArgument, errors.New("negative limit or offset"))
	}
	if limit == 0 {
		limit = DefaultLimit
	}
	return min(int(limit), MaxLimit), int(offset), nil
}

// voiceOf reads a voice and the language it applies to, which a narrowing
// voice requires.
func voiceOf(v sniffarrv1.Voice, lang *string) (store.Voice, string, error) {
	voice, ok := voices[v]
	if !ok {
		return "", "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown voice %d", v))
	}
	if voice != store.VoiceAny && (lang == nil || *lang == "") {
		return "", "", connect.NewError(connect.CodeInvalidArgument, errors.New("voice without audio_language"))
	}
	if lang == nil {
		return voice, "", nil
	}
	return voice, *lang, nil
}

func (b *Browser) Torrents(ctx context.Context, req *connect.Request[sniffarrv1.TorrentsRequest]) (*connect.Response[sniffarrv1.TorrentsResponse], error) {
	m := req.Msg
	lang, err := language(m.GetLang())
	if err != nil {
		return nil, err
	}
	q := store.Browse{Text: m.GetQuery(), MinSize: m.GetMinSize(), Exclude: []string{"adult"}, Matched: m.Matched}
	if q.Limit, q.Offset, err = pageLimit(m.GetLimit(), m.GetOffset()); err != nil {
		return nil, err
	}
	switch k := m.GetKind(); {
	case m.GetUnknownKind() && k != sniffarrv1.Kind_KIND_UNSPECIFIED:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unknown_kind with a kind"))
	case m.GetUnknownKind():
		q.Kinds = []string{""}
	case k == sniffarrv1.Kind_KIND_OTHER:
		q.Kinds = others
	case k != sniffarrv1.Kind_KIND_UNSPECIFIED:
		kind, ok := kinds[k]
		if !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown kind %d", k))
		}
		q.Kinds = []string{kind}
	}
	if q.Voice, q.AudioLanguage, err = voiceOf(m.GetVoice(), m.AudioLanguage); err != nil {
		return nil, err
	}
	if q.Quality, err = enum(qualities, m.GetQuality(), "quality"); err != nil {
		return nil, err
	}
	if q.Source, err = enum(sources, m.GetSource(), "source"); err != nil {
		return nil, err
	}
	if q.KindLayer, err = enum(layers, m.GetLayer(), "layer"); err != nil {
		return nil, err
	}
	sort, ok := sorts[m.GetSort()]
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown sort %d", m.GetSort()))
	}
	q.Sort = sort
	found, total, err := b.Store.Page(ctx, q)
	if err != nil {
		return nil, b.internal(ctx, "torrents", err)
	}
	var ids []int64
	for _, r := range found {
		if r.Work != 0 && !slices.Contains(ids, r.Work) {
			ids = append(ids, r.Work)
		}
	}
	sums, err := b.Store.Summaries(ctx, ids)
	if err != nil {
		return nil, b.internal(ctx, "torrents", err)
	}
	res := &sniffarrv1.TorrentsResponse{Total: total}
	for _, r := range found {
		row := &sniffarrv1.TorrentRow{Release: release(r, lang)}
		if sum, ok := sums[r.Work]; ok {
			row.Work = summary(sum, lang)
		}
		res.Rows = append(res.Rows, row)
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
		return nil, b.internal(ctx, "card", err)
	}
	res := &sniffarrv1.CardResponse{Release: release(c.Release, lang), Discovery: discoveries[c.Discovery],
		Peers: &sniffarrv1.PeerCounts{Day: c.Peers.Day, Week: c.Peers.Week, Month: c.Peers.Month}}
	for _, f := range c.Files {
		res.Files = append(res.Files, &sniffarrv1.File{Path: f.Path, Size: f.Size})
	}
	if c.Work != nil {
		res.Work, res.RankInWork = summary(*c.Work, lang), int32(c.RankInWork)
		if t := res.GetRelease().GetTmdb(); t != nil {
			res.Work.Tmdb.Layer, res.Work.Tmdb.Confidence, res.Work.Tmdb.CheckedAt = t.GetLayer(), t.GetConfidence(), t.GetCheckedAt()
		}
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
		return nil, b.internal(ctx, "stats", err)
	}
	used, err := b.Store.TokensUsed(ctx, now.UTC().Format(time.DateOnly))
	if err != nil {
		return nil, b.internal(ctx, "stats", err)
	}
	res := &sniffarrv1.StatsResponse{Total: st.Total, Pending: st.Pending, Days: st.Days, TokensUsedDay: used, TokensLimitDay: b.DailyTokens}
	byKind := map[sniffarrv1.Kind]int64{}
	for kind, n := range st.Kinds {
		if slices.Contains(others, kind) {
			kind = "other"
		}
		k, _ := name(kinds, kind)
		byKind[k] += n
	}
	for _, k := range slices.Sorted(maps.Keys(byKind)) {
		res.Kinds = append(res.Kinds, &sniffarrv1.KindCount{Kind: k, Count: byKind[k]})
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

// internal logs a failure of the operation and returns the INTERNAL error
// the client gets for it.
func (b *Browser) internal(ctx context.Context, op string, err error) error {
	b.Log.ErrorContext(ctx, op, "err", err)
	return connect.NewError(connect.CodeInternal, fmt.Errorf("%s failed", op))
}
