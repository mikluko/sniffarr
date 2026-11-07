package ui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"
)

//go:embed templates/*.html static i18n/*.json icons/*.svg
var files embed.FS

// Page names and the fragments (htmx targets) each one can return alone.
var pages = map[string][]string{
	"torrents": {"torrents"},
	"top":      {"top"},
	"work":     {"releases", "act"},
	"torrent":  {"files", "act"},
	"data":     {"data"},
}

type chartArgs struct {
	B     Base
	C     Chart
	Label string
}

type workAudioArgs struct {
	B Base
	A []WorkAudio
	N int
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

// tipArgs are a tooltip's trigger Label and its Text.
type tipArgs struct {
	Label, Text string
}

// actsArgs are the buttons of the card at Path, whose releases have Peers
// over 7 days.
type actsArgs struct {
	B     Base
	Path  string
	Peers int64
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
	static map[string]string // file under static/ → version, a hash of its content
	icons  map[string]template.HTML
}

func New() (*Renderer, error) {
	r := &Renderer{tpl: map[string]*template.Template{}, msgs: map[string]map[string]string{}}
	var err error
	if r.static, err = staticVersions(); err != nil {
		return nil, err
	}
	if r.icons, err = loadIcons(); err != nil {
		return nil, err
	}
	funcs := template.FuncMap{
		"static":      r.StaticURL,
		"icon":        r.Icon,
		"kinds":       func() []string { return Kinds },
		"sources":     func() []string { return Sources },
		"layers":      func() []string { return Layers },
		"voiceModes":  func() []string { return VoiceModes },
		"voiceBands":  func() []string { return VoiceBands },
		"topKinds":    func() []string { return TopKinds },
		"windows":     func() []string { return Windows },
		"sorts":       func() []string { return Sorts },
		"qualities":   func() []string { return Qualities },
		"matchedOpts": func() []string { return []string{"yes", "no"} },
		"relSources":  func() []string { return ReleaseSources },
		"relSorts":    func() []string { return ReleaseSorts },
		"concat":      func(a ...string) string { return strings.Join(a, "") },
		"int64":       func(i int) int64 { return int64(i) },
		"add":         func(a, b int) int { return a + b },
		"safeCSS":     func(s string) template.CSS { return template.CSS(s) },
		"chartArgs": func(b Base, c Chart, label string) chartArgs {
			return chartArgs{B: b, C: c, Label: label}
		},
		"tmdbState": StateOf,
		"workAudio": WorkAudioOf,
		"workAudioArgs": func(b Base, a []WorkAudio, n int) workAudioArgs {
			return workAudioArgs{B: b, A: a, N: n}
		},
		"queueTasks": QueueTasks,
		"tipArgs":    func(label, text string) tipArgs { return tipArgs{Label: label, Text: text} },
		"actsArgs": func(b Base, path string, peers int64) actsArgs {
			return actsArgs{B: b, Path: path, Peers: peers}
		},
		"copyArgs": func(b Base, value, label string) copyArgs {
			return copyArgs{B: b, Value: value, Label: label}
		},
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

// staticVersions hashes every file under static/, keyed by its path there.
func staticVersions() (map[string]string, error) {
	versions := map[string]string{}
	err := fs.WalkDir(files, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		versions[strings.TrimPrefix(p, "static/")] = hex.EncodeToString(sum[:8])
		return nil
	})
	return versions, err
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

// StaticURL returns the URL of a file under static/ with its version, which
// changes with the file's content; a file that is not there has no version.
func (r *Renderer) StaticURL(p string) string {
	v, ok := r.static[p]
	if !ok {
		return "/static/" + p
	}
	return "/static/" + p + "?v=" + v
}

// Static serves /static/ (app.css, app.js, the fonts and the vendored htmx + idiomorph).
// A file asked for at its StaticURL is immutable for a year; at any other
// URL the browser revalidates it by ETag.
func (r *Renderer) Static() http.Handler {
	sub, _ := fs.Sub(files, "static")
	server := http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if v, ok := r.static[strings.TrimPrefix(req.URL.Path, "/static/")]; ok {
			h := w.Header()
			h.Set("ETag", `"`+v+`"`)
			h.Set("Cache-Control", "no-cache")
			if req.URL.Query().Get("v") == v {
				h.Set("Cache-Control", "public, max-age=31536000, immutable")
			}
		}
		server.ServeHTTP(w, req)
	})
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
