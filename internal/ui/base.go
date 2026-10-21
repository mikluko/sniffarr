package ui

import (
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Base is embedded in every page model. Templates call its methods for
// translation and locale-aware formatting: {{.T "key"}}, {{$.Num .N}}, ...
type Base struct {
	Lang       string   // "ru" | "en"
	Langs      []string // switcher entries
	Nav        string   // "home" | "search" | "torrents" | "top" | "data" | "" (card, title page)
	VoiceLangs []string // the API's dubbing languages, e.g. ["ru"]; none hides the voice filter, columns and card rows
	Path       string   // current path+query, for the language switcher
	Loc        *time.Location
	Now        time.Time

	msgs map[string]string
}

// T returns the message for key, or the key itself when missing.
func (b Base) T(key string) string {
	if s, ok := b.msgs[key]; ok {
		return s
	}
	if onMissing != nil {
		onMissing(b.Lang, key)
	}
	return key
}

// onMissing is set by tests to catch untranslated keys.
var onMissing func(lang, key string)

func (b Base) ru() bool { return b.Lang == "ru" }

// Num formats an integer with locale grouping: 1 284 311 / 1,284,311.
func (b Base) Num(n int64) string {
	sep := ","
	if b.ru() {
		sep = " "
	}
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var out []byte
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, sep...)
		}
		out = append(out, s[i])
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

func (b Base) dec(f float64, prec int) string {
	s := strconv.FormatFloat(f, 'f', prec, 64)
	if b.ru() {
		s = strings.Replace(s, ".", ",", 1)
	}
	return s
}

// Size formats bytes with binary units: 24,8 ГБ / 24.8 GB.
func (b Base) Size(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	if b.ru() {
		units = []string{"Б", "КБ", "МБ", "ГБ", "ТБ"}
	}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	prec := 1
	if i == 0 || f >= 100 {
		prec = 0
	}
	return b.dec(f, prec) + " " + units[i]
}

// Prob formats a probability/confidence with two decimals: 0,93.
func (b Base) Prob(p *float64) string {
	if p == nil {
		return "—"
	}
	return b.dec(*p, 2)
}

// Pct formats part/total as a percentage with one decimal: 24,4%.
func (b Base) Pct(part, total int64) string {
	if total == 0 {
		return "—"
	}
	return b.dec(math.Round(float64(part)*1000/float64(total))/10, 1) + "%"
}

// PctW returns part/total as a CSS width ("82.6%"), clamped to 100.
func (b Base) PctW(part, total int64) string {
	if total == 0 {
		return "0%"
	}
	v := math.Min(100, float64(part)*100/float64(total))
	return strconv.FormatFloat(v, 'f', 1, 64) + "%"
}

// Time formats a timestamp compactly: same year → "20.10 14:02", else "20.10.2025".
func (b Base) Time(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	loc := b.Loc
	if loc == nil {
		loc = time.Local
	}
	t = t.In(loc)
	now := b.Now
	if now.IsZero() {
		now = time.Now()
	}
	if b.ru() {
		if t.Year() == now.In(loc).Year() {
			return t.Format("02.01 15:04")
		}
		return t.Format("02.01.2006")
	}
	if t.Year() == now.In(loc).Year() {
		return t.Format("Jan 2 15:04")
	}
	return t.Format("2006-01-02")
}

// TimeFull formats a timestamp in full: 20.10.2026 14:02 / 2026-10-20 14:02.
func (b Base) TimeFull(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	loc := b.Loc
	if loc == nil {
		loc = time.Local
	}
	if b.ru() {
		return t.In(loc).Format("02.01.2006 15:04")
	}
	return t.In(loc).Format("2006-01-02 15:04")
}

// Delta formats a change against the previous window: "+12%", "−8%", "0%";
// nil means the work was absent then ("new").
func (b Base) Delta(d *float64) string {
	if d == nil {
		return b.T("top.new")
	}
	v := math.Round(*d * 100)
	switch {
	case v > 0:
		return "+" + strconv.FormatFloat(v, 'f', 0, 64) + "%"
	case v < 0:
		return "−" + strconv.FormatFloat(-v, 'f', 0, 64) + "%"
	}
	return "0%"
}

// SourceList joins translated source keys: "WEB, ремукс".
func (b Base) SourceList(keys []string) string {
	if len(keys) == 0 {
		return "—"
	}
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = b.T("source." + k)
	}
	return strings.Join(out, ", ")
}

// URL builds a top page link with the given parameters and the current dub language.
func (p TopPage) URL(kind, win, voice string, all bool) string {
	return p.url(kind, win, p.VoiceLang, voice, all)
}

// VoiceLangURL switches the dub language of the filter, keeping the rest.
func (p TopPage) VoiceLangURL(vl string) string {
	return p.url(p.Kind, p.Window, vl, p.VoiceMode, p.All)
}

func (p TopPage) url(kind, win, vl, voice string, all bool) string {
	v := url.Values{}
	v.Set("kind", kind)
	v.Set("win", win)
	p.setVoice(v, vl, voice)
	if all {
		v.Set("all", "1")
	}
	return "/top?" + v.Encode()
}

// setVoice adds vl (only when several languages are configured) and voice (unless "any").
func (p TopPage) setVoice(v url.Values, vl, voice string) {
	if len(p.VoiceLangs) > 1 && vl != "" {
		v.Set("vl", vl)
	}
	if voice != "" && voice != "any" {
		v.Set("voice", voice)
	}
}

// KindURL filters the titles tab by kind ("" = movies and series).
func (p SearchPage) KindURL(kind string) string {
	v := url.Values{}
	v.Set("q", p.Q)
	if kind != "" {
		v.Set("kind", kind)
	}
	return "/?" + v.Encode()
}

// Date formats a day: same year → "20.10" / "Oct 20", else "20.10.2025" / "2025-10-20".
func (b Base) Date(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	loc := b.Loc
	if loc == nil {
		loc = time.Local
	}
	t = t.In(loc)
	now := b.Now
	if now.IsZero() {
		now = time.Now()
	}
	same := t.Year() == now.In(loc).Year()
	switch {
	case b.ru() && same:
		return t.Format("02.01")
	case b.ru():
		return t.Format("02.01.2006")
	case same:
		return t.Format("Jan 2")
	}
	return t.Format("2006-01-02")
}

// Prob2 is Prob for a plain value.
func (b Base) Prob2(p float64) string { return b.dec(p, 2) }

// Fmt translates key and substitutes {0}, {1}, ... with args.
func (b Base) Fmt(key string, args ...string) string {
	s := b.T(key)
	for i, a := range args {
		s = strings.ReplaceAll(s, "{"+strconv.Itoa(i)+"}", a)
	}
	return s
}

// SE formats a series position: "S02E05", "S02" for a whole season, "—" when unknown.
func (b Base) SE(season, episode int) string {
	switch {
	case season <= 0:
		return "—"
	case episode <= 0:
		return fmt.Sprintf("S%02d", season)
	}
	return fmt.Sprintf("S%02dE%02d", season, episode)
}
