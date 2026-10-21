// Package web serves the web UI as a client of the browse API: the home
// page, search, the tops of works, a release's card, and the store's counts.
// It authenticates nobody; that is left to a proxy in front of it.
package web

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
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
	"github.com/mikluko/sniffarr/internal/rules"
	"github.com/mikluko/sniffarr/internal/ui"
)

// TokenHeader carries the access token of the signed-in person, as
// oauth2-proxy passes it on.
const TokenHeader = "X-Forwarded-Access-Token"

// PageSize is how many releases a search page lists.
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
	// ObtainLanguages returns them; none leaves the voice filter, columns
	// and card block out of every page.
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
	mux.HandleFunc("GET /top", s.top)
	mux.HandleFunc("GET /top/w/{id}", s.work)
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

// The UI's keys of the API's enumerations; a value absent here is "none".
var (
	kinds = map[sniffarrv1.Kind]string{sniffarrv1.Kind_KIND_UNSPECIFIED: "unknown", sniffarrv1.Kind_KIND_MOVIE: "movie", sniffarrv1.Kind_KIND_SERIES: "series",
		sniffarrv1.Kind_KIND_MUSIC: "music", sniffarrv1.Kind_KIND_BOOK: "book", sniffarrv1.Kind_KIND_SOFTWARE: "software", sniffarrv1.Kind_KIND_ADULT: "adult",
		sniffarrv1.Kind_KIND_OTHER: "other"}
	kindLayers = map[sniffarrv1.Layer]string{sniffarrv1.Layer_LAYER_RULES: "rules", sniffarrv1.Layer_LAYER_REUSE: "title_repeat", sniffarrv1.Layer_LAYER_CLASSIFIER: "classifier"}
	sources    = map[sniffarrv1.VideoSource]string{sniffarrv1.VideoSource_VIDEO_SOURCE_DISC_IMAGE: "disc", sniffarrv1.VideoSource_VIDEO_SOURCE_REMUX: "remux",
		sniffarrv1.VideoSource_VIDEO_SOURCE_ENCODE: "rip", sniffarrv1.VideoSource_VIDEO_SOURCE_WEB: "web", sniffarrv1.VideoSource_VIDEO_SOURCE_OTHER: "other"}
	drops = map[sniffarrv1.Rejection]string{sniffarrv1.Rejection_REJECTION_ADULT: "adult", sniffarrv1.Rejection_REJECTION_SOFTWARE: "software",
		sniffarrv1.Rejection_REJECTION_BOOK: "book", sniffarrv1.Rejection_REJECTION_COURSE: "course", sniffarrv1.Rejection_REJECTION_SCRIPT: "script",
		sniffarrv1.Rejection_REJECTION_NO_MEDIA: "filetype", sniffarrv1.Rejection_REJECTION_SMALL: "size"}
	discoveries = map[sniffarrv1.Source]string{sniffarrv1.Source_SOURCE_ANNOUNCE: "announce", sniffarrv1.Source_SOURCE_SAMPLE: "sample"}
	sorts       = map[sniffarrv1.Sort]string{sniffarrv1.Sort_SORT_PEERS: "peers", sniffarrv1.Sort_SORT_LAST_SEEN: "last_seen", sniffarrv1.Sort_SORT_SIZE: "size"}
	windows     = map[sniffarrv1.Window]string{sniffarrv1.Window_WINDOW_DAY: "24h", sniffarrv1.Window_WINDOW_WEEK: "7d", sniffarrv1.Window_WINDOW_MONTH: "30d"}
	voices      = map[sniffarrv1.Voice]string{sniffarrv1.Voice_VOICE_UNSPECIFIED: "any", sniffarrv1.Voice_VOICE_LIKELY: "p50", sniffarrv1.Voice_VOICE_MARKER: "explicit"}
	tmdbMethods = map[sniffarrv1.TMDBLayer]string{sniffarrv1.TMDBLayer_TMDB_LAYER_SEARCH: "search", sniffarrv1.TMDBLayer_TMDB_LAYER_CLASSIFIER: "classifier"}
)

