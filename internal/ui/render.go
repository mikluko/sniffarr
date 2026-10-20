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
)

//go:embed templates/*.html static i18n/*.json
var files embed.FS

// Page names and the fragments (htmx targets) each one can return alone.
// A target "work-<id>" matches the fragment "work".
var pages = map[string][]string{
	"home":    {},
	"search":  {"results"},
	"top":     {"top", "work"},
	"torrent": {"files"},
	"data":    {"data"},
}

type chartArgs struct {
	B     Base
	C     Chart
	Label string
}

type Renderer struct {
	tpl   map[string]*template.Template
	msgs  map[string]map[string]string
	langs []string
}

func New() (*Renderer, error) {
	r := &Renderer{tpl: map[string]*template.Template{}, msgs: map[string]map[string]string{}}
	funcs := template.FuncMap{
		"kinds":      func() []string { return Kinds },
		"sources":    func() []string { return Sources },
		"layers":     func() []string { return Layers },
		"voiceModes": func() []string { return VoiceModes },
		"voiceBands": func() []string { return VoiceBands },
		"topKinds":   func() []string { return TopKinds },
		"windows":    func() []string { return Windows },
		"sorts":      func() []string { return Sorts },
		"concat":     func(a ...string) string { return strings.Join(a, "") },
		"int64":      func(i int) int64 { return int64(i) },
		"add":        func(a, b int) int { return a + b },
		"safeCSS":    func(s string) template.CSS { return template.CSS(s) },
		"chartArgs": func(b Base, c Chart, label string) chartArgs {
			return chartArgs{B: b, C: c, Label: label}
		},
		"tmdbState":   StateOf,
		"lowConf":     func(c float64) bool { return c < LowConfidence },
		"tmdbImg":     TMDBImg,
		"tmdbSearch":  TMDBSearchURL,
		"imdbSearch":  IMDbSearchURL,
		"posterThumb": func() string { return PosterThumb },
		"posterCard":  func() string { return PosterCard },
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
//	lang: from Lang(); nav: "home" | "search" | "top" | "data" | "".
func (r *Renderer) Base(req *http.Request, lang, nav string, voiceLangs []string, loc *time.Location) Base {
	if _, ok := r.msgs[lang]; !ok {
		lang = "en"
	}
	return Base{
		Lang: lang, Langs: []string{"ru", "en"}, Nav: nav,
		VoiceLangs: voiceLangs, Path: req.URL.RequestURI(),
		Loc: loc, Now: time.Now(), msgs: r.msgs[lang],
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
	_, err := buf.WriteTo(w)
	return err
}

// Static serves /static/ (app.css, app.js and the vendored htmx + idiomorph).
func Static() http.Handler {
	sub, _ := fs.Sub(files, "static")
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
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
