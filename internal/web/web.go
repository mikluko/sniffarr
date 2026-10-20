// Package web serves the web UI over the store: search, a release's card,
// and the store's counts. It authenticates nobody; that is left to a proxy
// in front of it.
package web

import (
	"cmp"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mikluko/sniffarr/internal/audio"
	"github.com/mikluko/sniffarr/internal/names"
	"github.com/mikluko/sniffarr/internal/rules"
	"github.com/mikluko/sniffarr/internal/store"
	"github.com/mikluko/sniffarr/internal/ui"
)

// TokenHeader carries the access token of the signed-in person, as
// oauth2-proxy passes it on.
const TokenHeader = "X-Forwarded-Access-Token"

// PageSize is how many releases a search page lists.
const PageSize = 50

// cardFiles is how many files a card lists until all are asked for.
const cardFiles = 8

// Config is what New needs.
type Config struct {
	Store *store.Store
	// Languages are the configured audio languages, in their configured
	// order.
	Languages []audio.Language
	// DailyTokens is the classifier's daily token limit; 0 is none.
	DailyTokens int64
	// Location is the time zone of dates; time.Local when nil.
	Location *time.Location
	// Log discards everything when nil.
	Log *slog.Logger
	// Now is time.Now when nil.
	Now func() time.Time
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
	mux.HandleFunc("GET /{$}", s.search)
	mux.HandleFunc("GET /t/{infohash}", s.torrent)
	mux.HandleFunc("GET /data", s.data)
	return mux, nil
}

// Locale returns the locale claim of the JWT in TokenHeader, or "". The
// token is not verified: the proxy that sets the header is trusted.
func Locale(req *http.Request) string {
	parts := strings.Split(req.Header.Get(TokenHeader), ".")
	if len(parts) != 3 {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		Locale string `json:"locale"`
	}
	if json.Unmarshal(b, &claims) != nil {
		return ""
	}
	return claims.Locale
}

