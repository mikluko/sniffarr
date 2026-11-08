package ui

import (
	"html/template"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	libui "sarak.sh/mikluko/libarr/ui"
	"sarak.sh/mikluko/sniffarr/internal/latest"
)

// Renders every page and fragment in both languages, fails on template errors
// and untranslated keys. PREVIEW_DIR=dir writes the full pages there.
func TestRenderAll(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	libui.OnMissing = func(lang, key string) { missing = append(missing, lang+":"+key) }
	defer func() { libui.OnMissing = nil }()

	loc := time.FixedZone("CEST", 2*3600)
	now := time.Date(2026, 10, 20, 14, 5, 0, 0, loc)

	type tc struct {
		name, page, path, target string
		data                     func(Base) any
	}
	multi := func(b *Base) { b.VoiceLangs = []string{"ru", "uk"} }
	cases := []tc{
		{"torrents", "torrents", "/torrents", "", func(b Base) any { b.Nav = "torrents"; return SampleTorrents(b, "", Filters{}) }},
		{"torrents-filtered", "torrents", "/torrents?q=spider&kind=movie&quality=1080p", "", func(b Base) any {
			b.Nav = "torrents"
			return SampleTorrents(b, "spider", Filters{Kind: "movie", Quality: "1080p"})
		}},
		{"torrents-unmatched", "torrents", "/torrents?matched=no", "torrents", func(b Base) any { return SampleTorrents(b, "", Filters{Matched: "no"}) }},
		{"torrents-explicit", "torrents", "/torrents?voice=explicit", "torrents", func(b Base) any { return SampleTorrents(b, "", Filters{Voice: "explicit"}) }},
		{"torrents-nolangs", "torrents", "/torrents", "", func(b Base) any { b.VoiceLangs = nil; return SampleTorrents(b, "", Filters{}) }},
		{"torrents-multi", "torrents", "/torrents", "", func(b Base) any { multi(&b); return SampleTorrents(b, "", Filters{}) }},
		{"top", "top", "/top", "", func(b Base) any { b.Nav = "top"; return SampleTop(b, "movie", false) }},
		{"top-series", "top", "/top?kind=series", "", func(b Base) any { b.Nav = "top"; return SampleTop(b, "series", true) }},
		{"top-frag", "top", "/top?kind=series", "top", func(b Base) any { return SampleTop(b, "series", false) }},
		{"top-explicit", "top", "/top?voice=explicit", "", func(b Base) any { return SampleTopFiltered(b, "movie", "", "explicit", false) }},
		{"top-p50", "top", "/top?voice=p50", "top", func(b Base) any { return SampleTopFiltered(b, "movie", "", "p50", false) }},
		{"top-multilang", "top", "/top?vl=uk", "", func(b Base) any { multi(&b); return SampleTopFiltered(b, "series", "uk", "any", false) }},
		{"top-nolangs", "top", "/top", "", func(b Base) any { b.VoiceLangs = nil; return SampleTop(b, "movie", false) }},
		{"top-since", "top", "/top", "", func(b Base) any { b.Nav = "top"; return SampleTopSince(b, "movie", b.Now.Add(-10*24*time.Hour)) }},
		{"work", "work", "/movie/900001", "", func(b Base) any { return SampleWork(b, SampleSpiderID, ReleaseFilters{}) }},
		{"work-quality", "work", "/movie/900001?quality=sd", "releases", func(b Base) any {
			return SampleWork(b, SampleSpiderID, ReleaseFilters{Quality: "sd"})
		}},
		{"work-empty", "work", "/tv/106379?season=2&episode=9", "releases", func(b Base) any {
			return SampleWork(b, SampleFalloutID, ReleaseFilters{Season: "2", Episode: "9"})
		}},
		{"work-series", "work", "/tv/106379", "", func(b Base) any { return SampleWork(b, SampleFalloutID, ReleaseFilters{Season: "2"}) }},
		{"work-series-filtered", "work", "/tv/106379?season=2&episode=4", "", func(b Base) any {
			return SampleWork(b, SampleFalloutID, ReleaseFilters{Season: "2", Episode: "4"})
		}},
		{"work-noprobes", "work", "/movie/900001", "", func(b Base) any { b.Probes = false; return SampleWork(b, SampleSpiderID, ReleaseFilters{}) }},
		{"work-series-multi", "work", "/tv/106379", "", func(b Base) any { multi(&b); return SampleWork(b, SampleFalloutID, ReleaseFilters{}) }},
		{"work-tmdbonly", "work", "/movie/1930", "", func(b Base) any { return SampleWork(b, SampleTMDBOnlyID, ReleaseFilters{}) }},
		{"work-nolangs", "work", "/movie/900001", "", func(b Base) any { b.VoiceLangs = nil; return SampleWork(b, SampleSpiderID, ReleaseFilters{}) }},
		{"torrent-poster", "torrent", "/t/x", "", func(b Base) any { b.Nav = "torrents"; return SampleTorrent(b, false, "poster") }},
		{"torrent-noposter", "torrent", "/t/x", "", func(b Base) any { return SampleTorrent(b, false, "noposter") }},
		{"torrent-unmatched", "torrent", "/t/x", "", func(b Base) any { return SampleTorrent(b, false, "unmatched") }},
		{"torrent-unchecked", "torrent", "/t/x", "", func(b Base) any { return SampleTorrent(b, false, "unchecked") }},
		{"torrent-multilang", "torrent", "/t/x", "", func(b Base) any { multi(&b); return SampleTorrent(b, false, "poster") }},
		{"torrent-files", "torrent", "/t/x?files=all", "files", func(b Base) any { return SampleTorrent(b, true, "poster") }},
		{"torrent-journal", "torrent", "/t/x", "", func(b Base) any { return SampleTorrent(b, false, "journal") }},
		{"torrent-jlist", "torrent", "/t/x/journal", "jlist", func(b Base) any { p := SampleTorrent(b, false, "poster"); p.Torrent.JournalAll = true; return p }},
		{"torrent-nopeers", "torrent", "/t/x", "", func(b Base) any { return SampleTorrent(b, false, "nopeers") }},
		{"torrent-noprobes", "torrent", "/t/x", "", func(b Base) any { b.Probes = false; return SampleTorrent(b, false, "nopeers") }},
		{"act-peers", "torrent", "/t/x/peers", "act-answer", func(b Base) any { return Requested{Base: b, Action: "peers", Releases: 1} }},
		{"act-probe", "torrent", "/t/x/probe", "act-answer", func(b Base) any { return Requested{Base: b, Action: "probe", Releases: 1} }},
		{"act-probe-unreadable", "torrent", "/t/x/probe", "act-answer", func(b Base) any { return Requested{Base: b, Action: "probe"} }},
		{"act-match", "torrent", "/t/x/match", "act-answer", func(b Base) any {
			w := sampleMovies[1].summary(b.Lang, 2)
			return Requested{Base: b, Action: "match", Titles: 1, Work: &w}
		}},
		{"act-match-none", "torrent", "/t/x/match", "act-answer", func(b Base) any { return Requested{Base: b, Action: "match", Titles: 1} }},
		{"act-match-budget", "torrent", "/t/x/match", "act-answer", func(b Base) any { return Requested{Base: b, Action: "match", Failure: "budget"} }},
		{"act-peers-work", "work", "/movie/1/peers", "act-answer", func(b Base) any { return Requested{Base: b, Action: "peers", Releases: 95} }},
		{"data", "data", "/", "", func(b Base) any { b.Nav = "data"; return SampleData(b) }},
		{"data-frag", "data", "/", "data", func(b Base) any { return SampleData(b) }},
		{"data-stale", "data", "/", "", func(b Base) any { b.Nav = "data"; return SampleDataStale(b) }},
		{"data-noprobes", "data", "/", "", func(b Base) any { b.Nav, b.Probes = "data", false; return SampleData(b) }},
		{"data-update", "data", "/", "", func(b Base) any {
			b.Nav, b.Build.Version = "data", "0.44.0"
			b.Latest = latest.Release{Version: "0.46.0", Published: time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC), URL: "https://sarak.sh/mikluko/sniffarr/releases/tag/v0.46.0"}
			return SampleData(b)
		}},
		{"notfound", "notfound", "/stats/old", "", func(b Base) any { return NotFoundPage{Base: b, Asked: "/stats/old"} }},
		{"act-probe-noprobers", "torrent", "/t/x/probe", "act-answer", func(b Base) any { return Requested{Base: b, Action: "probe", Releases: 1, NoProbers: true} }},
	}
	for _, state := range []string{"all", "off", "degraded"} {
		cases = append(cases,
			tc{"data-features-" + state, "data", "/", "", func(b Base) any { b.Nav = "data"; return SampleDataFeatures(b, state) }},
			tc{"torrent-features-" + state, "torrent", "/t/x", "", func(b Base) any { return SampleTorrent(SampleFeatureBase(b, state), false, "poster") }},
			tc{"work-features-" + state, "work", "/movie/900001", "", func(b Base) any {
				return SampleWork(SampleFeatureBase(b, state), SampleSpiderID, ReleaseFilters{})
			}})
	}
	dir := os.Getenv("PREVIEW_DIR")
	for _, lang := range []string{"ru", "en"} {
		for _, c := range cases {
			req := httptest.NewRequest("GET", c.path, nil)
			if c.target != "" {
				req.Header.Set("HX-Request", "true")
				req.Header.Set("HX-Target", c.target)
			}
			b := r.Base(req, lang, "", []string{"ru"}, loc)
			b.Now, b.Probes, b.Classifier = now, true, true
			b.Build = sampleBuild
			b.JournalRetention = 30 * 24 * time.Hour
			w := httptest.NewRecorder()
			if err := r.Render(w, req, c.page, c.data(b)); err != nil {
				t.Fatalf("%s/%s: %v", lang, c.name, err)
			}
			body := w.Body.String()
			full := strings.HasPrefix(body, "<!doctype html>")
			if full != (c.target == "") {
				t.Errorf("%s/%s: full page = %v, want %v", lang, c.name, full, c.target == "")
			}
			if c.target != "" && !strings.Contains(body, `id="`+c.target+`"`) {
				t.Errorf("%s/%s: fragment lacks id=%q", lang, c.name, c.target)
			}
			writePreview(dir, lang+"-"+c.name, full, w.Body.Bytes())
		}
	}
	if len(missing) > 0 {
		t.Errorf("untranslated keys: %v", missing)
	}
}

