package ui

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"

	"sarak.sh/mikluko/libarr/static"
	"sarak.sh/mikluko/libarr/version"
)

//go:embed templates/*.html static i18n/*.json icons/*.svg
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
	langs  []string
	static *static.Files
	icons  map[string]template.HTML
}

func New() (*Renderer, error) {
	r := &Renderer{tpl: map[string]*template.Template{}, msgs: map[string]map[string]string{}}
	sub, err := fs.Sub(files, "static")
	if err != nil {
		return nil, err
	}
	if r.static, err = static.New(sub, "/static/"); err != nil {
		return nil, err
	}
	if r.icons, err = loadIcons(); err != nil {
		return nil, err
	}
	funcs := template.FuncMap{
		"static":      r.static.URL,
		"icon":        r.Icon,
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
	for name := range pages {
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(files,
			"templates/layout.html", "templates/chart.html", "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		r.tpl[name] = t
	}
	ents, err := fs.Glob(files, "i18n/*.json")
	if err != nil {
		return nil, err
	}
	for _, p := range ents {
		b, err := files.ReadFile(p)
		if err != nil {
			return nil, err
		}
		m := map[string]string{}
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		lang := strings.TrimSuffix(strings.TrimPrefix(p, "i18n/"), ".json")
		r.msgs[lang] = m
		r.langs = append(r.langs, lang)
	}
	return r, nil
}

// Base builds the common page model for a request.
//
//	lang: from Lang(); nav: "data" | "torrents" | "top" | "".
func (r *Renderer) Base(req *http.Request, lang, nav string, voiceLangs []string, loc *time.Location) Base {
	if _, ok := r.msgs[lang]; !ok {
		lang = "en"
	}
	return Base{
		Lang: lang, Langs: []string{"ru", "en"}, Nav: nav,
		VoiceLangs: voiceLangs, Path: req.URL.RequestURI(),
		Build: version.Get(), Loc: loc, Now: time.Now(), msgs: r.msgs[lang],
	}
}

// Lang resolves the UI language: ?lang= (switcher, also sets a cookie) →
// "lang" cookie → userLocale, the signed-in person's locale → en.
// Any locale starting with "ru" maps to "ru"; "C", "POSIX" and "en*" map to "en".
func Lang(w http.ResponseWriter, req *http.Request, userLocale string) string {
	pick := func(s string) string {
		if strings.HasPrefix(strings.ToLower(s), "ru") {
			return "ru"
		}
		if s == "C" || s == "POSIX" || strings.HasPrefix(strings.ToLower(s), "en") {
			return "en"
		}
		return ""
	}
	if l := pick(req.URL.Query().Get("lang")); l != "" {
		http.SetCookie(w, &http.Cookie{Name: "lang", Value: l, Path: "/", MaxAge: 365 * 24 * 3600,
			HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
		return l
	}
	if c, err := req.Cookie("lang"); err == nil {
		if l := pick(c.Value); l != "" {
			return l
		}
	}
	if l := pick(userLocale); l != "" {
		return l
	}
	return "en"
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

// loadIcons reads every icons/<name>.svg, keyed by name.
func loadIcons() (map[string]template.HTML, error) {
	paths, err := fs.Glob(files, "icons/*.svg")
	if err != nil {
		return nil, err
	}
	icons := map[string]template.HTML{}
	for _, p := range paths {
		b, err := files.ReadFile(p)
		if err != nil {
			return nil, err
		}
		icons[strings.TrimSuffix(strings.TrimPrefix(p, "icons/"), ".svg")] = template.HTML(strings.TrimSpace(string(b)))
	}
	return icons, nil
}

// Icon returns the inline SVG of the icon name, drawn in currentColor and
// hidden from assistive technology; an unknown name is an error.
func (r *Renderer) Icon(name string) (template.HTML, error) {
	svg, ok := r.icons[name]
	if !ok {
		return "", fmt.Errorf("no icon %q", name)
	}
	return svg, nil
}

// Static serves /static/: app.css, app.js, the fonts and the vendored htmx
// and idiomorph, at the versioned URLs its URL gives.
func (r *Renderer) Static() *static.Files {
	return r.static
}

// LangURL returns the current URL with lang=<l>, for the switcher.
func (b Base) LangURL(l string) string {
	u, err := url.Parse(b.Path)
	if err != nil {
		return "?lang=" + l
	}
	q := u.Query()
	q.Set("lang", l)
	u.RawQuery = q.Encode()
	return u.RequestURI()
}
