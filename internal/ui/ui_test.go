package ui

import (
	"html/template"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// Renders every page and fragment in both languages, fails on template errors
// and untranslated keys. PREVIEW_DIR=dir writes the full pages there.
func TestRenderAll(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	onMissing = func(lang, key string) { missing = append(missing, lang+":"+key) }
	defer func() { onMissing = nil }()

	loc := time.FixedZone("CEST", 2*3600)
	now := time.Date(2026, 10, 20, 14, 5, 0, 0, loc)

	type tc struct {
		name, page, path, target string
		data                     func(Base) any
	}
	multi := func(b *Base) { b.VoiceLangs = []string{"ru", "uk"} }
	cases := []tc{
		{"torrents", "torrents", "/torrents", "", func(b Base) any { b.Nav = "torrents"; return SampleTorrents(b, "", Filters{}) }},
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
		{"work", "work", "/movie/900001", "", func(b Base) any { return SampleWork(b, SampleSpiderID, ReleaseFilters{}) }},
		{"work-explicit", "work", "/movie/900001" + "?voice=explicit", "releases", func(b Base) any {
			return SampleWork(b, SampleSpiderID, ReleaseFilters{Voice: "explicit"})
		}},
		{"work-empty", "work", "/movie/900001" + "?quality=sd&source=remux", "releases", func(b Base) any {
			return SampleWork(b, SampleSpiderID, ReleaseFilters{Quality: "sd", Source: "remux"})
		}},
		{"work-series", "work", "/tv/106379", "", func(b Base) any { return SampleWork(b, SampleFalloutID, ReleaseFilters{Season: "2"}) }},
		{"work-series-multi", "work", "/tv/106379", "", func(b Base) any { multi(&b); return SampleWork(b, SampleFalloutID, ReleaseFilters{}) }},
		{"work-tmdbonly", "work", "/movie/1930", "", func(b Base) any { return SampleWork(b, SampleTMDBOnlyID, ReleaseFilters{}) }},
		{"work-nolangs", "work", "/movie/900001", "", func(b Base) any { b.VoiceLangs = nil; return SampleWork(b, SampleSpiderID, ReleaseFilters{}) }},
		{"torrent-poster", "torrent", "/t/x", "", func(b Base) any { return SampleTorrent(b, false, "poster") }},
		{"torrent-noposter", "torrent", "/t/x", "", func(b Base) any { return SampleTorrent(b, false, "noposter") }},
		{"torrent-unmatched", "torrent", "/t/x", "", func(b Base) any { return SampleTorrent(b, false, "unmatched") }},
		{"torrent-unchecked", "torrent", "/t/x", "", func(b Base) any { return SampleTorrent(b, false, "unchecked") }},
		{"torrent-multilang", "torrent", "/t/x", "", func(b Base) any { multi(&b); return SampleTorrent(b, false, "poster") }},
		{"torrent-files", "torrent", "/t/x?files=all", "files", func(b Base) any { return SampleTorrent(b, true, "poster") }},
		{"torrent-journal", "torrent", "/t/x", "", func(b Base) any { return SampleTorrent(b, false, "journal") }},
		{"data", "data", "/", "", func(b Base) any { b.Nav = "data"; return SampleData(b) }},
		{"data-frag", "data", "/", "data", func(b Base) any { return SampleData(b) }},
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
			b.Now = now
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
			if dir != "" && full {
				_ = os.WriteFile(filepath.Join(dir, lang+"-"+c.name+".html"), w.Body.Bytes(), 0o644)
			}
		}
	}
	if len(missing) > 0 {
		t.Errorf("untranslated keys: %v", missing)
	}
}

