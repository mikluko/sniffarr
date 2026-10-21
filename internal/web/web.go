// Package web serves the web UI as a client of the browse API: the home
// page, the search of movies and series, a movie's or series' page with its
// releases, the torrents, the tops, a release's card, and the store's
// counts. It authenticates nobody; that is left to a proxy in front of it.
package web

import (
	"cmp"
	"context"
	"encoding/hex"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/audio"
	"github.com/mikluko/sniffarr/internal/names"
	"github.com/mikluko/sniffarr/internal/ui"
)

// TokenHeader carries the access token of the signed-in person, as
// oauth2-proxy passes it on.
const TokenHeader = "X-Forwarded-Access-Token"

// PageSize is how many rows a page of torrents or releases lists.
const PageSize = 50

// cardFiles is how many files a card lists until all are asked for.
const cardFiles = 8

// A top page lists topShown works until all topSize are asked for.
const (
	topShown = 25
	topSize  = 100
)

// infohashLen is the length of a BitTorrent v1 infohash in bytes.
const infohashLen = 20

// Config is what New needs.
type Config struct {
	// Browse is the API every page is read from, with its token.
	Browse sniffarrv1connect.BrowseServiceClient
	// Languages are the API's audio languages in its order, as
	// ObtainLanguages returns them; none leaves the voice filters, columns
	// and card rows out of every page.
	Languages []audio.Language
	// Locales tells the locale of the person an access token was issued to;
	// when nil, the language is only the one picked on the page.
	Locales Locales
	// Location is the time zone of dates; time.Local when nil.
	Location *time.Location
	// Log discards everything when nil.
	Log *slog.Logger
	// Now is time.Now when nil.
	Now func() time.Time
}

// Locales looks up locales by access token.
type Locales interface {
	Locale(ctx context.Context, token string) (string, error)
}

type server struct {
	cfg  Config
	ui   *ui.Renderer
	tags []string
}

