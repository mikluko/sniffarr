package ui

import (
	"slices"
	"strings"
	"time"
)

// JournalRow is one row of the card's journal: a single entry, a fetch or
// probe chain of attempts, or the feeds of one title in a row.
type JournalRow struct {
	// ID is the ID of the last step, the row's anchor.
	ID int64
	// Kind is "fetch", "probe" or "feed" for a group, the entry's kind for a
	// single entry.
	Kind string
	// At is the time of the last step.
	At time.Time
	// Gap is the time since the older row, 0 under JournalGap.
	Gap time.Duration
	// Severity is the worst level of the row's entries; End the last one's.
	Severity, End Severity
	// Outcome is the label of how the row ended.
	Outcome string
	// Summary is the particulars the collapsed row shows.
	Summary []SummaryPair
	// Details is every particular of the last step but the tracks Tracks
	// lists.
	Details []DetailPair
	// Steps is the group's entries, oldest first; none for a single entry.
	Steps []JournalStep
	// Tracks is the tracks the last step read.
	Tracks []Track
}

// JournalStep is one entry of a row's group.
type JournalStep struct {
	At            time.Time
	Kind, Outcome string
	Severity      Severity
	Summary       []SummaryPair
	Details       []DetailPair
}

// JournalGap is the least time between two rows that JournalRow.Gap shows.
const JournalGap = 30 * time.Minute

// JournalRows groups the journal's entries, oldest first, into the rows the
// card shows, newest first.
func (b Base) JournalRows(es []JournalEntry) []JournalRow {
	levels := make([]Severity, len(es))
	for i, e := range es {
		levels[i] = e.Level()
	}
	groups := groupJournal(es, levels)
	rows := make([]JournalRow, len(groups))
	for i, g := range groups {
		rows[len(groups)-1-i] = b.journalRow(es, levels, g)
	}
	for i := range len(rows) - 1 {
		if d := rows[i].At.Sub(rows[i+1].At); d >= JournalGap {
			rows[i].Gap = d
		}
	}
	return rows
}

// journalRow is the row of the group of the entries at the indices.
func (b Base) journalRow(es []JournalEntry, levels []Severity, g []int) JournalRow {
	last := es[g[len(g)-1]]
	r := JournalRow{ID: last.ID, Kind: last.Kind, At: last.At, End: levels[g[len(g)-1]], Details: b.JournalDetail(last), Tracks: last.Tracks()}
	if len(r.Tracks) > 0 {
		r.Details = b.journalDetail(last, "tracks")
	}
	for _, i := range g {
		r.Severity = max(r.Severity, levels[i])
	}
	if len(g) == 1 {
		r.Outcome, r.Summary = b.outcomeLabel(last), b.JournalSummary(last)
		return r
	}
	steps := make([]JournalEntry, len(g))
	for j, i := range g {
		e := es[i]
		steps[j] = e
		r.Steps = append(r.Steps, JournalStep{e.At, e.Kind, e.Outcome, levels[i], b.JournalSummary(e), b.JournalDetail(e)})
	}
	r.Kind = journalFamily(last.Kind)
	if r.Kind == "feed" {
		r.Outcome, r.Summary = b.feedOutcome(steps), b.feedSummary(steps)
		return r
	}
	r.Outcome, r.Summary = b.chainOutcome(steps, r.End), b.JournalSummary(last)
	return r
}

// outcomeLabel is the label of the entry's outcome.
func (b Base) outcomeLabel(e JournalEntry) string {
	return b.T("journal.outcome." + e.Kind + "." + e.Outcome)
}

// chainOutcome labels how a chain of attempts ended, its last step at the
// level: on the attempt it got its data on, after the attempts it gave up
// after, or by the last step's outcome alone after a single attempt or while
// the chain goes on.
func (b Base) chainOutcome(steps []JournalEntry, level Severity) string {
	last := steps[len(steps)-1]
	label := b.outcomeLabel(last)
	var n int64
	for _, e := range steps {
		if a, ok := e.Detail["attempt"].(float64); ok {
			n = max(n, int64(a))
		}
	}
	switch {
	case n < 2 || !closesChain(last, level):
		return label
	case failsAttempt(last):
		return b.Fmt("journal.row.after", label, b.Count(n, "journal.attempts"))
	}
	return b.onAttempt(label, n)
}

// onAttempt is the label of an outcome reached on the nth attempt: "stored
// on the 2nd attempt", "сохранены со 2-й попытки"; Russian says "со" before
// the 2nd alone, the one ordinal of these that opens on two consonants.
func (b Base) onAttempt(label string, n int64) string {
	key := "journal.row.on_attempt"
	if n == 2 {
		key = "journal.row.on_attempt_so"
	}
	return b.Fmt(key, label, b.ordinal(n))
}

// ordinal writes n as an ordinal by "journal.ordinal.<suffix>", the suffix
// of English rules: 1st, 2nd, 3rd, 11th.
func (b Base) ordinal(n int64) string {
	suffix := "th"
	switch {
	case n%100 >= 11 && n%100 <= 13:
	case n%10 == 1:
		suffix = "st"
	case n%10 == 2:
		suffix = "nd"
	case n%10 == 3:
		suffix = "rd"
	}
	return b.Fmt("journal.ordinal."+suffix, b.Num(n))
}

