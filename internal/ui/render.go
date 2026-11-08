package ui

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"sarak.sh/mikluko/libarr/static"
	libui "sarak.sh/mikluko/libarr/ui"
	"sarak.sh/mikluko/libarr/version"
)

//go:embed templates/*.html static i18n/*.json
var files embed.FS

// Page names and the fragments (htmx targets) each one can return alone.
var pages = map[string][]string{
	"torrents": {"torrents"},
	"top":      {"top"},
	"work":     {"releases", "act"},
	"torrent":  {"files", "act", "jlist", "jpage"},
	"data":     {"data"},
	"notfound": nil,
}

type chartArgs struct {
	B     Base
	C     Chart
	Label string
}

type audioArgs struct {
	B Base
	A []Audio
}

// copyArgs are an icon button that copies Value, named by Label.
type copyArgs struct {
	B            Base
	Value, Label string
}

// limitArgs are a daily limit's figures: Done so far, Pace, what the day
// comes to at its pace so far, 0 for none, and Limit.
type limitArgs struct {
	B                 Base
	Done, Pace, Limit int64
}

// shareArgs are a store row's count N out of Total.
type shareArgs struct {
	B        Base
	N, Total int64
}

// jtimeArgs are a journal time, At, written in the zone of B.
type jtimeArgs struct {
	B  Base
	At time.Time
}

// jmarkArgs are a journal row's or step's level, S, marked in B's language.
type jmarkArgs struct {
	B Base
	S Severity
}

// tipArgs are a tooltip's trigger Label and its Text.
type tipArgs struct {
	Label, Text string
}

// actsArgs are the buttons of the card at Path, whose releases have Peers
// over 7 days; Match adds the one asking for a TMDB lookup.
type actsArgs struct {
	B     Base
	Path  string
	Peers int64
	Match bool
}

// actButton is the button of the card at Path asking for Action, disabled
// when Off; OOB has an htmx answer put it in place of the one on the page.
type actButton struct {
	B            Base
	Path, Action string
	Off, OOB     bool
}

// baseOf accepts a Base or any page model that embeds one.
func baseOf(v any) Base {
	switch p := v.(type) {
	case Base:
		return p
	case TopPage:
		return p.Base
	case TorrentPage:
		return p.Base
	case WorkPage:
		return p.Base
	case TorrentsPage:
		return p.Base
	}
	return Base{}
}

type Renderer struct {
	tpl    map[string]*template.Template
	msgs   map[string]map[string]string
	static *static.Files
	shared *libui.Assets
}

