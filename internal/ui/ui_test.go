package ui

import (
	"net/http/httptest"
	"os"
	"path/filepath"
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
		{"home", "home", "/", "", func(b Base) any { b.Nav = "home"; return SampleHome(b) }},
		{"search", "search", "/?q=spider", "", func(b Base) any { b.Nav = "search"; return SampleSearch(b, "spider", "") }},
		{"search-series", "search", "/?q=spider&kind=series", "results", func(b Base) any { return SampleSearch(b, "spider", "series") }},
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
		{"work", "work", "/w/" + SampleSpiderID, "", func(b Base) any { return SampleWork(b, SampleSpiderID, ReleaseFilters{}) }},
		{"work-explicit", "work", "/w/" + SampleSpiderID + "?voice=explicit", "releases", func(b Base) any {
			return SampleWork(b, SampleSpiderID, ReleaseFilters{Voice: "explicit"})
		}},
		{"work-empty", "work", "/w/" + SampleSpiderID + "?quality=sd&source=remux", "releases", func(b Base) any {
			return SampleWork(b, SampleSpiderID, ReleaseFilters{Quality: "sd", Source: "remux"})
		}},
		{"work-series", "work", "/w/" + SampleFalloutID, "", func(b Base) any { return SampleWork(b, SampleFalloutID, ReleaseFilters{Season: "2"}) }},
		{"work-series-multi", "work", "/w/" + SampleFalloutID, "", func(b Base) any { multi(&b); return SampleWork(b, SampleFalloutID, ReleaseFilters{}) }},
		{"work-tmdbonly", "work", "/w/" + SampleTMDBOnlyID, "", func(b Base) any { return SampleWork(b, SampleTMDBOnlyID, ReleaseFilters{}) }},
		{"work-nolangs", "work", "/w/" + SampleSpiderID, "", func(b Base) any { b.VoiceLangs = nil; return SampleWork(b, SampleSpiderID, ReleaseFilters{}) }},
		{"torrent-poster", "torrent", "/t/x", "", func(b Base) any { return SampleTorrent(b, false, "poster") }},
		{"torrent-noposter", "torrent", "/t/x", "", func(b Base) any { return SampleTorrent(b, false, "noposter") }},
		{"torrent-unmatched", "torrent", "/t/x", "", func(b Base) any { return SampleTorrent(b, false, "unmatched") }},
		{"torrent-unchecked", "torrent", "/t/x", "", func(b Base) any { return SampleTorrent(b, false, "unchecked") }},
		{"torrent-multilang", "torrent", "/t/x", "", func(b Base) any { multi(&b); return SampleTorrent(b, false, "poster") }},
		{"torrent-files", "torrent", "/t/x?files=all", "files", func(b Base) any { return SampleTorrent(b, true, "poster") }},
		{"data", "data", "/data", "", func(b Base) any { b.Nav = "data"; return SampleData(b) }},
		{"data-frag", "data", "/data", "data", func(b Base) any { return SampleData(b) }},
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
		{"home", "home", "/", func(b Base) any { return SampleHome(b) }},
		{"search", "search", "/?q=spider", func(b Base) any { return SampleSearch(b, "spider", "") }},
		{"torrents", "torrents", "/torrents", func(b Base) any { return SampleTorrents(b, "", Filters{}) }},
		{"top", "top", "/top", func(b Base) any { return SampleTop(b, "movie", false) }},
		{"work", "work", "/w/" + SampleSpiderID, func(b Base) any { return SampleWork(b, SampleSpiderID, ReleaseFilters{}) }},
		{"torrent", "torrent", "/t/x", func(b Base) any { return SampleTorrent(b, false, "poster") }},
		{"data", "data", "/data", func(b Base) any {
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
	ru := Base{Lang: "ru"}
	c := Base{Lang: "en"}
	for _, x := range []struct{ got, want string }{
		{ru.Num(1284311), "1 284 311"},
		{c.Num(1284311), "1,284,311"},
		{ru.Size(26629001216), "24,8 ГБ"},
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
		{ru.SE(2, 5), "S02E05"},
		{ru.SE(2, 0), "S02"},
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