// Renders every page without audio languages: nothing of the extension in
// the output.
func TestRenderNoLanguages(t *testing.T) {
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
		{"torrent", "torrent", "/t/x", func(b Base) any { return SampleTorrent(b, false, "poster") }},
		{"data", "data", "/", func(b Base) any {
			p := SampleData(b)
			p.Stats.Voice = nil
			return p
		}},
	}
	for _, lang := range []string{"ru", "en"} {
		for _, c := range cases {
			req := httptest.NewRequest("GET", c.path, nil)
			b := r.Base(req, lang, c.name, nil, loc)
			w := httptest.NewRecorder()
			if err := r.Render(w, req, c.page, c.data(b)); err != nil {
				t.Fatalf("%s/%s: %v", lang, c.name, err)
			}
			body := w.Body.String()
			for _, s := range []string{`name="vl"`, `name="voice"`, `voice=`, b.T("d.voice"), b.T("top.audio"), b.T("audio.markcol")} {
				if strings.Contains(body, s) {
					t.Errorf("%s/%s: has %q", lang, c.name, s)
				}
			}
		}
	}
}

func TestFormat(t *testing.T) {
	t5 := time.Date(2026, 10, 20, 14, 5, 0, 0, time.UTC)
	ru := Base{Lang: "ru"}
	c := Base{Lang: "en"}
	units := Base{Lang: "ru", msgs: map[string]string{"unit.min": "мин", "unit.h": "ч", "unit.d": "д"}}
	for _, x := range []struct{ got, want string }{
		{ru.Num(1284311), "1\u00a0284\u00a0311"},
		{ru.Num(-1284), "-1\u00a0284"},
		{c.Num(1284311), "1,284,311"},
		{ru.Size(26629001216), "24,8\u00a0ГБ"},
		{Base{Lang: "ru", msgs: map[string]string{"kind.movie": "фильм"}}.Facts("movie", &TMDBWork{}), "Фильм"},
		{sentence("сериал"), "Сериал"},
		{sentence(""), ""},
		{c.Size(412 << 20), "412 MB"},
		{ru.Prob(fp(.93)), "0,93"},
		{ru.Pct(41280, 50000), "82,6%"},
		{c.PctW(41280, 50000), "82.6%"},
		{ru.AudioCell(Audio{Lang: "ru", Marker: true, Mark: "seleZen"}), "seleZen"},
		{ru.AudioCell(Audio{Lang: "ru", Probability: fp(.62)}), "0,62"},
		{ru.AudioCell(Audio{Lang: "ru"}), "—"},
		{ru.Marks([]string{"a", "b", "c", "d", "e"}), "a, b, c +2"},
		{DefaultVoiceLang([]string{"uk", "ru"}, "ru"), "ru"},
		{DefaultVoiceLang([]string{"uk", "de"}, "en"), "uk"},
		{DefaultVoiceLang(nil, "ru"), ""},
		{WorkID("series", 1399), "tv1399"},
		{WorkID("movie", 318846), "m318846"},
		{WorkPath("series", 1399), "/tv/1399"},
		{WorkPath("movie", 318846), "/movie/318846"},
		{Base{Seerr: "https://seerr.example.com/"}.SeerrURL(&TMDBWork{State: TMDBMatched, Type: "tv", ID: 1399}), "https://seerr.example.com/tv/1399"},
		{Base{Seerr: "https://seerr.example.com"}.SeerrURL(&TMDBWork{State: TMDBMatched, Type: "movie", ID: 348}), "https://seerr.example.com/movie/348"},
		{Base{}.SeerrURL(&TMDBWork{State: TMDBMatched, Type: "movie", ID: 348}), ""},
		{Base{Seerr: "https://seerr.example.com"}.SeerrURL(nil), ""},
		{Base{Radarr: "https://radarr.example.com/"}.RadarrURL(&TMDBWork{State: TMDBMatched, Type: "movie", ID: 348}), "https://radarr.example.com/add/new?term=tmdb%3A348"},
		{Base{Radarr: "https://radarr.example.com"}.RadarrURL(&TMDBWork{State: TMDBMatched, Type: "tv", ID: 1399}), ""},
		{Base{Sonarr: "https://example.com/sonarr"}.SonarrURL(&TMDBWork{State: TMDBMatched, Type: "tv", ID: 1399}), "https://example.com/sonarr/add/new?term=tmdb%3A1399"},
		{Base{Sonarr: "https://example.com/sonarr"}.SonarrURL(&TMDBWork{State: TMDBMatched, Type: "movie", ID: 348}), ""},
		{Base{}.RadarrURL(&TMDBWork{State: TMDBMatched, Type: "movie", ID: 348}), ""},
		{Base{Radarr: "https://radarr.example.com"}.RadarrURL(&TMDBWork{State: TMDBUnmatched}), ""},
		{units.Span(30 * time.Second), "<\u00a01\u00a0мин"},
		{units.Span(42 * time.Minute), "42\u00a0мин"},
		{units.Span(200 * time.Minute), "3\u00a0ч 20\u00a0мин"},
		{units.Span(5*24*time.Hour + 4*time.Hour + 59*time.Minute), "5\u00a0д 4\u00a0ч"},
		{units.Span(48 * time.Hour), "2\u00a0д"},
		{ru.SE(2, 5), "S02E05"},
		{ru.SE(2, 0), "S02"},
		{Base{Lang: "en", Now: t5, msgs: map[string]string{"ago.min": "{0} min ago", "ago.now": "just now"}}.Ago(t5.Add(-125 * time.Minute)), "125 min ago"},
		{Base{Lang: "en", Now: t5, msgs: map[string]string{"ago.now": "just now"}}.Ago(t5.Add(-59 * time.Second)), "just now"},
	} {
		if x.got != x.want {
			t.Errorf("got %q, want %q", x.got, x.want)
		}
	}
	if k, id, ok := ParseWorkID("tv1399"); !ok || k != "series" || id != 1399 {
		t.Error("ParseWorkID")
	}
	if _, _, ok := ParseWorkID("1399"); ok {
		t.Error("ParseWorkID accepted a bare id")
	}
	if niceCeil(15740) != 20000 || niceCeil(89187) != 100000 {
		t.Error("niceCeil")
	}
}