// tmdbWork is the UI's view of a title's TMDB lookup: nil while unchecked,
// unmatched when nothing matched, else the match.
func tmdbWork(t *sniffarrv1.TMDBWork) *ui.TMDBWork {
	if t == nil {
		return nil
	}
	out := &ui.TMDBWork{State: ui.TMDBUnmatched, CheckedAt: t.GetCheckedAt().AsTime()}
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

// uiOr returns m's value for k, or "none".
func uiOr[K comparable](m map[K]string, k K) string {
	if v, ok := m[k]; ok {
		return v
	}
	return "none"
}

// key returns the key of m whose value is v, or the zero key.
func key[K comparable](m map[K]string, v string) (K, bool) {
	for k, x := range m {
		if x == v {
			return k, true
		}
	}
	var zero K
	return zero, false
}

// searchKeys are the query parameters whose presence makes GET / a search.
var searchKeys = []string{"q", "kind", "vl", "voice", "source", "layer", "min", "sort", "o"}

// index serves GET /: the home page, or the search once any of searchKeys is
// in the query.
func (s *server) index(w http.ResponseWriter, req *http.Request) {
	qs := req.URL.Query()
	for _, k := range searchKeys {
		if qs.Has(k) {
			s.search(w, req)
			return
		}
	}
	s.home(w, req)
}

func (s *server) home(w http.ResponseWriter, req *http.Request) {
	res, err := s.cfg.Browse.Home(req.Context(), connect.NewRequest(&sniffarrv1.HomeRequest{}))
	if err != nil {
		s.fail(w, req, "home", err)
		return
	}
	h := res.Msg
	s.render(w, req, "home", ui.HomePage{Base: s.base(req, s.lang(w, req), "home"), Summary: ui.Summary{Total: h.GetTotal(), NewDay: h.GetNewDay(), Pending: h.GetPending()}})
}

// filters reads the search filters from the query, keeping valid values only;
// without audio languages the voice filter is "any".
func (s *server) filters(q url.Values) ui.Filters {
	f := ui.Filters{VoiceLang: q.Get("vl"), Voice: q.Get("voice"), MinSizeGB: strings.TrimSpace(q.Get("min")), Sort: q.Get("sort")}
	if k := q.Get("kind"); slices.Contains(ui.Kinds, k) {
		f.Kind = k
	}
	if v := q.Get("source"); slices.Contains(ui.Sources, v) {
		f.Source = v
	}
	if v := q.Get("layer"); slices.Contains(ui.Layers, v) {
		f.Layer = v
	}
	if !slices.Contains(s.tags, f.VoiceLang) {
		f.VoiceLang = ""
		if len(s.tags) > 0 {
			f.VoiceLang = s.tags[0]
		}
	}
	if f.VoiceLang == "" || !slices.Contains(ui.VoiceModes, f.Voice) {
		f.Voice = "any"
	}
	if !slices.Contains(ui.Sorts, f.Sort) {
		f.Sort = defaultSort
	}
	return f
}

// defaultSort is the order of a search that names none.
const defaultSort = "peers"

// minSize is the size in bytes of a size in GiB as typed, with a point or a
// comma; 0 when it is not a positive number.
func minSize(gb string) int64 {
	v, err := strconv.ParseFloat(strings.Replace(gb, ",", ".", 1), 64)
	if err != nil || v <= 0 || math.IsInf(v, 0) {
		return 0
	}
	return int64(math.Min(v*(1<<30), math.MaxInt64))
}

// searchURL is the search page's URL for the query, filters and offset; it
// carries q even empty, since GET / without a search parameter is the home
// page.
func searchURL(q string, f ui.Filters, offset int, voiceDefault string) string {
	v := url.Values{}
	set := func(k, x, def string) {
		if x != "" && x != def {
			v.Set(k, x)
		}
	}
	set("q", q, "")
	set("kind", f.Kind, "")
	set("vl", f.VoiceLang, voiceDefault)
	set("voice", f.Voice, "any")
	set("source", f.Source, "")
	set("layer", f.Layer, "")
	set("min", f.MinSizeGB, "")
	set("sort", f.Sort, defaultSort)
	if offset > 0 {
		v.Set("o", strconv.Itoa(offset))
	}
	if len(v) == 0 {
		return "/?q="
	}
	return "/?" + v.Encode()
}

func (s *server) search(w http.ResponseWriter, req *http.Request) {
	qs := req.URL.Query()
	q := strings.TrimSpace(qs.Get("q"))
	f := s.filters(qs)
	offset, err := strconv.Atoi(qs.Get("o"))
	if err != nil || offset < 0 {
		offset = 0
	}
	lang := s.lang(w, req)
	b := &sniffarrv1.PageRequest{Query: q, MinSize: minSize(f.MinSizeGB), Offset: int32(offset), Limit: PageSize, Lang: lang}
	switch f.Kind {
	case "":
	case "unknown":
		b.UnknownKind = true
	default:
		b.Kind, _ = key(kinds, f.Kind)
	}
	if f.VoiceLang != "" {
		switch f.Voice {
		case "p50":
			b.AudioLanguage, b.MinAudioProbability = &f.VoiceLang, new(float32(0.5))
		case "explicit":
			b.AudioLanguage, b.AudioMarker = &f.VoiceLang, true
		}
	}
	if f.Source != "" {
		b.Source, _ = key(sources, f.Source)
	}
	if f.Layer != "" {
		b.Layer, _ = key(kindLayers, f.Layer)
	}
	b.Sort, _ = key(sorts, f.Sort)
	page, err := s.cfg.Browse.Page(req.Context(), connect.NewRequest(b))
	if err != nil {
		s.fail(w, req, "search", err)
		return
	}
	releases, total := page.Msg.GetReleases(), page.Msg.GetTotal()
	voiceDefault := ""
	if len(s.tags) > 0 {
		voiceDefault = s.tags[0]
	}
	res := ui.Results{Offset: offset, Shown: len(releases), Total: total}
	if offset > 0 {
		res.PrevURL = searchURL(q, f, max(0, offset-PageSize), voiceDefault)
	}
	if int64(offset+len(releases)) < total {
		res.NextURL = searchURL(q, f, offset+PageSize, voiceDefault)
	}
	for _, r := range releases {
		row := ui.Row{
			InfoHash:  hex.EncodeToString(r.GetInfohash()),
			Name:      r.GetName(),
			Peers:     r.GetPeersWeek(),
			Size:      r.GetSize(),
			Kind:      uiOr(kinds, r.GetKind()),
			Layer:     uiOr(kindLayers, r.GetLayer()),
			Source:    uiOr(sources, r.GetSource()),
			FirstSeen: r.GetFirstSeen().AsTime(),
			LastSeen:  r.GetLastSeen().AsTime(),
		}
		if r.GetLayer() == sniffarrv1.Layer_LAYER_CLASSIFIER {
			row.Confidence = new(float64(r.GetKindConfidence()))
		}
		row.Voice = s.voiceCells(r.GetAudio())
		res.Rows = append(res.Rows, row)
	}
	var works []ui.WorkHit
	for _, m := range page.Msg.GetWorks() {
		works = append(works, ui.WorkHit{Ref: workRef(m.GetWork()), Peers: m.GetPeers(), Releases: int(m.GetReleases()), TMDB: tmdbWork(m.GetTmdb())})
	}
	s.render(w, req, "search", ui.SearchPage{Base: s.base(req, lang, "search"), Q: q, Filters: f, Works: works, Results: res})
}

// audioOf is the audio entry for the language, or nil.
func audioOf(audio []*sniffarrv1.Audio, tag string) *sniffarrv1.Audio {
	for _, a := range audio {
		if a.GetLanguage() == tag {
			return a
		}
	}
	return nil
}

// voiceCells are the dubbing cells of the configured languages, in order.
func (s *server) voiceCells(audio []*sniffarrv1.Audio) []ui.VoiceCell {
	cells := make([]ui.VoiceCell, len(s.tags))
	for i, tag := range s.tags {
		if a := audioOf(audio, tag); a != nil {
			cells[i].Explicit = a.GetMarker()
			if !a.GetMarker() {
				cells[i].Prob = probability(a)
			}
		}
	}
	return cells
}

// workID is the UI's id of a work key: base64url, so that it fits a path
// segment and an HTML id whatever the key holds.
func workID(key string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(key))
}

