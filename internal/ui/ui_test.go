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
	oneWork := func(b Base) any {
		p := SampleTop(b, "movie", SampleOpenID, false)
		p.Works = p.Works[:1]
		return p
	}
	cases := []tc{
		{"home", "home", "/", "", func(b Base) any { b.Nav = "home"; return SampleHome(b) }},
		{"search", "search", "/?q=dune", "", func(b Base) any { b.Nav = "search"; return SampleSearch(b, "dune") }},
		{"search-results", "search", "/?q=dune", "results", func(b Base) any { return SampleSearch(b, "dune") }},
		{"top", "top", "/top", "", func(b Base) any { b.Nav = "top"; return SampleTop(b, "movie", SampleOpenID, false) }},
		{"top-series", "top", "/top?kind=series", "", func(b Base) any { b.Nav = "top"; return SampleTop(b, "series", "", true) }},
		{"top-frag", "top", "/top?kind=series", "top", func(b Base) any { return SampleTop(b, "series", "", false) }},
		{"top-work", "top", "/top/w/" + SampleOpenID + "?open=1", "work-" + SampleOpenID, oneWork},
		{"torrent-poster", "torrent", "/t/x", "", func(b Base) any { return SampleTorrent(b, false, "poster") }},
		{"torrent-noposter", "torrent", "/t/x", "", func(b Base) any { return SampleTorrent(b, false, "noposter") }},
		{"torrent-unmatched", "torrent", "/t/x", "", func(b Base) any { return SampleTorrent(b, false, "unmatched") }},
		{"torrent-unchecked", "torrent", "/t/x", "", func(b Base) any { return SampleTorrent(b, false, "unchecked") }},
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
			if c.name == "top-work" && !strings.HasPrefix(body, "<tbody") {
				t.Errorf("%s/%s: the fragment is not a bare <tbody>", lang, c.name)
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

// Renders every page without audio languages: no voice cells anywhere, and
// nothing of the extension in the output.
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
		{"search", "search", "/?q=dune", func(b Base) any {
			p := SampleSearch(b, "dune")
			p.Filters.VoiceLang = ""
			for i := range p.Results.Rows {
				p.Results.Rows[i].Voice = nil
			}
			return p
		}},
		{"top", "top", "/top", func(b Base) any {
			p := SampleTop(b, "movie", SampleOpenID, false)
			for i := range p.Works {
				p.Works[i].Voice = nil
				for j := range p.Works[i].Items {
					p.Works[i].Items[j].Voice = nil
				}
			}
			return p
		}},
		{"torrent", "torrent", "/t/x", func(b Base) any {
			p := SampleTorrent(b, false, "poster")
			p.Torrent.Voice = nil
			return p
		}},
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
			for _, s := range []string{`name="vl"`, `name="voice"`, `voice=`, `data-l="ru"`, b.T("d.voice"), b.T("voice.layer_mark")} {
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
		{ru.Voice(VoiceCell{Explicit: true}), "voice.explicit"}, // no msgs loaded here
	} {
		if x.got != x.want {
			t.Errorf("got %q, want %q", x.got, x.want)
		}
	}
	if niceCeil(15740) != 20000 || niceCeil(89187) != 100000 {
		t.Error("niceCeil")
	}
}
