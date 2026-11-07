package ui

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// JournalView is the journal block of a card: its rows by day, newest first,
// and what its header says.
type JournalView struct {
	Base
	Days []JournalDay
	// Tip is the heading's tooltip.
	Tip string
	// From and To are the times of the oldest and the newest loaded entry.
	From, To time.Time
	// Retries and Finals count the loaded entries at those levels; Finals
	// is 0 where Gone states the failure.
	Retries, Finals int
	// Total is how many entries the journal holds.
	Total int64
	// Gone says what ended a torrent the store keeps no row of; nil for a
	// stored one.
	Gone *JournalGone
	// Cut reports, for a torrent the store keeps no row of, that every entry
	// is loaded and the oldest is within a day of the retention, so older
	// ones may have been deleted.
	Cut bool
}

// JournalDay is the rows of one day.
type JournalDay struct {
	// Key is the day, "2006-01-02" in Loc; Label its heading.
	Key, Label string
	Rows       []JournalRow
}

// JournalGone is what the card of a torrent the store keeps no row of says
// ended it: the newest entry when it is a final failure, else that entry
// as the last event, linked to its row.
type JournalGone struct {
	// Severity is the newest entry's level.
	Severity Severity
	// Final reports whether the newest entry ended the torrent.
	Final bool
	// Lead is the bold start; Rest what follows a final entry; Event the
	// last event's link text otherwise.
	Lead, Rest, Event string
	// ID is the newest entry's, the anchor of its row.
	ID int64
	// Note is the line under it.
	Note string
}

// JournalView builds the card's journal block from the torrent's loaded
// entries.
func (p TorrentPage) JournalView() JournalView {
	b, t := p.Base, p.Torrent
	rows := b.JournalRows(t.Journal)
	v := JournalView{Base: b, Tip: p.JournalTip(), Total: max(t.JournalTotal, int64(len(t.Journal)))}
	if n := len(t.Journal); n > 0 {
		v.From, v.To = t.Journal[0].At, t.Journal[n-1].At
	}
	v.Retries, v.Finals = failures(rows)
	if t.JournalOnly && len(rows) > 0 {
		v.Gone, v.Finals = b.journalGone(t.Journal, rows[0]), 0
		cut := b.JournalRetention - 24*time.Hour
		v.Cut = b.JournalRetention > 0 && v.Total <= int64(len(t.Journal)) && b.Now.Sub(v.From) >= cut
	}
	v.Days = b.journalDays(rows)
	return v
}

// failures counts the entries of the rows that failed and will be retried,
// and those that failed for good.
func failures(rows []JournalRow) (retries, finals int) {
	count := func(s Severity) {
		switch s {
		case SeverityRetry:
			retries++
		case SeverityFinal:
			finals++
		}
	}
	for _, r := range rows {
		if len(r.Steps) == 0 {
			count(r.Severity)
		}
		for _, s := range r.Steps {
			count(s.Severity)
		}
	}
	return retries, finals
}

// journalDays splits the rows, newest first, by their day in Loc.
func (b Base) journalDays(rows []JournalRow) []JournalDay {
	var out []JournalDay
	for _, r := range rows {
		key := r.At.In(b.loc()).Format(time.DateOnly)
		if len(out) == 0 || out[len(out)-1].Key != key {
			out = append(out, JournalDay{Key: key, Label: b.DayLabel(r.At)})
		}
		out[len(out)-1].Rows = append(out[len(out)-1].Rows, r)
	}
	return out
}

// journalGone is what ended the torrent of the entries, oldest first, whose
// newest row is newest.
func (b Base) journalGone(es []JournalEntry, newest JournalRow) *JournalGone {
	last := es[len(es)-1]
	g := &JournalGone{Severity: newest.End, Final: newest.End == SeverityFinal, ID: newest.ID}
	name, size := journalMeta(es)
	g.Note = b.T("journal.gone.unstored")
	for _, e := range es {
		if e.Kind == "deleted" {
			g.Note = b.T("journal.gone.deleted")
		}
	}
	switch {
	case name != "" && size > 0:
		g.Note += " " + b.Fmt("journal.gone.meta_size", name, b.Size(size))
	case name != "":
		g.Note += " " + b.Fmt("journal.gone.meta", name)
	}
	if !g.Final {
		g.Lead = b.T("journal.gone.lead")
		g.Event = b.Fmt("journal.gone.event", b.T("journal.kind."+last.Kind), b.outcomeLabel(last), b.Time(last.At))
		return g
	}
	what := b.T("journal.kind."+last.Kind) + ", " + b.outcomeLabel(last)
	if m := "journal.gone." + last.Kind + "." + last.Outcome; b.has(m) {
		what = b.T(m)
	}
	g.Lead = b.Fmt("journal.gone.final", what, b.Time(last.At))
	var parts []string
	for _, p := range b.JournalSummary(last) {
		if p.Name == "size" && size > 0 {
			continue
		}
		parts = append(parts, p.Text())
	}
	if len(parts) > 0 {
		g.Rest = ", " + strings.Join(parts, ", ")
	}
	g.Rest += "."
	return g
}

// journalMeta is the name and size the newest entry with a name records,
// or "" and 0.
func journalMeta(es []JournalEntry) (string, int64) {
	for i := len(es) - 1; i >= 0; i-- {
		if name, _ := es[i].Detail["name"].(string); name != "" {
			size, _ := es[i].Detail["size"].(float64)
			return name, int64(size)
		}
	}
	return "", 0
}