// workKey is the work key of a UI id, or false for an id workID did not make.
func workKey(id string) (string, bool) {
	b, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil || len(b) == 0 {
		return "", false
	}
	return string(b), true
}

// workRef is the UI's name of a work. Sub is the year of a movie and the
// season of a series as "S01"; a music work has none.
func workRef(w *sniffarrv1.WorkRef) ui.WorkRef {
	ref := ui.WorkRef{ID: workID(w.GetKey()), Kind: uiOr(kinds, w.GetKind()), Title: w.GetTitle(), Rank: int(w.GetRank())}
	switch {
	case ref.Kind == "movie" && w.GetYear() > 0:
		ref.Sub = strconv.Itoa(int(w.GetYear()))
	case ref.Kind == "series" && w.GetSeason() > 0:
		ref.Sub = fmt.Sprintf("S%02d", w.GetSeason())
	}
	return ref
}

// uiWork is the UI's row of a work, without its place and peers.
func (s *server) uiWork(w *sniffarrv1.Work) ui.Work {
	out := ui.Work{Ref: workRef(w.GetRef()), Releases: int(w.GetReleases()), Voice: s.voiceCells(w.GetAudio()), FirstSeen: w.GetFirstSeen().AsTime(), TMDB: tmdbWork(w.GetTmdb())}
	for _, src := range w.GetSources() {
		if k, ok := sources[src]; ok {
			out.Sources = append(out.Sources, k)
		}
	}
	return out
}