// writePreview writes a rendered page into dir as <name>.html, a fragment
// as frag-<name>.html; nothing without dir.
func writePreview(dir, name string, full bool, body []byte) {
	if dir == "" {
		return
	}
	if !full {
		name = "frag-" + name
	}
	_ = os.WriteFile(filepath.Join(dir, name+".html"), body, 0o644)
}

// Renders every page with every optional feature off: none leaves a trace
// but the data page's block of features, which says each is off.
func TestRenderFeaturesOff(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	loc := time.FixedZone("CEST", 2*3600)
	cases := []struct {
		name, page, path string
		data             func(Base) any
	}{
		{"torrents", "torrents", "/torrents", func(b Base) any { return SampleTorrents(b, "", Filters{}) }},
		{"top", "top", "/top", func(b Base) any { return SampleTop(b, "movie", false) }},
		{"work", "work", "/movie/900001", func(b Base) any { return SampleWork(b, SampleSpiderID, ReleaseFilters{}) }},
		{"torrent", "torrent", "/t/x", func(b Base) any { return SampleTorrent(b, false, "nopeers") }},
		{"data", "data", "/", func(b Base) any { return SampleDataFeatures(b, "off") }},
	}
	for _, lang := range []string{"ru", "en"} {
		for _, c := range cases {
			req := httptest.NewRequest("GET", c.path, nil)
			b := SampleFeatureBase(r.Base(req, lang, c.name, []string{"ru"}, loc), "off")
			w := httptest.NewRecorder()
			if err := r.Render(w, req, c.page, c.data(b)); err != nil {
				t.Fatalf("%s/%s: %v", lang, c.name, err)
			}
			off := []string{
				`name="vl"`, `name="voice"`, `voice=`, b.T("d.voice") + ": ", "<th>" + b.T("top.audio") + "</th>", `<h3 class="gap">` + b.T("top.audio"), b.T("card.audio_mark"),
				`act-probe`, b.T("act.probe_nopeers"), `aria-label="` + b.T("d.probes") + `: `, `<h3>` + b.T("q.probe") + `</h3>`, b.T("q.p1"), b.T("q.p3"),
				`aria-label="` + b.T("d.classifier") + `: `, `/torrents?layer=classifier`,
			}
			var on []string
			if c.name == "data" {
				on = []string{b.T("feat.state.classifier.off"), b.Fmt("feat.how.probes", "-probe")}
			}
			checkBody(t, lang+"/"+c.name, w.Body.String(), off, on)
		}
	}
}