func New() (*Renderer, error) {
	r := &Renderer{tpl: map[string]*template.Template{}}
	sub, err := fs.Sub(files, "static")
	if err != nil {
		return nil, err
	}
	if r.static, err = static.New(sub, "/static/"); err != nil {
		return nil, err
	}
	if r.shared, err = libui.New("/ui/"); err != nil {
		return nil, err
	}
	funcs := template.FuncMap{
		"static":      r.static.URL,
		"kinds":       func() []string { return Kinds },
		"sources":     func() []string { return Sources },
		"voiceModes":  func() []string { return VoiceModes },
		"voiceBands":  func() []string { return VoiceBands },
		"topKinds":    func() []string { return TopKinds },
		"windows":     func() []string { return Windows },
		"sorts":       func() []string { return Sorts },
		"qualities":   func() []string { return Qualities },
		"matchedOpts": func() []string { return []string{"yes", "no"} },
		"relSorts":    func() []string { return ReleaseSorts },
		"concat":      func(a ...string) string { return strings.Join(a, "") },
		"int64":       func(i int) int64 { return int64(i) },
		"add":         func(a, b int) int { return a + b },
		"safeCSS":     func(s string) template.CSS { return template.CSS(s) },
		"chartArgs": func(b Base, c Chart, label string) chartArgs {
			return chartArgs{B: b, C: c, Label: label}
		},
		"tmdbState":  StateOf,
		"workAudio":  WorkAudioOf,
		"queueTasks": QueueTasks,
		"tipArgs":    func(label, text string) tipArgs { return tipArgs{Label: label, Text: text} },
		"shareArgs":  func(b Base, n, total int64) shareArgs { return shareArgs{B: b, N: n, Total: total} },
		"limitArgs": func(b Base, done, pace, limit int64) limitArgs {
			return limitArgs{B: b, Done: done, Pace: pace, Limit: limit}
		},
		"actsArgs": func(b Base, path string, peers int64, match ...bool) actsArgs {
			return actsArgs{B: b, Path: path, Peers: peers, Match: len(match) > 0 && match[0]}
		},
		"actArgs": func(b Base, path, action string, off bool) actButton {
			return actButton{B: b, Path: path, Action: action, Off: off}
		},
		"actOOB": func(b Base, action string) actButton {
			return actButton{B: b, Action: action, Off: true, OOB: true}
		},
		"copyArgs": func(b Base, value, label string) copyArgs {
			return copyArgs{B: b, Value: value, Label: label}
		},
		"jtimeArgs": func(b Base, at time.Time) jtimeArgs { return jtimeArgs{B: b, At: at} },
		"jmarkArgs": func(b Base, s Severity) jmarkArgs { return jmarkArgs{B: b, S: s} },
		"audioArgs": func(b any, a []Audio) audioArgs {
			return audioArgs{B: baseOf(b), A: a}
		},
		"audioOf":     func(list []Audio, lang string) Audio { a, _ := AudioOf(list, lang); return a },
		"lowConf":     func(c float64) bool { return c < LowConfidence },
		"tmdbImg":     TMDBImg,
		"tmdbSearch":  TMDBSearchURL,
		"imdbSearch":  IMDbSearchURL,
		"posterThumb": func() string { return PosterThumb },
		"posterCard":  func() string { return PosterCard },
		"posterWork":  func() string { return PosterWork },
		"posterWork2": func() string { return PosterWork2 },
		"pathEscape":  url.PathEscape,
		"queryEscape": url.QueryEscape,
	}
	maps.Copy(funcs, r.shared.FuncMapNamed("shared", "icon"))
	for name := range pages {
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(files,
			"templates/layout.html", "templates/chart.html", "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		r.tpl[name] = t
	}
	i18n, err := fs.Sub(files, "i18n")
	if err != nil {
		return nil, err
	}
	if r.msgs, err = libui.LoadMessages(i18n, langs); err != nil {
		return nil, err
	}
	return r, nil
}

// langs are the languages of the UI, the default first.
var langs = []string{"en", "ru"}

// Base builds the common page model for a request.
//
//	lang: from Lang(); nav: "data" | "torrents" | "top" | "".
func (r *Renderer) Base(req *http.Request, lang, nav string, voiceLangs []string, loc *time.Location) Base {
	if _, ok := r.msgs[lang]; !ok {
		lang = langs[0]
	}
	return Base{
		Locale: libui.Locale{Lang: lang, Path: req.URL.RequestURI(), Loc: loc, Now: time.Now(), Messages: r.msgs[lang]},
		Langs:  []string{"ru", "en"}, Nav: nav, VoiceLangs: voiceLangs, Build: version.Get(),
	}
}

// Lang resolves the UI language as libarr's ui.Lang does, falling back on
// userLocale, the signed-in person's locale: one starting with "ru" is
// "ru"; "C", "POSIX" and one starting with "en" are "en".
func Lang(w http.ResponseWriter, req *http.Request, userLocale string) string {
	return libui.Lang(w, req, langs, func(*http.Request) string {
		switch l := strings.ToLower(userLocale); {
		case strings.HasPrefix(l, "ru"):
			return "ru"
		case userLocale == "C", userLocale == "POSIX", strings.HasPrefix(l, "en"):
			return "en"
		}
		return ""
	})
}

// Render writes the full page, or only the fragment htmx asked for:
// HX-Request: true + HX-Target: <fragment id> (or <fragment>-<id>) → that block alone.
// History restores always get the full page.
func (r *Renderer) Render(w http.ResponseWriter, req *http.Request, page string, data any) error {
	return r.RenderStatus(w, req, page, http.StatusOK, data)
}

// RenderStatus is Render answering with the status code.
func (r *Renderer) RenderStatus(w http.ResponseWriter, req *http.Request, page string, code int, data any) error {
	t, ok := r.tpl[page]
	if !ok {
		return fmt.Errorf("unknown page %q", page)
	}
	name := "layout.html"
	if req.Header.Get("HX-Request") == "true" && req.Header.Get("HX-History-Restore-Request") != "true" {
		target := req.Header.Get("HX-Target")
		for _, f := range pages[page] {
			if target == f || strings.HasPrefix(target, f+"-") {
				name = f
			}
		}
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Add("Vary", "HX-Request, HX-Target")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, err := buf.WriteTo(w)
	return err
}

// Static serves /static/: app.css, app.js and the TMDB logo, at the
// versioned URLs its URL gives.
func (r *Renderer) Static() *static.Files {
	return r.static
}

// Shared serves /ui/, libarr's design system, of which the pages load
// app.js, the fonts, htmx and idiomorph.
func (r *Renderer) Shared() http.Handler {
	return r.shared.Handler()
}
