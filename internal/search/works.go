package search

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/store"
)

const (
	// DefaultTopLimit is the number of entries Top returns when asked for
	// none in particular.
	DefaultTopLimit = 20
	// MaxOurTitles and MaxTMDBTitles are the most works with releases, and
	// without, SearchTitles returns.
	MaxOurTitles  = 50
	MaxTMDBTitles = 40
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
	sniffarrv1.Sort_SORT_FIRST_SEEN:  store.SortFirstSeen,
}

// summary is the API's view of a work's summary, with TMDBWork texts in
// lang.
func summary(s store.Summary, lang string) *sniffarrv1.WorkSummary {
	kind, _ := name(kinds, s.Work.Kind())
	return &sniffarrv1.WorkSummary{Id: WorkID(s.Work), Kind: kind, Tmdb: tmdbOf(s.Work, lang), Releases: s.Releases,
		Peers: s.Work.PeersWeek, Rank: int32(s.Rank)}
}

// workAudio is the API's view of a work's audio summaries.
func workAudio(as []store.WorkAudio) []*sniffarrv1.WorkAudio {
	var out []*sniffarrv1.WorkAudio
	for _, a := range as {
		out = append(out, &sniffarrv1.WorkAudio{Language: a.Language, Marked: a.Marked, Marks: a.Marks, Likely: a.Likely})
	}
	return out
}

// titleKind reads the kind of works a request asks for: KIND_MOVIE,
// KIND_SERIES, or either.
func titleKind(k sniffarrv1.Kind) (string, error) {
	switch k {
	case sniffarrv1.Kind_KIND_UNSPECIFIED:
		return "", nil
	case sniffarrv1.Kind_KIND_MOVIE, sniffarrv1.Kind_KIND_SERIES:
		return kinds[k], nil
	}
	return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("kind %v has no works", k))
}

func (b *Browser) SearchTitles(ctx context.Context, req *connect.Request[sniffarrv1.SearchTitlesRequest]) (*connect.Response[sniffarrv1.SearchTitlesResponse], error) {
	m := req.Msg
	lang, err := language(m.GetLang())
	if err != nil {
		return nil, err
	}
	kind, err := titleKind(m.GetKind())
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(m.GetQuery())
	res := &sniffarrv1.SearchTitlesResponse{}
	if query == "" {
		return connect.NewResponse(res), nil
	}
	types := []string{store.TypeMovie, store.TypeTV}
	typ := ""
	if kind != "" {
		typ = store.TypeOf(kind)
		types = []string{typ}
	}
	ids, err := b.Store.FindWorks(ctx, query, typ)
	if err != nil {
		return nil, b.internal(ctx, "search titles", err)
	}
	if b.TMDB != nil {
		found := make([][]int64, len(types))
		var wg sync.WaitGroup
		for i, t := range types {
			wg.Go(func() {
				var err error
				if found[i], err = b.TMDB.SearchWorks(ctx, t, query, lang); err != nil && ctx.Err() == nil {
					b.Log.WarnContext(ctx, "search tmdb", "type", t, "err", err)
				}
			})
		}
		wg.Wait()
		for _, f := range found {
			for _, id := range f {
				if !slices.Contains(ids, id) {
					ids = append(ids, id)
				}
			}
		}
	}
	sums, err := b.Store.Summaries(ctx, ids)
	if err != nil {
		return nil, b.internal(ctx, "search titles", err)
	}
	var ours, rest []store.Summary
	for _, id := range ids {
		s, ok := sums[id]
		switch {
		case !ok:
		case s.Releases > 0:
			ours = append(ours, s)
		default:
			rest = append(rest, s)
		}
	}
	slices.SortStableFunc(ours, func(a, b store.Summary) int { return cmp.Compare(b.Work.PeersWeek, a.Work.PeersWeek) })
	slices.SortStableFunc(rest, func(a, b store.Summary) int { return cmp.Compare(b.Work.Popularity, a.Work.Popularity) })
	ours, rest = ours[:min(len(ours), MaxOurTitles)], rest[:min(len(rest), MaxTMDBTitles)]
	for _, s := range slices.Concat(ours, rest) {
		res.Works = append(res.Works, summary(s, lang))
	}
	res.Ours, res.TmdbOnly = int32(len(ours)), int32(len(rest))
	return connect.NewResponse(res), nil
}

// work returns the store's ID of the work with the API's ID, or a
// connect error.
func (b *Browser) work(ctx context.Context, id string) (int64, error) {
	typ, tmdbID, ok := ParseWorkID(id)
	if !ok {
		return 0, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("work id %q", id))
	}
	w, err := b.Store.WorkByTMDB(ctx, typ, tmdbID)
	if errors.Is(err, store.ErrNotFound) {
		return 0, connect.NewError(connect.CodeNotFound, fmt.Errorf("work %s", id))
	}
	if err != nil {
		return 0, b.internal(ctx, "work", err)
	}
	return w, nil
}