// feedOutcome labels the feeds of one title by how many times and in which
// feeds it was listed: "2 times: rutracker, rutor".
func (b Base) feedOutcome(steps []JournalEntry) string {
	var sources []string
	for _, e := range steps {
		if s, _ := e.Detail["source"].(string); s != "" && !slices.Contains(sources, s) {
			sources = append(sources, s)
		}
	}
	return b.Fmt("journal.row.feeds", b.times(int64(len(steps))), strings.Join(sources, ", "))
}

// feedSummary is the size the last feed lists and the earliest publication
// of the feeds.
func (b Base) feedSummary(steps []JournalEntry) []SummaryPair {
	last := steps[len(steps)-1]
	var out []SummaryPair
	if v, ok := last.Detail["size"]; ok {
		out = append(out, b.summaryPair(last, "size", last.typed("size", v)))
	}
	var first float64
	for _, e := range steps {
		if p, ok := e.Detail["published_at"].(float64); ok && (first == 0 || p < first) {
			first = p
		}
	}
	if first != 0 {
		out = append(out, b.summaryPair(last, "first_published_at", last.typed("published_at", first)))
	}
	return out
}

// journalFamily is the kind of the group an entry of the kind belongs to:
// "fetch", "probe", "feed", or "" for a kind that is never grouped.
func journalFamily(kind string) string {
	switch kind {
	case "leased", "fetch_failed", "fetched":
		return "fetch"
	case "probe_wanted", "probe_leased", "probed":
		return "probe"
	case "feed":
		return "feed"
	}
	return ""
}

// closesChain reports whether the entry, at the level, ends its chain: a
// fetch, a fetch failed for good, or a probe not to be retried.
func closesChain(e JournalEntry, level Severity) bool {
	switch e.Kind {
	case "fetched":
		return true
	case "fetch_failed":
		return level == SeverityFinal
	case "probed":
		return level != SeverityRetry
	}
	return false
}

// failsAttempt reports whether the entry is an attempt that got no data.
func failsAttempt(e JournalEntry) bool {
	return e.Kind == "fetch_failed" || (e.Kind == "probed" && (e.Outcome == "no_peers" || e.Outcome == "timeout"))
}

// isLease reports whether the entry starts an attempt.
func isLease(e JournalEntry) bool { return e.Kind == "leased" || e.Kind == "probe_leased" }

// journalChain is the open chain of a family while groupJournal walks the
// entries.
type journalChain struct {
	group   int
	attempt int64
	open    bool
}

// journalGrouper walks the entries into groups of their indices.
type journalGrouper struct {
	es           []JournalEntry
	levels       []Severity
	groups       [][]int
	fetch, probe journalChain
	// wanted is the probe wants waiting for the next probe lease.
	wanted []int
	// feeds maps a title to its group among the run of feeds walked last.
	feeds map[string]int
}

// groupJournal groups the indices of the entries, oldest first, at their
// levels into rows, ordered by their last entries. A lease of an attempt
// past its open chain's continues the chain up to a fetch, a fetch failed
// for good or a probe not to be retried; any other lease starts a chain.
// A probe want joins the chain of the next probe lease, or else the wants
// left at the end. A feed joins the feeds of its title since the last
// entry of another kind.
func groupJournal(es []JournalEntry, levels []Severity) [][]int {
	g := journalGrouper{es: es, levels: levels}
	for i, e := range es {
		if e.Kind != "feed" {
			g.feeds = nil
		}
		switch journalFamily(e.Kind) {
		case "fetch":
			g.step(&g.fetch, i)
		case "probe":
			g.probeStep(i)
		case "feed":
			g.feed(i)
		default:
			g.add(-1, i)
		}
	}
	if len(g.wanted) > 0 {
		g.groups = append(g.groups, g.wanted)
	}
	slices.SortStableFunc(g.groups, func(a, b []int) int { return a[len(a)-1] - b[len(b)-1] })
	return g.groups
}

// add appends the index to the group, or to a new one for -1, and returns
// the group.
func (g *journalGrouper) add(group, i int) int {
	if group < 0 {
		g.groups = append(g.groups, nil)
		group = len(g.groups) - 1
	}
	g.groups[group] = append(g.groups[group], i)
	return group
}

// step adds the entry at i to the family's chain.
func (g *journalGrouper) step(c *journalChain, i int) {
	e := g.es[i]
	a, _ := e.Detail["attempt"].(float64)
	if !c.open || (isLease(e) && int64(a) <= c.attempt) {
		*c = journalChain{group: g.add(-1, i)}
	} else {
		g.add(c.group, i)
	}
	c.attempt = max(c.attempt, int64(a))
	c.open = !closesChain(e, g.levels[i])
}

// probeStep holds a probe want for the next lease and adds any other probe
// entry to the probe chain, a lease with the wants before it.
func (g *journalGrouper) probeStep(i int) {
	if g.es[i].Kind == "probe_wanted" {
		g.wanted = append(g.wanted, i)
		return
	}
	g.step(&g.probe, i)
	if isLease(g.es[i]) && len(g.wanted) > 0 {
		g.groups[g.probe.group] = append(g.groups[g.probe.group], g.wanted...)
		slices.Sort(g.groups[g.probe.group])
		g.wanted = nil
	}
}

// feed adds the feed entry at i to the feeds of its title in the run.
func (g *journalGrouper) feed(i int) {
	title, _ := g.es[i].Detail["title"].(string)
	if g.feeds == nil {
		g.feeds = map[string]int{}
	}
	if grp, ok := g.feeds[title]; ok {
		g.add(grp, i)
		return
	}
	g.feeds[title] = g.add(-1, i)
}