// topQuery reads the window and voice of a top from the query; a value that
// is not one of the UI's is the default, 7d and any.
func topQuery(q url.Values) (win, voice string, window sniffarrv1.Window, v sniffarrv1.Voice) {
	win, voice = q.Get("win"), q.Get("voice")
	if !slices.Contains(ui.Windows, win) {
		win = "7d"
	}
	if !slices.Contains(ui.VoiceModes, voice) {
		voice = "any"
	}
	window, _ = key(windows, win)
	v, _ = key(voices, voice)
	return win, voice, window, v
}

// top serves GET /top: the works of a kind with the most peers over a
// window. A kind that has no top is movie; all=1 lists every work instead of
// the first topShown.
func (s *server) top(w http.ResponseWriter, req *http.Request) {
	qs := req.URL.Query()
	kind := qs.Get("kind")
	if !slices.Contains(ui.TopKinds, kind) {
		kind = ui.TopKinds[0]
	}
	win, voice, window, v := topQuery(qs)
	all := qs.Get("all") == "1"
	limit := topShown
	if all {
		limit = topSize
	}
	pk, _ := key(kinds, kind)
	lang := s.lang(w, req)
	res, err := s.cfg.Browse.Top(req.Context(), connect.NewRequest(&sniffarrv1.TopRequest{Kind: pk, Window: window, Voice: v, Limit: int32(limit), Lang: lang}))
	if err != nil {
		s.fail(w, req, "top", err)
		return
	}
	page := ui.TopPage{Base: s.base(req, lang, "top"), Kind: kind, Window: win, VoiceMode: voice, Limit: topSize, All: all}
	if at := res.Msg.GetComputedAt(); at != nil {
		page.Computed = at.AsTime()
	}
	for _, e := range res.Msg.GetEntries() {
		page.Works = append(page.Works, s.topWork(e))
	}
	s.render(w, req, "top", page)
}

// topWork is the UI's row of an entry of a top.
func (s *server) topWork(e *sniffarrv1.TopEntry) ui.Work {
	wk := s.uiWork(e.GetWork())
	wk.Rank, wk.Peers = int(e.GetRank()), e.GetPeers()
	if e.Delta != nil {
		wk.Delta = new(float64(e.GetDelta()))
	}
	return wk
}

// work serves GET /top/w/{id}: one work's row of the top over the window,
// with its releases when open=1.
func (s *server) work(w http.ResponseWriter, req *http.Request) {
	k, ok := workKey(req.PathValue("id"))
	if !ok {
		http.NotFound(w, req)
		return
	}
	qs := req.URL.Query()
	win, voice, window, v := topQuery(qs)
	lang := s.lang(w, req)
	res, err := s.cfg.Browse.Work(req.Context(), connect.NewRequest(&sniffarrv1.WorkRequest{Key: k, Window: window, Voice: v, Lang: lang}))
	if connect.CodeOf(err) == connect.CodeNotFound {
		http.NotFound(w, req)
		return
	}
	if err != nil {
		s.fail(w, req, "work", err)
		return
	}
	wk := s.uiWork(res.Msg.GetWork())
	wk.Rank, wk.Peers = int(res.Msg.GetRank()), res.Msg.GetPeers()
	if res.Msg.Delta != nil {
		wk.Delta = new(float64(res.Msg.GetDelta()))
	}
	if qs.Get("open") == "1" {
		wk.Open = true
		for _, r := range res.Msg.GetReleases() {
			rel := r.GetRelease()
			wk.Items = append(wk.Items, ui.Release{InfoHash: hex.EncodeToString(rel.GetInfohash()), Name: rel.GetName(), Peers: r.GetPeers(), Size: rel.GetSize(),
				Voice: s.voiceCells(rel.GetAudio()), Source: uiOr(sources, rel.GetSource()), FirstSeen: rel.GetFirstSeen().AsTime()})
		}
	}
	kind := wk.Ref.Kind
	if !slices.Contains(ui.TopKinds, kind) {
		kind = ui.TopKinds[0]
	}
	s.render(w, req, "top", ui.TopPage{Base: s.base(req, lang, "top"), Kind: kind, Window: win, VoiceMode: voice, Limit: topSize, All: true, Works: []ui.Work{wk}})
}