// checkBody fails the test for each of without the body has and each of
// with it lacks.
func checkBody(t *testing.T, name, body string, without, with []string) {
	t.Helper()
	for _, s := range without {
		if strings.Contains(body, s) {
			t.Errorf("%s: has %q", name, s)
		}
	}
	for _, s := range with {
		if !strings.Contains(body, s) {
			t.Errorf("%s: no %q", name, s)
		}
	}
}

// The data page and a probe's answer with the classifier's limit spent and
// no prober connected, in amber, against the same with every feature
// working.
func TestRenderFeaturesDegraded(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	b := r.Base(req, "ru", "data", []string{"ru"}, time.UTC)
	b.Now = time.Date(2026, 10, 20, 14, 5, 0, 0, time.UTC)
	render := func(page string, data any) string {
		t.Helper()
		w := httptest.NewRecorder()
		if err := r.Render(w, req, page, data); err != nil {
			t.Fatal(err)
		}
		return w.Body.String()
	}
	degraded := []string{
		`<div class="meter full" role="img" aria-label="100,0%"><i style="width: 100%"></i></div>`,
		`<span class="xs dw">` + b.Fmt("d.spent", b.Num(3412)) + `</span>`,
		b.T("d.spent_tip"),
		`<i class="m"></i><span class="tt" tabindex="0" aria-label="Нет проберов: ` + b.T("q.noprobers_tip"),
		`aria-label="?: ` + b.T("q.noprobers_eta") + `"`,
		`<span class="dw"><i class="m"></i>лимит на сутки исчерпан</span>`,
		`<span class="dw"><i class="m"></i>нет проберов</span>`,
		b.Fmt("feat.why.probes_fails", b.Count(2230, "q.tasks_n")),
	}
	bad := render("data", SampleDataFeatures(b, "degraded"))
	all := render("data", SampleDataFeatures(b, "all"))
	for _, s := range degraded {
		if !strings.Contains(bad, s) {
			t.Errorf("degraded: no %q", s)
		}
		if strings.Contains(all, s) {
			t.Errorf("all working: has %q", s)
		}
	}
	for _, s := range []string{"82,6% суточного лимита", "включена: ru", ">P2: announce<"} {
		if !strings.Contains(all, s) {
			t.Errorf("all working: no %q", s)
		}
	}

	req = httptest.NewRequest("POST", "/t/x/probe", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "act-answer")
	act := Requested{Base: b, Action: "probe", Releases: 1}
	if s := render("torrent", act); strings.Contains(s, "act-ans dw") || strings.Contains(s, b.T("act.noprobers")) {
		t.Error("a probe asked for with a prober connected is answered in amber")
	}
	act.NoProbers = true
	if s := render("torrent", act); !strings.Contains(s, `class="act-ans dw"`) || !strings.Contains(s, `<i class="m"></i>`+b.T("act.done.probe")+" "+b.T("act.noprobers")) {
		t.Errorf("a probe asked for with no prober connected: %s", s)
	}
}

