package ui

import (
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mikluko/sniffarr/internal/latest"
	"github.com/mikluko/sniffarr/internal/version"
)

// Base is embedded in every page model. Templates call its methods for
// translation and locale-aware formatting: {{.T "key"}}, {{$.Num .N}}, ...
type Base struct {
	Lang       string       // "ru" | "en"
	Langs      []string     // switcher entries
	Nav        string       // "torrents" | "top" select that header tab; "data" is the logo's page, which selects none; "" is any other
	VoiceLangs []string     // the API's dubbing languages, e.g. ["ru"]; none hides the voice filter, columns and card rows
	Path       string       // current path+query, for the language switcher
	Seerr      string       // base URL of a Seerr the cards link titles to; "" links none
	Radarr     string       // root URL of a Radarr the cards link movies to; "" links none
	Sonarr     string       // root URL of a Sonarr the cards link series to; "" links none
	Probes     bool         // whether the API probes releases; false hides the probe button, meter and queue
	Classifier bool         // whether the API runs the classifier; false hides its meter and layer on the data page
	Feeds      bool         // whether the API reads tracker feeds; false labels P2 as feeds turned off
	Build      version.Info // the build the footer names
	Loc        *time.Location
	Now        time.Time

	// JournalRetention is how long the API keeps a journal entry; 0 when unknown.
	JournalRetention time.Duration

	// Latest is the latest stable release the API knows; zero when none.
	Latest latest.Release

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

// Priority is what queues tasks at priority p, naming probes only while the
// API probes and feeds as off while it reads none.
func (b Base) Priority(p int) string {
	switch {
	case !b.Probes && (p == 1 || p == 4):
		return b.T(fmt.Sprintf("q.p%d_noprobes", p))
	case !b.Feeds && p == 2:
		return b.T("q.p2_nofeeds")
	}
	return b.T(fmt.Sprintf("q.p%d", p))
}

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

// Count is n followed by the form of the noun key that agrees with it:
// key+".one", ".few" or ".many", by Russian rules in Russian; English has
// no ".few".
func (b Base) Count(n int64, key string) string {
	return b.Num(n) + "\u00a0" + b.T(key+"."+b.form(n))
}

func (b Base) form(n int64) string {
	n10, n100 := n%10, n%100
	switch {
	case !b.ru() && n == 1, b.ru() && n10 == 1 && n100 != 11:
		return "one"
	case b.ru() && n10 >= 2 && n10 <= 4 && (n100 < 12 || n100 > 14):
		return "few"
	}
	return "many"
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

// BuildTip is the tooltip of the footer's version, "Built 2026-11-07 14:02 UTC,
// commit 1a2b3c4."; empty unless the build's time and commit are both known.
func (b Base) BuildTip() string {
	if b.Build.Built.IsZero() || b.Build.Commit == "" {
		return ""
	}
	utc := b
	utc.Loc = time.UTC
	return b.Fmt("foot.build", utc.TimeFull(b.Build.Built), b.Build.Commit)
}

// thisYear reports whether t falls in Now's year in Loc.
func (b Base) thisYear(t time.Time) bool {
	now := b.Now
	if now.IsZero() {
		now = time.Now()
	}
	return t.In(b.loc()).Year() == now.In(b.loc()).Year()
}

// Time formats a timestamp compactly: "20.10 14:02" / "Oct 20, 14:02", and
// a timestamp of another year by its day alone: "20.10.2025" / "Oct 20, 2025".
func (b Base) Time(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	layout := "Jan 2, 2006"
	switch {
	case b.ru() && b.thisYear(t):
		layout = "02.01 15:04"
	case b.ru():
		layout = "02.01.2006"
	case b.thisYear(t):
		layout = "Jan 2, 15:04"
	}
	return t.In(b.loc()).Format(layout)
}

// TimeFull formats a timestamp with its year: "20.10.2026 14:02" / "Oct 20, 2026, 14:02".
func (b Base) TimeFull(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	if b.ru() {
		return t.In(b.loc()).Format("02.01.2006 15:04")
	}
	return t.In(b.loc()).Format("Jan 2, 2006, 15:04")
}

// Ago formats how long before Now t was in whole minutes by "ago.min", or
// by "ago.now" under a minute.
func (b Base) Ago(t time.Time) string {
	now := b.Now
	if now.IsZero() {
		now = time.Now()
	}
	m := int64(now.Sub(t) / time.Minute)
	if m < 1 {
		return b.T("ago.now")
	}
	return b.Fmt("ago.min", b.Num(m))
}

// Moved formats the places a work moved from prev to rank: "+3", "−1", "=";
// prev 0 means it had no peers then, "—".
func (b Base) Moved(rank, prev int) string {
	if prev == 0 {
		return "—"
	}
	switch d := prev - rank; {
	case d > 0:
		return "+" + strconv.Itoa(d)
	case d < 0:
		return "−" + strconv.Itoa(-d)
	}
	return "="
}

// PrevPlace is the tooltip of a work's place prev in the previous window,
// 0 for none: "В прошлом окне: 9 787-е место." / "Not in the previous window.".
func (b Base) PrevPlace(prev int) string {
	if prev == 0 {
		return b.T("top.prev_none")
	}
	n := b.Num(int64(prev))
	if !b.ru() {
		n += ordinal(prev)
	}
	return b.Fmt("top.prev_place", n)
}

// ordinal is the English suffix of the ordinal n: "st", "nd", "rd" or "th".
func ordinal(n int) string {
	if n%100 >= 11 && n%100 <= 13 {
		return "th"
	}
	switch n % 10 {
	case 1:
		return "st"
	case 2:
		return "nd"
	case 3:
		return "rd"
	}
	return "th"
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

// Date formats a day: "20.10" / "Oct 20", with the year when it is not
// Now's: "20.10.2025" / "Oct 20, 2025".
func (b Base) Date(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	if b.thisYear(t) {
		if b.ru() {
			return t.In(b.loc()).Format("02.01")
		}
		return t.In(b.loc()).Format("Jan 2")
	}
	return b.FullDate(t)
}

// RankN writes a place: "№2" / "#2".
func (b Base) RankN(n int) string { return b.Fmt("rank.n", b.Num(int64(n))) }

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