// The pages link static files at their versions; a file at its version is
// immutable, at any other URL it revalidates by ETag.
func TestStatic(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	u := r.StaticURL("app.css")
	if !strings.HasPrefix(u, "/static/app.css?v=") || len(u) != len("/static/app.css?v=")+16 {
		t.Fatalf("StaticURL = %q", u)
	}
	if got := r.StaticURL("nope.css"); got != "/static/nope.css" {
		t.Errorf("StaticURL of a missing file = %q", got)
	}
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	if err := r.Render(w, req, "data", SampleData(r.Base(req, "en", "data", nil, time.UTC))); err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`/static/[^"?]*"`).FindString(w.Body.String()); m != "" {
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

// The journal's messages are the same in both languages.
func TestJournalMessages(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	keys := func(lang string) []string {
		var out []string
		for k := range r.Base(httptest.NewRequest("GET", "/t/x", nil), lang, "", nil, time.UTC).msgs {
			if strings.HasPrefix(k, "journal.") {
				out = append(out, k)
			}
		}
		slices.Sort(out)
		return out
	}
	en, ru := keys("en"), keys("ru")
	for _, k := range en {
		if !slices.Contains(ru, k) {
			t.Errorf("%s is not in Russian", k)
		}
	}
	for _, k := range ru {
		if !slices.Contains(en, k) {
			t.Errorf("%s is not in English", k)
		}
	}
}

// Every kind and outcome journalSpec marks up has its labels and those of its
// summary keys in both languages.
func TestJournalSpecMessages(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"en", "ru"} {
		for _, m := range journalSpecMissing(r.Base(httptest.NewRequest("GET", "/t/x", nil), lang, "", nil, time.UTC)) {
			t.Errorf("%s: %s", lang, m)
		}
	}
}