// The pages link static files at their versions; a file at its version is
// immutable, at any other URL it revalidates by ETag.
func TestStatic(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	u := r.Static().URL("app.css")
	if !strings.HasPrefix(u, "/static/app.css?v=") || len(u) != len("/static/app.css?v=")+16 {
		t.Fatalf("URL = %q", u)
	}
	if got := r.Static().URL("nope.css"); got != "/static/nope.css" {
		t.Errorf("URL of a missing file = %q", got)
	}
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	if err := r.Render(w, req, "data", SampleData(r.Base(req, "en", "data", nil, time.UTC))); err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`/(static|ui)/[^"?]*"`).FindString(w.Body.String()); m != "" {
		t.Errorf("a static file linked without its version: %s", m)
	}

	h := r.Static()
	serve := func(path string, hdr ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	w = serve(u)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("at its version: %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
	etag := w.Header().Get("ETag")
	if etag != `"`+strings.TrimPrefix(u, "/static/app.css?v=")+`"` {
		t.Errorf("ETag = %q", etag)
	}
	for _, p := range []string{"/static/app.css", "/static/app.css?v=stale"} {
		if w = serve(p); w.Code != 200 || w.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: %d %q", p, w.Code, w.Header().Get("Cache-Control"))
		}
	}
	if w = serve("/static/app.css", "If-None-Match", etag); w.Code != 304 {
		t.Errorf("revalidated: %d", w.Code)
	}
	if w = serve("/static/nope.css"); w.Code != 404 || w.Header().Get("Cache-Control") != "" {
		t.Errorf("missing: %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
}

// Every page preloads both faces at the versioned URLs of libarr's /ui/ its
// @font-face rules name, and those URLs serve the font for a year.
func TestFonts(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	if err := r.Render(w, req, "data", SampleData(r.Base(req, "en", "data", nil, time.UTC))); err != nil {
		t.Fatal(err)
	}
	body := w.Body.String()
	for _, f := range []string{"fonts/jetbrains-mono-400.woff2", "fonts/jetbrains-mono-600.woff2"} {
		u := r.shared.URL(f)
		if !strings.Contains(u, "?v=") {
			t.Fatalf("%s has no version: %q", f, u)
		}
		if want := `<link rel="preload" href="` + u + `" as="font" type="font/woff2" crossorigin>`; !strings.Contains(body, want) {
			t.Errorf("no preload of %s; want %s", f, want)
		}
		if want := `src:url("` + u + `")`; !strings.Contains(body, want) {
			t.Errorf("no @font-face of %s; want %s", f, want)
		}
		sw := httptest.NewRecorder()
		r.Shared().ServeHTTP(sw, httptest.NewRequest("GET", u, nil))
		if sw.Code != 200 || sw.Header().Get("Content-Type") != "font/woff2" || sw.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Errorf("%s: %d %q %q", u, sw.Code, sw.Header().Get("Content-Type"), sw.Header().Get("Cache-Control"))
		}
	}
	sw := httptest.NewRecorder()
	r.Shared().ServeHTTP(sw, httptest.NewRequest("GET", "/ui/fonts/OFL.txt", nil))
	if sw.Code != 200 {
		t.Errorf("OFL.txt: %d", sw.Code)
	}
}

// The pages load libarr's app.js from /ui/ at its version.
func TestSharedAppJS(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	if err := r.Render(w, req, "data", SampleData(r.Base(req, "en", "data", nil, time.UTC))); err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`<script src="(/ui/app\.js\?v=\w+)" defer>`).FindStringSubmatch(w.Body.String())
	if m == nil {
		t.Fatal("libarr's app.js is not linked at its version")
	}
	sw := httptest.NewRecorder()
	r.Shared().ServeHTTP(sw, httptest.NewRequest("GET", m[1], nil))
	if sw.Code != 200 || !strings.Contains(sw.Header().Get("Content-Type"), "javascript") {
		t.Errorf("%s: %d %q", m[1], sw.Code, sw.Header().Get("Content-Type"))
	}
}