func (b *Browser) Title(ctx context.Context, req *connect.Request[sniffarrv1.TitleRequest]) (*connect.Response[sniffarrv1.TitleResponse], error) {
	lang, err := language(req.Msg.GetLang())
	if err != nil {
		return nil, err
	}
	id, err := b.work(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	sums, err := b.Store.Summaries(ctx, []int64{id})
	if err != nil {
		return nil, b.internal(ctx, "title", err)
	}
	ranks, err := b.Store.Ranks(ctx, id)
	if err != nil {
		return nil, b.internal(ctx, "title", err)
	}
	peers, err := b.Store.WorkPeers(ctx, id, b.now())
	if err != nil {
		return nil, b.internal(ctx, "title", err)
	}
	s := sums[id]
	return connect.NewResponse(&sniffarrv1.TitleResponse{Work: summary(s, lang), Audio: workAudio(s.Audio),
		Ranks: &sniffarrv1.WindowRanks{Day: int32(ranks[0]), Week: int32(ranks[1]), Month: int32(ranks[2])},
		Peers: &sniffarrv1.PeerCounts{Day: peers.Day, Week: peers.Week, Month: peers.Month}}), nil
}

func (b *Browser) TitleReleases(ctx context.Context, req *connect.Request[sniffarrv1.TitleReleasesRequest]) (*connect.Response[sniffarrv1.TitleReleasesResponse], error) {
	m := req.Msg
	f := store.ReleaseFilter{MinSize: m.GetMinSize(), MaxSize: m.GetMaxSize(), Season: int(m.GetSeason()), Episode: int(m.GetEpisode())}
	var err error
	if f.Limit, f.Offset, err = pageLimit(m.GetLimit(), m.GetOffset()); err != nil {
		return nil, err
	}
	if f.MinSize < 0 || f.MaxSize < 0 || f.Season < 0 || f.Episode < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("negative size, season or episode"))
	}
	if f.Voice, f.AudioLanguage, err = voiceOf(m.GetVoice(), m.AudioLanguage); err != nil {
		return nil, err
	}
	if f.Quality, err = enum(qualities, m.GetQuality(), "quality"); err != nil {
		return nil, err
	}
	if f.Source, err = enum(sources, m.GetSource(), "source"); err != nil {
		return nil, err
	}
	switch m.GetSort() {
	case sniffarrv1.Sort_SORT_UNSPECIFIED, sniffarrv1.Sort_SORT_PEERS, sniffarrv1.Sort_SORT_FIRST_SEEN, sniffarrv1.Sort_SORT_SIZE:
		f.Sort = sorts[m.GetSort()]
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("sort %v of releases", m.GetSort()))
	}
	id, err := b.work(ctx, m.GetId())
	if err != nil {
		return nil, err
	}
	p, err := b.Store.WorkReleases(ctx, id, f)
	if err != nil {
		return nil, b.internal(ctx, "title releases", err)
	}
	res := &sniffarrv1.TitleReleasesResponse{Total: p.Total, Matched: p.Matched}
	for _, r := range p.Releases {
		res.Releases = append(res.Releases, release(r, ""))
	}
	for _, n := range p.Seasons {
		res.Seasons = append(res.Seasons, int32(n))
	}
	return connect.NewResponse(res), nil
}

func (b *Browser) Home(ctx context.Context, _ *connect.Request[sniffarrv1.HomeRequest]) (*connect.Response[sniffarrv1.HomeResponse], error) {
	h, err := b.Store.Home(ctx, b.now())
	if err != nil {
		return nil, b.internal(ctx, "home", err)
	}
	return connect.NewResponse(&sniffarrv1.HomeResponse{Total: h.Total, NewDay: h.NewDay, Pending: h.Pending}), nil
}

func (b *Browser) Top(ctx context.Context, req *connect.Request[sniffarrv1.TopRequest]) (*connect.Response[sniffarrv1.TopResponse], error) {
	m := req.Msg
	kind, ok := kinds[m.GetKind()]
	if !ok || !slices.Contains(store.TopKinds, kind) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("kind %v has no top", m.GetKind()))
	}
	window, ok := windows[m.GetWindow()]
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown window %d", m.GetWindow()))
	}
	voice, ok := voices[m.GetVoice()]
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown voice %d", m.GetVoice()))
	}
	vl := m.GetVoiceLanguage()
	if vl == "" && len(b.Languages) > 0 {
		vl = b.Languages[0]
	}
	lang, err := language(m.GetLang())
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
	entries, total, at, err := b.Store.Top(ctx, kind, window, voice, vl, limit, offset)
	if err != nil {
		return nil, b.internal(ctx, "top", err)
	}
	res := &sniffarrv1.TopResponse{Total: total}
	if !at.IsZero() {
		res.ComputedAt = timestamppb.New(at)
	}
	for _, e := range entries {
		pe := &sniffarrv1.TopEntry{Rank: int32(e.Rank), Work: summary(e.Work, lang), Peers: e.Peers, Audio: workAudio(e.Work.Audio)}
		if !e.Work.FirstSeen.IsZero() {
			pe.FirstSeen = timestamppb.New(e.Work.FirstSeen)
		}
		if e.Delta != nil {
			pe.Delta = new(float32(*e.Delta))
		}
		for _, s := range e.Work.Sources {
			pe.Sources = append(pe.Sources, sources[s])
		}
		res.Entries = append(res.Entries, pe)
	}
	return connect.NewResponse(res), nil
}