// journalSpecMissing lists the labels of journalSpec the language lacks.
func journalSpecMissing(b Base) []string {
	var out []string
	for kind, outcomes := range journalSpec {
		if m := "journal.kind." + kind; !b.has(m) {
			out = append(out, m)
		}
		for outcome, o := range outcomes {
			if m := "journal.outcome." + kind + "." + outcome; !b.has(m) {
				out = append(out, m)
			}
			for _, k := range o.summary {
				if !b.has("journal.key."+kind+"."+k) && !b.has("journal.key."+k) {
					out = append(out, "journal.key."+k+" of "+kind+"/"+outcome)
				}
			}
		}
	}
	return out
}

func TestJournalLevel(t *testing.T) {
	var log strings.Builder
	defer slog.SetDefault(slog.Default())
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))

	for _, c := range []struct {
		name   string
		e      JournalEntry
		want   Severity
		logged bool
	}{
		{"stored", JournalEntry{Kind: "fetched", Outcome: "stored"}, SeverityNormal, false},
		{"rejected", JournalEntry{Kind: "fetched", Outcome: "rejected"}, SeverityFinal, false},
		{"timeout", JournalEntry{Kind: "fetch_failed", Outcome: "timeout", Detail: map[string]any{"retry_at": 1.0}}, SeverityRetry, false},
		{"dropped", JournalEntry{Kind: "fetch_failed", Outcome: "dropped", Detail: map[string]any{"dead_until": 1.0}}, SeverityFinal, false},
		{"no peers", JournalEntry{Kind: "fetch_failed", Outcome: "no_peers", Detail: map[string]any{"dead_until": 1.0}}, SeverityFinal, false},
		{"a timeout kept out", JournalEntry{Kind: "fetch_failed", Outcome: "timeout", Detail: map[string]any{"dead_until": 1.0}}, SeverityRetry, true},
		{"no peers with a retry", JournalEntry{Kind: "fetch_failed", Outcome: "no_peers", Detail: map[string]any{"dead_until": 1.0, "retry_at": 1.0}}, SeverityFinal, true},
		{"dropped with neither", JournalEntry{Kind: "fetch_failed", Outcome: "dropped"}, SeverityFinal, true},
		{"classification failed", JournalEntry{Kind: "classified", Outcome: "failed"}, SeverityFinal, false},
		{"nothing matched", JournalEntry{Kind: "matched", Outcome: "none"}, SeverityRetry, false},
		{"a match failed", JournalEntry{Kind: "matched", Outcome: "error"}, SeverityRetry, false},
		{"a final probe", JournalEntry{Kind: "probed", Outcome: "timeout", Detail: map[string]any{"final": true}}, SeverityFinal, false},
		{"a probe to retry", JournalEntry{Kind: "probed", Outcome: "no_peers", Detail: map[string]any{"final": false}}, SeverityRetry, false},
		{"a probe without the flag", JournalEntry{Kind: "probed", Outcome: "timeout"}, SeverityFinal, true},
		{"tracks not read", JournalEntry{Kind: "probed", Outcome: "unknown"}, SeverityFinal, false},
		{"tracks read", JournalEntry{Kind: "probed", Outcome: "tracks"}, SeverityNormal, false},
		{"deleted", JournalEntry{Kind: "deleted", Outcome: "idle"}, SeverityFinal, false},
		{"unknown", JournalEntry{Kind: "fetched", Outcome: "novel"}, SeverityNormal, true},
	} {
		log.Reset()
		if got := c.e.Level(); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
		if logged := log.Len() > 0; logged != c.logged {
			t.Errorf("%s: logged %q, want a data error logged: %v", c.name, log.String(), c.logged)
		}
	}
}

func TestJournalSummary(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	b := r.Base(httptest.NewRequest("GET", "/t/x", nil), "en", "", nil, time.UTC)
	e := JournalEntry{Kind: "fetched", Outcome: "stored", Detail: map[string]any{"name": "x", "size": 26629001216.0, "files": 14.0, "discovery": "announce"}}
	want := []DetailPair{{"files", "14", ""}, {"size", "24.8 GB", ""}}
	if got := b.JournalSummary(e); !slices.Equal(got, want) {
		t.Errorf("%v, want %v", got, want)
	}
}