// A tooltip's trigger is its label, focusable and named by label and text
// together; the text itself is hidden from assistive technology.
func TestTip(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	b := r.Base(req, "en", "data", nil, time.UTC)
	w := httptest.NewRecorder()
	if err := r.Render(w, req, "data", SampleData(b)); err != nil {
		t.Fatal(err)
	}
	label, text := b.T("d.queue"), b.T("q.note")
	want := `<span class="tt" tabindex="0" aria-label="` + template.HTMLEscapeString(label+": "+text) + `">` +
		label + `<span class="tc" aria-hidden="true">` + template.HTMLEscapeString(text) + `</span></span>`
	if !strings.Contains(w.Body.String(), want) {
		t.Errorf("no tooltip %s", want)
	}
}

// The card's copy buttons are icon buttons named by aria-label, with a
// tooltip of the same name and the confirmation text.
func TestCopyButtons(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/t/x", nil)
	b := r.Base(req, "en", "", []string{"ru"}, time.UTC)
	p := SampleTorrent(b, false, "poster")
	w := httptest.NewRecorder()
	if err := r.Render(w, req, "torrent", p); err != nil {
		t.Fatal(err)
	}
	body := w.Body.String()
	svg, err := r.shared.Icon("copy")
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{b.T("card.copy_hash"), b.T("card.copy_magnet")} {
		re := regexp.MustCompile(`<span class="tt ic"><button type="button" class="ic" data-copy="[^"]+" data-done="` +
			regexp.QuoteMeta(b.T("card.copied")) + `" aria-label="` + regexp.QuoteMeta(label) + `">` +
			regexp.QuoteMeta(string(svg)) + `</button><span class="tc" role="status">` + regexp.QuoteMeta(label) + `</span></span>`)
		if !re.MatchString(body) {
			t.Errorf("no copy button %q", label)
		}
	}
}