// Text is the pair as a collapsed row writes it.
func (p SummaryPair) Text() string {
	switch {
	case !p.Bare:
		return p.Key + ": " + p.Value
	case p.Key == "":
		return p.Value
	}
	return p.Key + " " + p.Value
}

// loc is Loc, or time.Local without one.
func (b Base) loc() *time.Location {
	if b.Loc == nil {
		return time.Local
	}
	return b.Loc
}

// DayLabel is the heading of the day of t in Loc: "19 октября", "Oct 19",
// with the year when it is not Now's.
func (b Base) DayLabel(t time.Time) string {
	t = t.In(b.loc())
	day, month := strconv.Itoa(t.Day()), b.T("journal.month."+strconv.Itoa(int(t.Month())))
	if t.Year() == b.Now.In(b.loc()).Year() {
		return b.Fmt("journal.day", day, month)
	}
	return b.Fmt("journal.day_year", day, month, strconv.Itoa(t.Year()))
}

// Clock is the time of t in Loc, "15:04".
func (b Base) Clock(t time.Time) string { return t.In(b.loc()).Format("15:04") }

// Stamp is t in Loc to the second: "19.10.2026 00:07:12", "2026-10-19 00:07:12".
func (b Base) Stamp(t time.Time) string {
	if b.ru() {
		return t.In(b.loc()).Format("02.01.2006 15:04:05")
	}
	return t.In(b.loc()).Format(time.DateTime)
}

// ISO is t as a <time> element's datetime, in UTC.
func (Base) ISO(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// JournalSpan is the time from one moment to another, as Time writes them, the
// second by its clock alone on the first's day: "18.10 20:57 – 19.10 00:07",
// "20.10 11:05 – 12:05"; one moment alone when they are the same.
func (b Base) JournalSpan(from, to time.Time) string {
	switch {
	case from.Equal(to):
		return b.Time(from)
	case from.In(b.loc()).Format(time.DateOnly) == to.In(b.loc()).Format(time.DateOnly):
		return b.Time(from) + " – " + b.Clock(to)
	}
	return b.Time(from) + " – " + b.Time(to)
}

// Gap writes the time between two rows as Span does, after a plus: "+58 мин".
func (b Base) Gap(d time.Duration) string { return "+" + b.Span(d) }

// RowTitle is the label of the row: a group's by "journal.group.<kind>",
// a single entry's by its kind, capitalised.
func (b Base) RowTitle(r JournalRow) string {
	if len(r.Steps) > 0 {
		return b.T("journal.group." + r.Kind)
	}
	return capitalised(b.T("journal.kind." + r.Kind))
}

// StepTitle is the label of a step within its group: by
// "journal.step.<kind>", else by its kind.
func (b Base) StepTitle(s JournalStep) string {
	if m := "journal.step." + s.Kind; b.has(m) {
		return b.T(m)
	}
	return b.T("journal.kind." + s.Kind)
}

// Mark is the class of the square that marks the level: "warn", "bad", or
// "" for none.
func (s Severity) Mark() string {
	switch s {
	case SeverityRetry:
		return "warn"
	case SeverityFinal:
		return "bad"
	}
	return ""
}

// TrackKind is the kind of the track in words.
func (b Base) TrackKind(t Track) string {
	if m := "journal.track." + t.Type; b.has(m) {
		return b.T(m)
	}
	return t.Type
}

// SeverityText is what a screen reader says for the mark of a level, "" for
// none.
func (b Base) SeverityText(s Severity) string {
	if s == SeverityNormal {
		return ""
	}
	return b.T("journal.sev." + s.String())
}

// Inline reports whether the row's step i shows its particulars under it:
// every step of the feeds of a title, else every step but the last, whose
// are the row's Details.
func (r JournalRow) Inline(i int) bool { return r.Kind == "feed" || i < len(r.Steps)-1 }

// KV is the particulars the open row lists apart: the last step's, unless
// its steps show each their own.
func (r JournalRow) KV() []DetailPair {
	if r.Kind == "feed" && len(r.Steps) > 0 {
		return nil
	}
	return r.Details
}

// Retention writes a period as days, or as hours when not whole days, by
// the noun key: "30 дней", "36 часов"; key "_gen" after the noun takes the
// form "older than" asks for.
func (b Base) Retention(d time.Duration, gen bool) string {
	suffix := ""
	if gen {
		suffix = "_gen"
	}
	if d%(24*time.Hour) == 0 {
		return b.Count(int64(d/(24*time.Hour)), "journal.days"+suffix)
	}
	return b.Count(int64(d/time.Hour), "journal.hours"+suffix)
}

// JournalTip is the tooltip of the journal's heading: how long entries are
// kept, and on a stored torrent's card that its start may be gone, then
// what is not journaled.
func (p TorrentPage) JournalTip() string {
	b := p.Base
	rest := b.T("journal.tip.rest")
	switch {
	case b.JournalRetention <= 0:
		return rest
	case p.Torrent.JournalOnly:
		return b.Fmt("journal.tip.kept_gone", b.Retention(b.JournalRetention, false)) + " " + rest
	}
	return b.Fmt("journal.tip.kept", b.Retention(b.JournalRetention, false)) + " " + rest
}

// capitalised is s with its first letter in upper case.
func capitalised(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}