func TestJournalFields(t *testing.T) {
	e := JournalEntry{Kind: "probed", Detail: map[string]any{
		"retry_at": 1762500000.0, "downloaded": 4194304.0, "leases": 2.0, "attempt": 3.0, "tmdb_id": 949.0, "share": 0.5,
		"tracks": []any{"video::hevc", "audio:rus:eac3", "odd"}, "audio": map[string]any{"ru": 1.0},
	}}
	want := []JournalField{
		{"attempt", int64(3)},
		{"audio", map[string]any{"ru": 1.0}},
		{"downloaded", Bytes(4194304)},
		{"leases", Times(2)},
		{"retry_at", time.Unix(1762500000, 0)},
		{"share", 0.5},
		{"tmdb_id", ID(949)},
		{"tracks", []any{Track{"video", "", "hevc"}, Track{"audio", "rus", "eac3"}, "odd"}},
	}
	if got := e.Fields(); !reflect.DeepEqual(got, want) {
		t.Errorf("%v, want %v", got, want)
	}
	if got, want := e.Tracks(), []Track{{"video", "", "hevc"}, {"audio", "rus", "eac3"}}; !slices.Equal(got, want) {
		t.Errorf("tracks %v, want %v", got, want)
	}
}

func TestJournalDetail(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	loc := time.FixedZone("CEST", 2*3600)
	base := func(lang string) Base {
		return r.Base(httptest.NewRequest("GET", "/t/x", nil), lang, "", nil, loc)
	}
	var missing []string
	onMissing = func(lang, key string) { missing = append(missing, lang+":"+key) }
	defer func() { onMissing = nil }()

	for _, c := range []struct {
		name, lang string
		e          JournalEntry
		want       []DetailPair
	}{
		{"probed", "en", JournalEntry{Kind: "probed", Detail: map[string]any{"retry_at": 1762500000.0, "attempt": 2.0, "final": true, "downloaded": 4194304.0,
			"tracks": []any{"video::hevc", "audio:rus:eac3"}, "audio": map[string]any{"ru": 0.88, "en": 0.0}, "path": "a.mkv", "novel": "x"}},
			[]DetailPair{
				{"attempt", "2", ""},
				{"audio track probability", "English 0%, Russian 88%", ""},
				{"downloaded", "4.0\u00a0MB", ""},
				{"attempts ran out", "yes", ""},
				{"novel", "x", ""},
				{"file", "a.mkv", ""},
				{"next try", "2025-11-07 09:20", ""},
				{"tracks", "video hevc, audio rus eac3", ""},
			}},
		{"probed in Russian", "ru", JournalEntry{Kind: "probed", Detail: map[string]any{"final": false, "downloaded": 4194304.0, "audio": map[string]any{"ru": 0.88},
			"tracks": []any{"video::hevc", "audio:rus:eac3", "subtitle:eng:subrip", "other:x:y"}}},
			[]DetailPair{{"вероятность звуковой дорожки", "русская 88%", ""}, {"скачано", "4,0\u00a0МБ", ""}, {"попытки исчерпаны", "нет", ""},
				{"дорожки", "видео hevc, звук rus eac3, субтитры eng subrip, other x y", ""}}},
		{"a codec with colons", "en", JournalEntry{Kind: "probed", Detail: map[string]any{"tracks": []any{"audio:eng:A:B"}}}, []DetailPair{{"tracks", "audio eng A:B", ""}}},
		{"stored", "en", JournalEntry{Kind: "fetched", Detail: map[string]any{"kind": "movie", "kind_layer": "reuse", "discovery": "announce", "size": 26629001216.0, "leases": 3.0}},
			[]DetailPair{{"discovered by", "announce", ""}, {"kind", "movie", ""}, {"kind decided by", "title repeat", ""}, {"leased", "3 times", ""}, {"size", "24.8\u00a0GB", ""}}},
		{"leased twice in Russian", "ru", JournalEntry{Kind: "fetch_failed", Detail: map[string]any{"leases": 2.0}}, []DetailPair{{"выдана", "2 раза", ""}}},
		{"leased 5 times in Russian", "ru", JournalEntry{Kind: "fetch_failed", Detail: map[string]any{"leases": 5.0}}, []DetailPair{{"выдана", "5 раз", ""}}},
		{"leased 12 times in Russian", "ru", JournalEntry{Kind: "fetch_failed", Detail: map[string]any{"leases": 12.0}}, []DetailPair{{"выдана", "12 раз", ""}}},
		{"leased 21 times in Russian", "ru", JournalEntry{Kind: "fetch_failed", Detail: map[string]any{"leases": 21.0}}, []DetailPair{{"выдана", "21 раз", ""}}},
		{"leased 22 times in Russian", "ru", JournalEntry{Kind: "fetch_failed", Detail: map[string]any{"leases": 22.0}}, []DetailPair{{"выдана", "22 раза", ""}}},
		{"leased once", "en", JournalEntry{Kind: "fetch_failed", Detail: map[string]any{"leases": 1.0}}, []DetailPair{{"leased", "1 time", ""}}},
		{"an unknown value", "en", JournalEntry{Kind: "fetched", Detail: map[string]any{"kind": "opera", "reason": "nomedia"}},
			[]DetailPair{{"kind", "opera", ""}, {"reason", "no video or audio files", ""}}},
		{"small in the unit the card prints", "en", JournalEntry{Kind: "fetched", Detail: map[string]any{"reason": "small", "size": 1048576.0}},
			[]DetailPair{{"reason", "under 50 MB", ""}, {"size", "1.0\u00a0MB", ""}}},
		{"classified", "ru", JournalEntry{Kind: "classified", Detail: map[string]any{"source": "web", "source_confidence": 0.8, "trigger": "search"}},
			[]DetailPair{{"источник", "web", ""}, {"уверенность в источнике", "0,8", ""}, {"кто запросил", "поиск", ""}}},
		{"the classifier's disc image", "en", JournalEntry{Kind: "classified", Detail: map[string]any{"source": "disc_image"}}, []DetailPair{{"source", "disc image", ""}}},
		{"the classifier's encode", "ru", JournalEntry{Kind: "classified", Detail: map[string]any{"source": "encode"}}, []DetailPair{{"источник", "rip", ""}}},
		{"arr", "en", JournalEntry{Kind: "arr", Detail: map[string]any{"app": "sonarr", "source": "history", "recorded_at": 1762500000.0}},
			[]DetailPair{{"app", "Sonarr", ""}, {"recorded", "2025-11-07 09:20", ""}, {"came by", "history import", ""}}},
		{"an arr match", "en", JournalEntry{Kind: "matched", Detail: map[string]any{"event_at": 1762500000.0, "trigger": "arr"}},
			[]DetailPair{{"time of the Radarr/Sonarr event", "2025-11-07 09:20", ""}, {"asked by", "Radarr/Sonarr", ""}}},
		{"an earlier match", "en", JournalEntry{Kind: "matched", Detail: map[string]any{"layer": "tmdb", "key_matched_at": 1762500000.0}},
			[]DetailPair{{"match made", "2025-11-07 09:20", ""}, {"matched by", "TMDB search", ""}}},
		{"an earlier match in Russian", "ru", JournalEntry{Kind: "matched", Detail: map[string]any{"layer": "arr", "key_matched_at": 1762500000.0}},
			[]DetailPair{{"сопоставлено", "07.11.2025 09:20", ""}, {"чем сопоставлено", "Radarr/Sonarr", ""}}},
		{"feed", "en", JournalEntry{Kind: "feed", Detail: map[string]any{"source": "rutracker"}}, []DetailPair{{"feed", "rutracker", ""}}},
		{"promoted", "ru", JournalEntry{Kind: "sighted", Detail: map[string]any{"from": "sample", "to": "feed"}}, []DetailPair{{"откуда", "sample", ""}, {"куда", "фид", ""}}},
		{"reuse", "en", JournalEntry{Kind: "classified", Detail: map[string]any{"from": "8f3c2a91d07e4b6c5a1e9f20b37d4c8e6a0f1b52"}},
			[]DetailPair{{"kind taken from", "8f3c2a91", "/t/8f3c2a91d07e4b6c5a1e9f20b37d4c8e6a0f1b52"}}},
		{"reuse from what is no infohash", "en", JournalEntry{Kind: "classified", Detail: map[string]any{"from": "3c1e"}}, []DetailPair{{"kind taken from", "3c1e", ""}}},
		{"a search", "en", JournalEntry{Kind: "probe_wanted", Detail: map[string]any{"t": "tvsearch", "id": "tmdbid:949", "query": "heat"}},
			[]DetailPair{{"searched by", "TMDB 949", ""}, {"query", "heat", ""}, {"search type", "series", ""}}},
		{"a search by IMDb and TVDB", "ru", JournalEntry{Kind: "probe_wanted", Detail: map[string]any{"t": "movie", "id": "imdbid:tt0113277"}},
			[]DetailPair{{"искали по", "IMDb tt0113277", ""}, {"тип поиска", "фильм", ""}}},
		{"a search by text", "en", JournalEntry{Kind: "probe_wanted", Detail: map[string]any{"t": "search", "id": "kpid:5"}},
			[]DetailPair{{"searched by", "kpid:5", ""}, {"search type", "text", ""}}},
		{"numbers grouped, IDs not", "ru", JournalEntry{Kind: "matched", Detail: map[string]any{"tmdb_id": 693134.0, "previous": 41000.0, "peers_week": 1284.0}},
			[]DetailPair{{"пиров за 7 дней", "1 284", ""}, {"ID прежнего произведения", "41000", ""}, {"TMDB ID", "693134", ""}}},
		{"numbers grouped in English", "en", JournalEntry{Kind: "fetched", Detail: map[string]any{"files": 1284.0}}, []DetailPair{{"files", "1,284", ""}}},
		{"a time on the entry's day", "en", JournalEntry{Kind: "leased", At: time.Date(2026, 10, 18, 19, 0, 0, 0, time.UTC),
			Detail: map[string]any{"lease_until": float64(time.Date(2026, 10, 18, 19, 1, 0, 0, time.UTC).Unix())}}, []DetailPair{{"leased until", "21:01", ""}}},
		{"a time on the entry's day in the zone, not in UTC", "en", JournalEntry{Kind: "leased", At: time.Date(2026, 10, 18, 22, 30, 0, 0, time.UTC),
			Detail: map[string]any{"lease_until": float64(time.Date(2026, 10, 19, 1, 0, 0, 0, time.UTC).Unix())}}, []DetailPair{{"leased until", "03:00", ""}}},
		{"a time on the next day in the zone", "en", JournalEntry{Kind: "leased", At: time.Date(2026, 10, 18, 21, 30, 0, 0, time.UTC),
			Detail: map[string]any{"lease_until": float64(time.Date(2026, 10, 18, 22, 30, 0, 0, time.UTC).Unix())}}, []DetailPair{{"leased until", "2026-10-19 00:30", ""}}},
		{"an infohash under any key", "en", JournalEntry{Kind: "fetched", Detail: map[string]any{"sibling": "8F3C2A91D07E4B6C5A1E9F20B37D4C8E6A0F1B52"}},
			[]DetailPair{{"sibling", "8f3c2a91", "/t/8f3c2a91d07e4b6c5a1e9f20b37d4c8e6a0f1b52"}}},
		{"a tracker page by its source", "en", JournalEntry{Kind: "feed", Detail: map[string]any{"source": "rutracker", "url": "https://rutracker.org/forum/viewtopic.php?t=1"}},
			[]DetailPair{{"feed", "rutracker", ""}, {"page", "rutracker", "https://rutracker.org/forum/viewtopic.php?t=1"}}},
		{"a tracker page without a source", "en", JournalEntry{Kind: "feed", Detail: map[string]any{"url": "https://rutor.info/t/1"}},
			[]DetailPair{{"page", "https://rutor.info/t/1", "https://rutor.info/t/1"}}},
		{"a page that is no web page", "en", JournalEntry{Kind: "feed", Detail: map[string]any{"source": "x", "url": "javascript:alert(1)"}},
			[]DetailPair{{"feed", "x", ""}, {"page", "javascript:alert(1)", ""}}},
	} {
		got := base(c.lang).JournalDetail(c.e)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if len(missing) > 0 {
		t.Errorf("untranslated keys: %v", missing)
	}
}

// The magnet link survives html/template's URL filter, with the name escaped.
func TestTorrentMagnetHref(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/t/x", nil)
	b := r.Base(req, "en", "", []string{"ru"}, time.UTC)
	p := SampleTorrent(b, false, "poster")
	p.Torrent.InfoHash = "8f3c2a91d07e4b6c5a1e9f20b37d4c8e6a0f1b52"
	p.Torrent.Name = `Чужой (1979) & "Aliens"+<b>#1`
	w := httptest.NewRecorder()
	if err := r.Render(w, req, "torrent", p); err != nil {
		t.Fatal(err)
	}
	body := w.Body.String()
	want := `href="magnet:?xt=urn:btih:8f3c2a91d07e4b6c5a1e9f20b37d4c8e6a0f1b52&amp;dn=%D0%A7%D1%83%D0%B6%D0%BE%D0%B9%20%281979%29%20%26%20%22Aliens%22%2B%3Cb%3E%231"`
	if !strings.Contains(body, want) {
		t.Errorf("no %s in the page", want)
	}
	if strings.Contains(body, "ZgotmplZ") {
		t.Error("the URL filter blocked a link")
	}
}

// Every page preloads both faces at the versioned URLs its @font-face rules
// name, and those URLs serve the font for a year.
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
		u := r.StaticURL(f)
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
		r.Static().ServeHTTP(sw, httptest.NewRequest("GET", u, nil))
		if sw.Code != 200 || sw.Header().Get("Content-Type") != "font/woff2" || sw.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Errorf("%s: %d %q %q", u, sw.Code, sw.Header().Get("Content-Type"), sw.Header().Get("Cache-Control"))
		}
	}
	sw := httptest.NewRecorder()
	r.Static().ServeHTTP(sw, httptest.NewRequest("GET", "/static/fonts/OFL.txt", nil))
	if sw.Code != 200 {
		t.Errorf("OFL.txt: %d", sw.Code)
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
	want := `<span class="tip" tabindex="0" aria-label="` + template.HTMLEscapeString(label+": "+text) + `">` +
		label + `<span class="tipb" aria-hidden="true">` + template.HTMLEscapeString(text) + `</span></span>`
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
	svg, err := r.Icon("copy")
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{b.T("card.copy_hash"), b.T("card.copy_magnet")} {
		re := regexp.MustCompile(`<span class="tip ic"><button type="button" class="ic" data-copy="[^"]+" data-done="` +
			regexp.QuoteMeta(b.T("card.copied")) + `" aria-label="` + regexp.QuoteMeta(label) + `">` +
			regexp.QuoteMeta(string(svg)) + `</button><span class="tipb" role="status">` + regexp.QuoteMeta(label) + `</span></span>`)
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
		svg, err := r.Icon(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range []string{`<svg `, `stroke="currentColor"`, `stroke-linecap="square"`, `aria-hidden="true"`} {
			if !strings.Contains(string(svg), s) {
				t.Errorf("%s: no %s", name, s)
			}
		}
	}
	if _, err := r.Icon("nope"); err == nil {
		t.Error("an unknown icon is not an error")
	}
}