// New returns the UI's handler.
func New(cfg Config) (http.Handler, error) {
	r, err := ui.New()
	if err != nil {
		return nil, err
	}
	if cfg.Location == nil {
		cfg.Location = time.Local
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &server{cfg: cfg, ui: r, tags: audio.Tags(cfg.Languages)}
	mux := http.NewServeMux()
	mux.Handle("GET /static/", ui.Static())
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /w/{id}", s.work)
	mux.HandleFunc("GET /torrents", s.torrents)
	mux.HandleFunc("GET /top", s.top)
	mux.HandleFunc("GET /top/w/{id}", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/w/"+url.PathEscape(req.PathValue("id")), http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /t/{infohash}", s.torrent)
	mux.HandleFunc("GET /data", s.data)
	return mux, nil
}

// locale is the locale of the person whose access token is in TokenHeader,
// or "" when there is none or Locales is nil.
func (s *server) locale(req *http.Request) string {
	token := req.Header.Get(TokenHeader)
	if s.cfg.Locales == nil || token == "" {
		return ""
	}
	l, err := s.cfg.Locales.Locale(req.Context(), token)
	if err != nil {
		s.cfg.Log.WarnContext(req.Context(), "locale", "err", err)
	}
	return l
}

// lang resolves the language of the page, setting the cookie of a pick.
func (s *server) lang(w http.ResponseWriter, req *http.Request) string {
	return ui.Lang(w, req, s.locale(req))
}

func (s *server) base(req *http.Request, lang, nav string) ui.Base {
	b := s.ui.Base(req, lang, nav, s.tags, s.cfg.Location)
	b.Now = s.cfg.Now()
	return b
}

func (s *server) render(w http.ResponseWriter, req *http.Request, page string, data any) {
	if err := s.ui.Render(w, req, page, data); err != nil {
		s.fail(w, req, "render "+page, err)
	}
}

func (s *server) fail(w http.ResponseWriter, req *http.Request, op string, err error) {
	s.cfg.Log.ErrorContext(req.Context(), op, "err", err)
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

// The UI's keys of the API's enumerations; a value absent here is "none",
// or for kinds "other".
var (
	kinds = map[sniffarrv1.Kind]string{sniffarrv1.Kind_KIND_UNSPECIFIED: "unknown", sniffarrv1.Kind_KIND_MOVIE: "movie", sniffarrv1.Kind_KIND_SERIES: "series",
		sniffarrv1.Kind_KIND_OTHER: "other"}
	kindLayers = map[sniffarrv1.Layer]string{sniffarrv1.Layer_LAYER_RULES: "rules", sniffarrv1.Layer_LAYER_REUSE: "title_repeat", sniffarrv1.Layer_LAYER_CLASSIFIER: "classifier"}
	sources    = map[sniffarrv1.VideoSource]string{sniffarrv1.VideoSource_VIDEO_SOURCE_DISC_IMAGE: "disc", sniffarrv1.VideoSource_VIDEO_SOURCE_REMUX: "remux",
		sniffarrv1.VideoSource_VIDEO_SOURCE_ENCODE: "rip", sniffarrv1.VideoSource_VIDEO_SOURCE_WEB: "web", sniffarrv1.VideoSource_VIDEO_SOURCE_OTHER: "other"}
	qualities = map[sniffarrv1.Quality]string{sniffarrv1.Quality_QUALITY_2160P: "2160p", sniffarrv1.Quality_QUALITY_1080P: "1080p",
		sniffarrv1.Quality_QUALITY_720P: "720p", sniffarrv1.Quality_QUALITY_SD: "sd"}
	drops = map[sniffarrv1.Rejection]string{sniffarrv1.Rejection_REJECTION_ADULT: "adult", sniffarrv1.Rejection_REJECTION_SOFTWARE: "software",
		sniffarrv1.Rejection_REJECTION_BOOK: "book", sniffarrv1.Rejection_REJECTION_COURSE: "course", sniffarrv1.Rejection_REJECTION_SCRIPT: "script",
		sniffarrv1.Rejection_REJECTION_NO_MEDIA: "filetype", sniffarrv1.Rejection_REJECTION_SMALL: "size"}
	discoveries = map[sniffarrv1.Source]string{sniffarrv1.Source_SOURCE_ANNOUNCE: "announce", sniffarrv1.Source_SOURCE_SAMPLE: "sample"}
	sorts       = map[sniffarrv1.Sort]string{sniffarrv1.Sort_SORT_PEERS: "peers", sniffarrv1.Sort_SORT_LAST_SEEN: "last_seen", sniffarrv1.Sort_SORT_SIZE: "size",
		sniffarrv1.Sort_SORT_FIRST_SEEN: "first_seen"}
	windows     = map[sniffarrv1.Window]string{sniffarrv1.Window_WINDOW_DAY: "24h", sniffarrv1.Window_WINDOW_WEEK: "7d", sniffarrv1.Window_WINDOW_MONTH: "30d"}
	voices      = map[sniffarrv1.Voice]string{sniffarrv1.Voice_VOICE_UNSPECIFIED: "any", sniffarrv1.Voice_VOICE_LIKELY: "p50", sniffarrv1.Voice_VOICE_MARKER: "explicit"}
	tmdbMethods = map[sniffarrv1.TMDBLayer]string{sniffarrv1.TMDBLayer_TMDB_LAYER_SEARCH: "search", sniffarrv1.TMDBLayer_TMDB_LAYER_CLASSIFIER: "classifier"}
)

// uiOr returns m's value for k, or def.
func uiOr[K comparable](m map[K]string, k K, def string) string {
	if v, ok := m[k]; ok {
		return v
	}
	return def
}

// key returns the key of m whose value is v, or the zero key.
func key[K comparable](m map[K]string, v string) K {
	for k, x := range m {
		if x == v {
			return k
		}
	}
	var zero K
	return zero
}

// tmdbWork is the UI's view of a title's TMDB lookup: nil while unchecked,
// unmatched when nothing matched, else the match.
func tmdbWork(t *sniffarrv1.TMDBWork) *ui.TMDBWork {
	if t == nil {
		return nil
	}
	out := &ui.TMDBWork{State: ui.TMDBUnmatched}
	if t.CheckedAt != nil {
		out.CheckedAt = t.GetCheckedAt().AsTime()
	}
	if t.GetId() == 0 {
		return out
	}
	out.State = ui.TMDBMatched
	out.ID, out.Type, out.IMDbID = t.GetId(), t.GetType(), t.GetImdbId()
	out.Title, out.OriginalTitle, out.Year = t.GetTitle(), t.GetOriginalTitle(), int(t.GetYear())
	out.PosterPath, out.Overview, out.Genres = t.GetPosterPath(), t.GetOverview(), t.GetGenres()
	out.Rating, out.Votes, out.Runtime = t.GetRating(), int(t.GetVotes()), int(t.GetRuntime())
	out.Status, out.Seasons, out.Countries, out.Language = t.GetStatus(), int(t.GetSeasons()), t.GetCountries(), t.GetLanguage()
	out.Method, out.Confidence = tmdbMethods[t.GetLayer()], float64(t.GetConfidence())
	return out
}

// summary is the UI's view of a work, or nil for none.
func summary(w *sniffarrv1.WorkSummary) *ui.WorkSummary {
	if w == nil {
		return nil
	}
	return &ui.WorkSummary{ID: w.GetId(), Kind: uiOr(kinds, w.GetKind(), "movie"), TMDB: tmdbWork(w.GetTmdb()),
		Releases: int(w.GetReleases()), Peers: w.GetPeers(), Rank: int(w.GetRank())}
}

// workAudio is the UI's view of a work's audio summaries.
func workAudio(as []*sniffarrv1.WorkAudio) []ui.WorkAudio {
	var out []ui.WorkAudio
	for _, a := range as {
		out = append(out, ui.WorkAudio{Lang: a.GetLanguage(), Marked: int(a.GetMarked()), Marks: a.GetMarks(), Likely: int(a.GetLikely())})
	}
	return out
}

// audioOf is the UI's view of one audio entry.
func audioOf(a *sniffarrv1.Audio) ui.Audio {
	out := ui.Audio{Lang: a.GetLanguage(), Marker: a.GetMarker(), Mark: a.GetMark()}
	if a.Probability != nil {
		out.Probability = new(float64(a.GetProbability()))
	}
	return out
}

// releaseAudio is the UI's view of a release's audio: the entries of the
// configured languages with a marker or a probability.
func (s *server) releaseAudio(as []*sniffarrv1.Audio) []ui.Audio {
	var out []ui.Audio
	for _, a := range as {
		if slices.Contains(s.tags, a.GetLanguage()) && (a.GetMarker() || a.Probability != nil) {
			out = append(out, audioOf(a))
		}
	}
	return out
}

// voice reads the voice filter of a page: the language among the
// configured ones, the UI language's by default, and the mode, "any" by
// default and without languages.
func (s *server) voice(q url.Values, lang string) (vl, mode string) {
	vl, mode = q.Get("vl"), q.Get("voice")
	if !slices.Contains(s.tags, vl) {
		vl = ui.DefaultVoiceLang(s.tags, lang)
	}
	if vl == "" || !slices.Contains(ui.VoiceModes, mode) {
		mode = "any"
	}
	return vl, mode
}

// apiVoice is the API's voice and language of a filter's.
func apiVoice(vl, mode string) (sniffarrv1.Voice, *string) {
	v := key(voices, mode)
	if v == sniffarrv1.Voice_VOICE_UNSPECIFIED {
		return v, nil
	}
	return v, &vl
}

// oneOf returns v when it is in xs, else "".
func oneOf(v string, xs []string) string {
	if slices.Contains(xs, v) {
		return v
	}
	return ""
}

// size is the size in bytes of a size in GiB as typed, with a point or a
// comma; 0 when it is not a positive number.
func size(gb string) int64 {
	v, err := strconv.ParseFloat(strings.Replace(strings.TrimSpace(gb), ",", ".", 1), 64)
	if err != nil || v <= 0 || math.IsInf(v, 0) {
		return 0
	}
	return int64(math.Min(v*(1<<30), math.MaxInt64))
}

// number is a positive number as typed, or "".
func number(s string) string {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// offset reads the o parameter, 0 when it is not a non-negative number.
func offset(q url.Values) int {
	o, err := strconv.Atoi(q.Get("o"))
	if err != nil || o < 0 {
		return 0
	}
	return o
}

// link is the path with the query of the pairs whose value is set and not
// the default given after it, and the offset unless 0.
func link(path string, offset int, pairs ...string) string {
	v := url.Values{}
	for i := 0; i+2 < len(pairs); i += 3 {
		if x := pairs[i+1]; x != "" && x != pairs[i+2] {
			v.Set(pairs[i], x)
		}
	}
	if offset > 0 {
		v.Set("o", strconv.Itoa(offset))
	}
	if len(v) == 0 {
		return path
	}
	return path + "?" + v.Encode()
}

// pager sets the previous and next page links of a page of shown rows from
// offset among total, by at.
func pager(offset, shown int, total int64, at func(int) string) (prev, next string) {
	if offset > 0 {
		prev = at(max(0, offset-PageSize))
	}
	if int64(offset+shown) < total {
		next = at(offset + PageSize)
	}
	return prev, next
}

// index serves GET /: the home page, or the search of movies and series
// once q is in the query, empty or not.
func (s *server) index(w http.ResponseWriter, req *http.Request) {
	qs := req.URL.Query()
	lang := s.lang(w, req)
	if !qs.Has("q") {
		res, err := s.cfg.Browse.Home(req.Context(), connect.NewRequest(&sniffarrv1.HomeRequest{}))
		if err != nil {
			s.fail(w, req, "home", err)
			return
		}
		h := res.Msg
		s.render(w, req, "home", ui.HomePage{Base: s.base(req, lang, "home"), Summary: ui.Summary{Total: h.GetTotal(), NewDay: h.GetNewDay(), Pending: h.GetPending()}})
		return
	}
	q := strings.TrimSpace(qs.Get("q"))
	kind := oneOf(qs.Get("kind"), ui.TopKinds)
	page := ui.SearchPage{Base: s.base(req, lang, "search"), Q: q, Titles: ui.WorkResults{Kind: kind}}
	if q != "" {
		res, err := s.cfg.Browse.SearchTitles(req.Context(), connect.NewRequest(&sniffarrv1.SearchTitlesRequest{Query: q, Kind: key(kinds, kind), Lang: lang}))
		if err != nil {
			s.fail(w, req, "search titles", err)
			return
		}
		for _, x := range res.Msg.GetWorks() {
			page.Titles.Items = append(page.Titles.Items, *summary(x))
		}
		page.Titles.Ours, page.Titles.TMDBOnly = int(res.Msg.GetOurs()), int(res.Msg.GetTmdbOnly())
	}
	s.render(w, req, "search", page)
}

// work serves GET /w/{id}: a movie or series with its releases under the
// filters of the query.
func (s *server) work(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")
	if _, _, ok := ui.ParseWorkID(id); !ok {
		http.NotFound(w, req)
		return
	}
	qs := req.URL.Query()
	lang := s.lang(w, req)
	title, err := s.cfg.Browse.Title(req.Context(), connect.NewRequest(&sniffarrv1.TitleRequest{Id: id, Lang: lang}))
	if c := connect.CodeOf(err); c == connect.CodeNotFound || c == connect.CodeInvalidArgument {
		http.NotFound(w, req)
		return
	}
	if err != nil {
		s.fail(w, req, "title", err)
		return
	}
	t := title.Msg
	sum := summary(t.GetWork())
	r := t.GetRanks()
	page := ui.WorkPage{Base: s.base(req, lang, ""), Work: *sum, Ranks: [3]int{int(r.GetDay()), int(r.GetWeek()), int(r.GetMonth())},
		Peers: ui.PeerCounts{Day: t.GetPeers().GetDay(), Week: t.GetPeers().GetWeek(), Month: t.GetPeers().GetMonth()}, Audio: workAudio(t.GetAudio())}
	if sum.Releases == 0 {
		s.render(w, req, "work", page)
		return
	}
	f := ui.ReleaseFilters{Quality: oneOf(qs.Get("quality"), ui.Qualities), Source: oneOf(qs.Get("source"), ui.ReleaseSources),
		MinGB: strings.TrimSpace(qs.Get("min")), MaxGB: strings.TrimSpace(qs.Get("max")), Sort: oneOf(qs.Get("sort"), ui.ReleaseSorts)}
	if f.Sort == "" {
		f.Sort = "peers"
	}
	f.VoiceLang, f.Voice = s.voice(qs, lang)
	if sum.Kind == "series" {
		f.Season, f.Episode = number(qs.Get("season")), number(qs.Get("episode"))
	}
	o := offset(qs)
	rr := &sniffarrv1.TitleReleasesRequest{Id: id, Quality: key(qualities, f.Quality), Source: key(sources, f.Source),
		MinSize: size(f.MinGB), MaxSize: size(f.MaxGB), Sort: key(sorts, f.Sort), Offset: int32(o), Limit: PageSize}
	rr.Voice, rr.AudioLanguage = apiVoice(f.VoiceLang, f.Voice)
	season, _ := strconv.Atoi(f.Season)
	episode, _ := strconv.Atoi(f.Episode)
	rr.Season, rr.Episode = int32(season), int32(episode)
	res, err := s.cfg.Browse.TitleReleases(req.Context(), connect.NewRequest(rr))
	if err != nil {
		s.fail(w, req, "title releases", err)
		return
	}
	m := res.Msg
	tbl := ui.ReleaseTable{WorkID: id, Kind: sum.Kind, Filters: f, Total: int(m.GetTotal()), Matched: int(m.GetMatched()), Offset: o, Shown: len(m.GetReleases())}
	for _, n := range m.GetSeasons() {
		tbl.Seasons = append(tbl.Seasons, int(n))
	}
	vdef := ui.DefaultVoiceLang(s.tags, lang)
	tbl.PrevURL, tbl.NextURL = pager(o, tbl.Shown, m.GetMatched(), func(o int) string {
		return link("/w/"+url.PathEscape(id), o, "quality", f.Quality, "", "source", f.Source, "", "min", f.MinGB, "", "max", f.MaxGB, "",
			"vl", f.VoiceLang, vdef, "voice", f.Voice, "any", "season", f.Season, "", "episode", f.Episode, "", "sort", f.Sort, "peers")
	})
	for _, r := range m.GetReleases() {
		tbl.Rows = append(tbl.Rows, ui.Release{InfoHash: hex.EncodeToString(r.GetInfohash()), Name: r.GetName(), Peers: r.GetPeersWeek(), Size: r.GetSize(),
			Quality: qualities[r.GetQuality()], Source: uiOr(sources, r.GetSource(), "none"), Season: int(r.GetSeason()), Episode: int(r.GetEpisode()),
			Audio: s.releaseAudio(r.GetAudio()), FirstSeen: r.GetFirstSeen().AsTime()})
	}
	page.Releases = tbl
	s.render(w, req, "work", page)
}

// torrents serves GET /torrents: the releases under the filters of the
// query, matched to a work or not.
func (s *server) torrents(w http.ResponseWriter, req *http.Request) {
	qs := req.URL.Query()
	lang := s.lang(w, req)
	q := strings.TrimSpace(qs.Get("q"))
	f := ui.Filters{Kind: oneOf(qs.Get("kind"), ui.Kinds), Matched: oneOf(qs.Get("matched"), []string{"yes", "no"}), Quality: oneOf(qs.Get("quality"), ui.Qualities),
		Source: oneOf(qs.Get("source"), ui.Sources), Layer: oneOf(qs.Get("layer"), ui.Layers), MinSizeGB: strings.TrimSpace(qs.Get("min")), Sort: oneOf(qs.Get("sort"), ui.Sorts)}
	if f.Sort == "" {
		f.Sort = "peers"
	}
	f.VoiceLang, f.Voice = s.voice(qs, lang)
	o := offset(qs)
	tr := &sniffarrv1.TorrentsRequest{Query: q, Quality: key(qualities, f.Quality), Source: key(sources, f.Source), Layer: key(kindLayers, f.Layer),
		MinSize: size(f.MinSizeGB), Sort: key(sorts, f.Sort), Offset: int32(o), Limit: PageSize, Lang: lang}
	switch f.Kind {
	case "unknown":
		tr.UnknownKind = true
	case "":
	default:
		tr.Kind = key(kinds, f.Kind)
	}
	if f.Matched != "" {
		tr.Matched = new(f.Matched == "yes")
	}
	tr.Voice, tr.AudioLanguage = apiVoice(f.VoiceLang, f.Voice)
	res, err := s.cfg.Browse.Torrents(req.Context(), connect.NewRequest(tr))
	if err != nil {
		s.fail(w, req, "torrents", err)
		return
	}
	rows, total := res.Msg.GetRows(), res.Msg.GetTotal()
	results := ui.Results{Offset: o, Shown: len(rows), Total: total}
	vdef := ui.DefaultVoiceLang(s.tags, lang)
	results.PrevURL, results.NextURL = pager(o, len(rows), total, func(o int) string {
		return link("/torrents", o, "q", q, "", "kind", f.Kind, "", "matched", f.Matched, "", "quality", f.Quality, "", "vl", f.VoiceLang, vdef,
			"voice", f.Voice, "any", "source", f.Source, "", "layer", f.Layer, "", "min", f.MinSizeGB, "", "sort", f.Sort, "peers")
	})
	for _, x := range rows {
		r := x.GetRelease()
		row := ui.Row{
			InfoHash:  hex.EncodeToString(r.GetInfohash()),
			Name:      r.GetName(),
			Work:      summary(x.GetWork()),
			Peers:     r.GetPeersWeek(),
			Size:      r.GetSize(),
			Kind:      uiOr(kinds, r.GetKind(), "other"),
			Layer:     uiOr(kindLayers, r.GetLayer(), "none"),
			Quality:   qualities[r.GetQuality()],
			Audio:     s.releaseAudio(r.GetAudio()),
			Source:    uiOr(sources, r.GetSource(), "none"),
			FirstSeen: r.GetFirstSeen().AsTime(),
			LastSeen:  r.GetLastSeen().AsTime(),
		}
		if r.GetLayer() == sniffarrv1.Layer_LAYER_CLASSIFIER {
			row.Confidence = new(float64(r.GetKindConfidence()))
		}
		results.Rows = append(results.Rows, row)
	}
	s.render(w, req, "torrents", ui.TorrentsPage{Base: s.base(req, lang, "torrents"), Q: q, Filters: f, Results: results})
}

// top serves GET /top: the movies or series with the most peers over a
// window. A kind that has no top is movie; all=1 lists every work instead
// of the first topShown.
func (s *server) top(w http.ResponseWriter, req *http.Request) {
	qs := req.URL.Query()
	lang := s.lang(w, req)
	kind := cmp.Or(oneOf(qs.Get("kind"), ui.TopKinds), ui.TopKinds[0])
	win := cmp.Or(oneOf(qs.Get("win"), ui.Windows), "7d")
	vl, mode := s.voice(qs, lang)
	all := qs.Get("all") == "1"
	limit := topShown
	if all {
		limit = topSize
	}
	v, _ := apiVoice(vl, mode)
	res, err := s.cfg.Browse.Top(req.Context(), connect.NewRequest(&sniffarrv1.TopRequest{Kind: key(kinds, kind), Window: key(windows, win), Voice: v,
		VoiceLanguage: vl, Limit: int32(limit), Lang: lang}))
	if err != nil {
		s.fail(w, req, "top", err)
		return
	}
	page := ui.TopPage{Base: s.base(req, lang, "top"), Kind: kind, Window: win, VoiceLang: vl, VoiceMode: mode, Limit: topSize, All: all}
	if at := res.Msg.GetComputedAt(); at != nil {
		page.Computed = at.AsTime()
	}
	for _, e := range res.Msg.GetEntries() {
		wk := ui.Work{WorkSummary: *summary(e.GetWork()), Audio: workAudio(e.GetAudio())}
		wk.Rank, wk.Peers = int(e.GetRank()), e.GetPeers()
		if e.Delta != nil {
			wk.Delta = new(float64(e.GetDelta()))
		}
		if e.FirstSeen != nil {
			wk.FirstSeen = e.GetFirstSeen().AsTime()
		}
		for _, src := range e.GetSources() {
			if k, ok := sources[src]; ok {
				wk.Sources = append(wk.Sources, k)
			}
		}
		page.Works = append(page.Works, wk)
	}
	s.render(w, req, "top", page)
}

func (s *server) torrent(w http.ResponseWriter, req *http.Request) {
	raw, err := hex.DecodeString(req.PathValue("infohash"))
	if err != nil || len(raw) != infohashLen {
		http.NotFound(w, req)
		return
	}
	lang := s.lang(w, req)
	card, err := s.cfg.Browse.Card(req.Context(), connect.NewRequest(&sniffarrv1.CardRequest{Infohash: raw, Lang: lang}))
	if connect.CodeOf(err) == connect.CodeNotFound {
		http.NotFound(w, req)
		return
	}
	if err != nil {
		s.fail(w, req, "card", err)
		return
	}
	c, r := card.Msg, card.Msg.GetRelease()
	h := hex.EncodeToString(raw)
	t := ui.Torrent{
		InfoHash:      h,
		Name:          r.GetName(),
		Magnet:        "magnet:?xt=urn:btih:" + h,
		Size:          r.GetSize(),
		FileCount:     int(r.GetFileCount()),
		FirstSeen:     r.GetFirstSeen().AsTime(),
		LastSeen:      r.GetLastSeen().AsTime(),
		DiscoveredVia: discoveries[c.GetDiscovery()],
		Kind:          ui.Answer{Value: uiOr(kinds, r.GetKind(), "other"), Layer: uiOr(kindLayers, r.GetLayer(), "none")},
		Source:        ui.Answer{Value: uiOr(sources, r.GetSource(), "none"), Layer: "none"},
		Peers:         ui.PeerCounts{Day: c.GetPeers().GetDay(), Week: c.GetPeers().GetWeek(), Month: c.GetPeers().GetMonth()},
		Work:          summary(c.GetWork()),
		TMDB:          tmdbWork(r.GetTmdb()),
		TMDBQuery:     title(r.GetName()),
		RankInWork:    int(c.GetRankInWork()),
	}
	if r.GetLayer() == sniffarrv1.Layer_LAYER_CLASSIFIER {
		t.Kind.Confidence = new(float64(r.GetKindConfidence()))
	}
	if r.GetSource() != sniffarrv1.VideoSource_VIDEO_SOURCE_UNSPECIFIED {
		t.Source.Layer = "classifier"
		t.Source.Confidence = new(float64(r.GetSourceConfidence()))
	}
	for _, l := range s.cfg.Languages {
		a := ui.Audio{Lang: l.Tag}
		for _, x := range r.GetAudio() {
			if x.GetLanguage() != l.Tag {
				continue
			}
			a = audioOf(x)
			if x.GetScriptHint() && len(l.Scripts) > 0 {
				a.Script = strings.ToLower(l.Scripts[0])
			}
		}
		t.Audio = append(t.Audio, a)
	}
	all := req.URL.Query().Get("files") == "all"
	files := c.GetFiles()
	t.Files = ui.FileList{InfoHash: h, Stored: len(files), All: all}
	if !all && len(files) > cardFiles {
		files = files[:cardFiles]
	}
	for _, f := range files {
		t.Files.Files = append(t.Files.Files, ui.File{Path: f.GetPath(), Size: f.GetSize()})
	}
	s.render(w, req, "torrent", ui.TorrentPage{Base: s.base(req, lang, ""), BackURL: backURL(req), Torrent: t})
}

// title is the release's title key without its prefix, or "": what the
// card's TMDB and IMDb searches look for while the title is unmatched.
func title(name string) string {
	k := names.Parse(name).Key
	if _, t, ok := strings.Cut(k, ":"); ok {
		return t
	}
	return k
}

// backURL is the page of the UI's own the request came from, or "": the
// search, the torrents, a movie's or series' page, or the top.
func backURL(req *http.Request) string {
	u, err := url.Parse(req.Referer())
	if err != nil || (u.Host != "" && u.Host != req.Host) {
		return ""
	}
	if u.Path == "/" || u.Path == "/torrents" || u.Path == "/top" || strings.HasPrefix(u.Path, "/w/") {
		return u.RequestURI()
	}
	return ""
}

// statDays is how many days of new torrents the data page counts: 26 weeks.
const statDays = 26 * 7

func (s *server) data(w http.ResponseWriter, req *http.Request) {
	now := s.cfg.Now()
	res, err := s.cfg.Browse.Stats(req.Context(), connect.NewRequest(&sniffarrv1.StatsRequest{Days: statDays}))
	if err != nil {
		s.fail(w, req, "stats", err)
		return
	}
	st := res.Msg
	days := st.GetDays()
	if len(days) < statDays {
		days = append(days, make([]int64, statDays-len(days))...)
	}
	out := ui.Stats{
		At:         now,
		Total:      st.GetTotal(),
		NewDay:     days[0],
		Classifier: ui.ClassifierStats{Pending: st.GetPending(), DoneDay: st.GetTokensUsedDay(), LimitDay: st.GetTokensLimitDay()},
	}
	for _, n := range days[:7] {
		out.NewWeek += n
	}
	byKind := map[string]int64{}
	for _, c := range st.GetKinds() {
		byKind[uiOr(kinds, c.GetKind(), "")] += c.GetCount()
	}
	for _, k := range ui.Kinds {
		out.ByKind = append(out.ByKind, ui.Count{Key: k, N: byKind[k]})
	}
	byLayer := map[string]int64{}
	for _, c := range st.GetLayers() {
		byLayer[uiOr(kindLayers, c.GetLayer(), "none")] += c.GetCount()
	}
	for _, k := range ui.Layers {
		out.ByLayer = append(out.ByLayer, ui.Count{Key: k, N: byLayer[k]})
	}
	bySource := map[string]int64{}
	for _, c := range st.GetSources() {
		bySource[uiOr(sources, c.GetSource(), "none")] += c.GetCount()
	}
	for _, k := range ui.Sources {
		out.BySource = append(out.BySource, ui.Count{Key: k, N: bySource[k]})
	}
	audio := map[string]*sniffarrv1.AudioCounts{}
	for _, a := range st.GetAudio() {
		audio[a.GetLanguage()] = a
	}
	for _, tag := range s.tags {
		a := audio[tag]
		out.Voice = append(out.Voice, ui.VoiceStats{Lang: tag, Bands: [4]int64{a.GetMarker(), a.GetLikely(), a.GetUnlikely(), st.GetTotal() - a.GetMarker() - a.GetLikely() - a.GetUnlikely()}})
	}
	for _, c := range st.GetRejected() {
		k, ok := drops[c.GetReason()]
		if !ok {
			continue
		}
		out.Dropped = append(out.Dropped, ui.Count{Key: k, N: c.GetCount()})
		out.DroppedTotal += c.GetCount()
	}
	slices.SortStableFunc(out.Dropped, func(a, b ui.Count) int {
		return cmp.Or(cmp.Compare(b.N, a.N), cmp.Compare(slices.Index(ui.DropReasons, a.Key), slices.Index(ui.DropReasons, b.Key)))
	})
	out.Daily, out.Weekly = s.charts(now, days)
	s.render(w, req, "data", ui.DataPage{Base: s.base(req, s.lang(w, req), "data"), Stats: out})
}

// charts are the bar charts of the last 30 days and of the 26 weeks in days,
// counts of the 24-hour periods before now, the latest first.
func (s *server) charts(now time.Time, days []int64) (daily, weekly ui.Chart) {
	label := func(ago int) string { return now.AddDate(0, 0, -ago).In(s.cfg.Location).Format("02.01") }
	var values []int64
	var labels []string
	for i := 29; i >= 0; i-- {
		values = append(values, days[i])
		labels = append(labels, label(i))
	}
	daily = ui.NewBarChart(values, labels, 7)
	values, labels = nil, nil
	for wk := len(days)/7 - 1; wk >= 0; wk-- {
		var n int64
		for _, d := range days[wk*7 : wk*7+7] {
			n += d
		}
		values = append(values, n)
		labels = append(labels, label(wk*7))
	}
	weekly = ui.NewBarChart(values, labels, 4)
	return daily, weekly
}