// Every icon is a square-capped SVG hidden from assistive technology.
func TestIcons(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"copy", "refresh", "search"} {
		svg, err := r.shared.Icon(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range []string{`<svg `, `stroke="currentColor"`, `stroke-linecap="square"`, `aria-hidden="true"`} {
			if !strings.Contains(string(svg), s) {
				t.Errorf("%s: no %s", name, s)
			}
		}
	}
	if _, err := r.shared.Icon("nope"); err == nil {
		t.Error("an unknown icon is not an error")
	}
}

// A priority badge is a swatch and a label with no border or padding.
func TestBadgeHasNoBorder(t *testing.T) {
	css, err := files.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range strings.Split(string(css), "}") {
		if strings.Contains(rule, "#data .badge{") && (strings.Contains(rule, "border") || strings.Contains(rule, "padding")) {
			t.Errorf("the badge rule draws a box: %s}", rule)
		}
	}
}

// headerMarkup renders the page "notfound" under nav in lang and returns its header.
func headerMarkup(t *testing.T, lang, nav string) string {
	t.Helper()
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/x", nil)
	b := r.Base(req, lang, nav, nil, time.UTC)
	w := httptest.NewRecorder()
	if err := r.Render(w, req, "notfound", NotFoundPage{Base: b, Asked: "/x"}); err != nil {
		t.Fatal(err)
	}
	body := w.Body.String()
	i, j := strings.Index(body, "<header"), strings.Index(body, "</header>")
	if i < 0 || j < i {
		t.Fatalf("no header in %s", body)
	}
	return body[i:j]
}

// The header is the logo, the tabs of the torrents and the top, and the
// language switch, in one row that does not wrap, so that at 375 px it is
// one line of 44.
func TestHeader(t *testing.T) {
	css, err := os.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), ".top{flex-wrap:nowrap}") {
		t.Error("the header wraps")
	}
	for _, lang := range []string{"ru", "en"} {
		for _, c := range []struct{ nav, on string }{
			{"data", ""}, {"", ""}, {"torrents", "/torrents"}, {"top", "/top"},
		} {
			t.Run(lang+"/"+c.nav, func(t *testing.T) { checkTabs(t, headerMarkup(t, lang, c.nav), c.nav, c.on) })
			t.Run(lang+"/"+c.nav+"/row", func(t *testing.T) { checkRow(t, headerMarkup(t, lang, c.nav), c.nav) })
		}
	}
}

// checkTabs fails unless h has the two tabs, the torrents and the top, and
// only the one at href on selected, none for "".
func checkTabs(t *testing.T, h, nav, on string) {
	t.Helper()
	tabs := h[strings.Index(h, "<nav>") : strings.Index(h, "</nav>")+len("</nav>")]
	if n := strings.Count(tabs, "<a "); n != 2 {
		t.Errorf("%d tabs, want 2: %s", n, tabs)
	}
	if !strings.Contains(tabs, `<a href="/torrents"`) || !strings.Contains(tabs, `<a href="/top"`) {
		t.Errorf("tabs are not the torrents and the top: %s", tabs)
	}
	if strings.Contains(tabs, `href="/"`) {
		t.Errorf("the data page is a tab: %s", tabs)
	}
	want := 0
	if on != "" {
		want = 1
		if !strings.Contains(tabs, `<a href="`+on+`" class="on" aria-current="page">`) {
			t.Errorf("%s not selected: %s", on, tabs)
		}
	}
	if n := strings.Count(h, `class="on"`); n != want {
		t.Errorf("%d tabs selected, want %d", n, want)
	}
}

// checkRow fails unless h is the logo, the tabs and the switch, in that order,
// the logo marked current on the data page only.
func checkRow(t *testing.T, h, nav string) {
	t.Helper()
	last := -1
	for _, s := range []string{`<a href="/" class="brand"`, "<nav>", `<span class="langs">`} {
		i := strings.Index(h, s)
		if i <= last {
			t.Errorf("%q is not after the one before it: %s", s, h)
		}
		last = i
	}
	if got := strings.Contains(h, `class="brand" aria-current="page"`); got != (nav == "data") {
		t.Errorf("the logo marked current = %v", got)
	}
}