func (s *server) base(w http.ResponseWriter, req *http.Request, nav string) ui.Base {
	b := s.ui.Base(req, ui.Lang(w, req, Locale(req)), nav, s.tags, s.cfg.Location)
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

var (
	kindLayers = map[string]string{store.LayerRules: "rules", store.LayerReuse: "title_repeat", store.LayerJev: "classifier"}
	sources    = map[string]string{"disc_image": "disc", "remux": "remux", "encode": "rip", "web": "web", "other": "other"}
	drops      = map[rules.Reason]string{rules.Adult: "adult", rules.Software: "software", rules.Book: "book", rules.Course: "course",
		rules.Script: "script", rules.NoMedia: "filetype", rules.Small: "size"}
	hidden = []string{"adult"}
)

// uiKind is the UI's key of a stored kind.
func uiKind(k string) string {
	if k == "" {
		return "unknown"
	}
	return k
}

// uiOr returns m's value for k, or "none".
func uiOr(m map[string]string, k string) string {
	if v, ok := m[k]; ok {
		return v
	}
	return "none"
}

// key returns the key of m whose value is v, or "".
func key[K comparable](m map[K]string, v string) (K, bool) {
	for k, x := range m {
		if x == v {
			return k, true
		}
	}
	var zero K
	return zero, false
}

// filters reads the search filters from the query, keeping valid values only.
func (s *server) filters(q url.Values) ui.Filters {
	f := ui.Filters{VoiceLang: q.Get("vl"), Voice: q.Get("voice"), MinSizeGB: strings.TrimSpace(q.Get("min"))}
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
	return f
}

// minSize is the size in bytes of a size in GiB as typed, with a point or a
// comma; 0 when it is not a positive number.
func minSize(gb string) int64 {
	v, err := strconv.ParseFloat(strings.Replace(gb, ",", ".", 1), 64)
	if err != nil || v <= 0 || math.IsInf(v, 0) {
		return 0
	}
	return int64(math.Min(v*(1<<30), math.MaxInt64))
}

// searchURL is the search page's URL for the query, filters and offset.
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
	if offset > 0 {
		v.Set("o", strconv.Itoa(offset))
	}
	if len(v) == 0 {
		return "/"
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
	b := store.Browse{Text: q, AudioLanguage: f.VoiceLang, MinSize: minSize(f.MinSizeGB), Offset: offset, Limit: PageSize}
	switch f.Kind {
	case "":
		b.Exclude = hidden
	case "unknown":
		b.UnknownKind = true
	default:
		b.Kind = f.Kind
	}
	if f.VoiceLang != "" {
		switch f.Voice {
		case "p50":
			b.MinAudioProbability = new(0.5)
		case "explicit":
			b.AudioMarker = true
		}
	}
	if f.Source != "" {
		b.Source, _ = key(sources, f.Source)
	}
	if f.Layer != "" {
		b.KindLayer, _ = key(kindLayers, f.Layer)
	}
	releases, total, err := s.cfg.Store.Page(req.Context(), b)
	if err != nil {
		s.fail(w, req, "search", err)
		return
	}
	voiceDefault := ""
	if len(s.tags) > 0 {
		voiceDefault = s.tags[0]
	}
	res := ui.Results{
		Fresh:   q == "" && offset == 0,
		SelfURL: searchURL(q, f, offset, voiceDefault),
		Offset:  offset,
		Shown:   len(releases),
		Total:   total,
	}
	if offset > 0 {
		res.PrevURL = searchURL(q, f, max(0, offset-PageSize), voiceDefault)
	}
	if int64(offset+len(releases)) < total {
		res.NextURL = searchURL(q, f, offset+PageSize, voiceDefault)
	}
	for _, r := range releases {
		row := ui.Row{
			InfoHash:  hex.EncodeToString(r.Hash[:]),
			Name:      r.Name,
			Size:      r.Size,
			Kind:      uiKind(r.Kind),
			Layer:     uiOr(kindLayers, r.KindLayer),
			Source:    uiOr(sources, r.Source),
			FirstSeen: r.FirstSeen,
			LastSeen:  r.LastSeen,
		}
		if r.KindLayer == store.LayerJev {
			row.Confidence = new(r.KindConfidence)
		}
		for _, tag := range s.tags {
			var c ui.VoiceCell
			if a, ok := audioOf(r, tag); ok {
				c.Explicit = a.Marker
				if !a.Marker {
					c.Prob = a.Probability
				}
			}
			row.Voice = append(row.Voice, c)
		}
		res.Rows = append(res.Rows, row)
	}
	s.render(w, req, "search", ui.SearchPage{Base: s.base(w, req, "search"), Q: q, Filters: f, Results: res})
}

func audioOf(r store.Release, tag string) (store.Audio, bool) {
	for _, a := range r.Audio {
		if a.Language == tag {
			return a, true
		}
	}
	return store.Audio{}, false
}

func (s *server) torrent(w http.ResponseWriter, req *http.Request) {
	raw, err := hex.DecodeString(req.PathValue("infohash"))
	if err != nil || len(raw) != len(store.Hash{}) {
		http.NotFound(w, req)
		return
	}
	c, err := s.cfg.Store.Card(req.Context(), store.Hash(raw), math.MaxInt32)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, req)
		return
	}
	if err != nil {
		s.fail(w, req, "card", err)
		return
	}
	h := hex.EncodeToString(raw)
	t := ui.Torrent{
		InfoHash:      h,
		Name:          c.Name,
		Magnet:        "magnet:?xt=urn:btih:" + h,
		Size:          c.Size,
		FileCount:     int(c.FileCount),
		FirstSeen:     c.FirstSeen,
		LastSeen:      c.LastSeen,
		DiscoveredVia: c.Discovery,
		TMDBQuery:     title(c.Name),
		Kind:          ui.Answer{Value: uiKind(c.Kind), Layer: uiOr(kindLayers, c.KindLayer)},
		Source:        ui.Answer{Value: uiOr(sources, c.Source), Layer: "none"},
	}
	if c.KindLayer == store.LayerJev {
		t.Kind.Confidence = new(c.KindConfidence)
	}
	if c.Source != "" {
		t.Source.Layer = "classifier"
		t.Source.Confidence = new(c.SourceConfidence)
	}
	for _, l := range s.cfg.Languages {
		d := ui.VoiceDetail{Lang: l.Tag}
		if a, ok := audioOf(c.Release, l.Tag); ok {
			if a.Marker {
				d.Mark = l.Mark(c.Name, c.Files)
			}
			if a.ScriptHint && len(l.Scripts) > 0 {
				d.ScriptHint = strings.ToLower(l.Scripts[0])
			}
			if a.Layer == store.LayerJev {
				d.Prob = a.Probability
			}
		}
		t.Voice = append(t.Voice, d)
	}
	all := req.URL.Query().Get("files") == "all"
	files := c.Files
	if !all && len(files) > cardFiles {
		files = files[:cardFiles]
	}
	t.Files = ui.FileList{InfoHash: h, Stored: c.Stored, All: all}
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
	ctx := req.Context()
	now := s.cfg.Now()
	st, err := s.cfg.Store.Stats(ctx, now, statDays)
	if err != nil {
		s.fail(w, req, "stats", err)
		return
	}
	used, err := s.cfg.Store.TokensUsed(ctx, now.UTC().Format(time.DateOnly))
	if err != nil {
		s.fail(w, req, "tokens used", err)
		return
	}
	out := ui.Stats{
		At:         now,
		Total:      st.Total,
		NewDay:     st.Days[0],
		Classifier: ui.ClassifierStats{Pending: st.Pending, DoneDay: used, LimitDay: s.cfg.DailyTokens},
	}
	for _, n := range st.Days[:7] {
		out.NewWeek += n
	}
	for _, k := range ui.Kinds {
		stored := k
		if k == "unknown" {
			stored = ""
		}
		out.ByKind = append(out.ByKind, ui.Count{Key: k, N: st.Kinds[stored]})
	}
	for _, k := range ui.Layers {
		stored, _ := key(kindLayers, k)
		out.ByLayer = append(out.ByLayer, ui.Count{Key: k, N: st.Layers[stored]})
	}
	for _, k := range ui.Sources {
		stored, _ := key(sources, k)
		out.BySource = append(out.BySource, ui.Count{Key: k, N: st.Sources[stored]})
	}
	for _, tag := range s.tags {
		a := st.Audio[tag]
		out.Voice = append(out.Voice, ui.VoiceStats{Lang: tag, Bands: [4]int64{a.Marker, a.Likely, a.Unlikely, st.Total - a.Marker - a.Likely - a.Unlikely}})
	}
	for reason, n := range st.Rejected {
		k, ok := drops[rules.Reason(reason)]
		if !ok {
			continue
		}
		out.Dropped = append(out.Dropped, ui.Count{Key: k, N: n})
		out.DroppedTotal += n
	}
	slices.SortStableFunc(out.Dropped, func(a, b ui.Count) int {
		return cmp.Or(cmp.Compare(b.N, a.N), cmp.Compare(slices.Index(ui.DropReasons, a.Key), slices.Index(ui.DropReasons, b.Key)))
	})
	out.Daily, out.Weekly = s.charts(now, st.Days)
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
