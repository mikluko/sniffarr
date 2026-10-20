// Package web serves the web UI as a client of the browse API: the home
// page, search, a release's card, and the store's counts. It authenticates
// nobody; that is left to a proxy in front of it.
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

// infohashLen is the length of a BitTorrent v1 infohash in bytes.
const infohashLen = 20

// Config is what New needs.
type Config struct {
	// Browse is the API every page is read from, with its token.
	Browse sniffarrv1connect.BrowseServiceClient
	// Languages are the configured audio languages, in their configured
	// order; they must be the API's.
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

func (s *server) base(w http.ResponseWriter, req *http.Request, nav string) ui.Base {
	b := s.ui.Base(req, ui.Lang(w, req, s.locale(req)), nav, s.tags, s.cfg.Location)
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
)

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
	res, err := s.cfg.Browse.Stats(req.Context(), connect.NewRequest(&sniffarrv1.StatsRequest{Days: 1}))
	if err != nil {
		s.fail(w, req, "stats", err)
		return
	}
	st := res.Msg
	sum := ui.Summary{Total: st.GetTotal(), Pending: st.GetPending()}
	if d := st.GetDays(); len(d) > 0 {
		sum.NewDay = d[0]
	}
	s.render(w, req, "home", ui.HomePage{Base: s.base(w, req, "home"), Summary: sum})
}

// filters reads the search filters from the query, keeping valid values only.
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
	if !slices.Contains(ui.VoiceModes, f.Voice) {
		f.Voice = "any"
	}
	if !slices.Contains(ui.Sorts, f.Sort) {
		f.Sort = defaultSort
	}
	return f
}

// defaultSort is the order of a search that names none.
const defaultSort = "last_seen"

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
	b := &sniffarrv1.PageRequest{Query: q, MinSize: minSize(f.MinSizeGB), Offset: int32(offset), Limit: PageSize}
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
		for _, tag := range s.tags {
			var c ui.VoiceCell
			if a := audioOf(r, tag); a != nil {
				c.Explicit = a.GetMarker()
				if !a.GetMarker() {
					c.Prob = probability(a)
				}
			}
			row.Voice = append(row.Voice, c)
		}
		res.Rows = append(res.Rows, row)
	}
	s.render(w, req, "search", ui.SearchPage{Base: s.base(w, req, "search"), Q: q, Filters: f, Results: res})
}

// audioOf is r's audio entry for the language, or nil.
func audioOf(r *sniffarrv1.Release, tag string) *sniffarrv1.Audio {
	for _, a := range r.GetAudio() {
		if a.GetLanguage() == tag {
			return a
		}
	}
	return nil
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
	card, err := s.cfg.Browse.Card(req.Context(), connect.NewRequest(&sniffarrv1.CardRequest{Infohash: raw}))
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
		TMDBQuery:     title(r.GetName()),
		Kind:          ui.Answer{Value: uiOr(kinds, r.GetKind()), Layer: uiOr(kindLayers, r.GetLayer())},
		Source:        ui.Answer{Value: uiOr(sources, r.GetSource()), Layer: "none"},
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
		if a := audioOf(r, l.Tag); a != nil {
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
	s.render(w, req, "torrent", ui.TorrentPage{Base: s.base(w, req, ""), BackURL: backURL(req), Torrent: t})
}

// title is the release's title key without its prefix, or "".
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
	s.render(w, req, "data", ui.DataPage{Base: s.base(w, req, "data"), Stats: out})
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