// probability is the classifier's probability of an audio track, or nil
// while unknown.
func probability(a *sniffarrv1.Audio) *float64 {
	if a.Probability == nil {
		return nil
	}
	return new(float64(a.GetProbability()))
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
		Kind:          ui.Answer{Value: uiOr(kinds, r.GetKind()), Layer: uiOr(kindLayers, r.GetLayer())},
		Source:        ui.Answer{Value: uiOr(sources, r.GetSource()), Layer: "none"},
		Peers:         ui.PeerCounts{Day: c.GetPeers().GetDay(), Week: c.GetPeers().GetWeek(), Month: c.GetPeers().GetMonth()},
		TMDB:          tmdbWork(r.GetTmdb()),
		TMDBQuery:     title(r.GetName()),
		WorkRels:      int(c.GetReleases()),
		RankInWork:    int(c.GetReleaseRank()),
	}
	if c.Work != nil {
		ref := workRef(c.GetWork())
		t.Work = &ref
	}
	if r.GetLayer() == sniffarrv1.Layer_LAYER_CLASSIFIER {
		t.Kind.Confidence = new(float64(r.GetKindConfidence()))
	}
	if r.GetSource() != sniffarrv1.VideoSource_VIDEO_SOURCE_UNSPECIFIED {
		t.Source.Layer = "classifier"
		t.Source.Confidence = new(float64(r.GetSourceConfidence()))
	}
	files := make([]rules.File, len(c.GetFiles()))
	for i, f := range c.GetFiles() {
		files[i] = rules.File{Path: f.GetPath(), Size: f.GetSize()}
	}
	for _, l := range s.cfg.Languages {
		d := ui.VoiceDetail{Lang: l.Tag}
		if a := audioOf(r.GetAudio(), l.Tag); a != nil {
			if a.GetMarker() {
				d.Mark = l.Mark(r.GetName(), files)
			} else {
				d.Prob = probability(a)
			}
			if a.GetScriptHint() && len(l.Scripts) > 0 {
				d.ScriptHint = strings.ToLower(l.Scripts[0])
			}
		}
		t.Voice = append(t.Voice, d)
	}
	all := req.URL.Query().Get("files") == "all"
	t.Files = ui.FileList{InfoHash: h, Stored: len(files), All: all}
	if !all && len(files) > cardFiles {
		files = files[:cardFiles]
	}
	for _, f := range files {
		t.Files.Files = append(t.Files.Files, ui.File{Path: f.Path, Size: f.Size})
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

// backURL is the search page the request came from, or "".
func backURL(req *http.Request) string {
	u, err := url.Parse(req.Referer())
	if err != nil || u.Path != "/" || (u.Host != "" && u.Host != req.Host) {
		return ""
	}
	return u.RequestURI()
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
		byKind[uiOr(kinds, c.GetKind())] += c.GetCount()
	}
	for _, k := range ui.Kinds {
		out.ByKind = append(out.ByKind, ui.Count{Key: k, N: byKind[k]})
	}
	byLayer := map[string]int64{}
	for _, c := range st.GetLayers() {
		byLayer[uiOr(kindLayers, c.GetLayer())] += c.GetCount()
	}
	for _, k := range ui.Layers {
		out.ByLayer = append(out.ByLayer, ui.Count{Key: k, N: byLayer[k]})
	}
	bySource := map[string]int64{}
	for _, c := range st.GetSources() {
		bySource[uiOr(sources, c.GetSource())] += c.GetCount()
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
