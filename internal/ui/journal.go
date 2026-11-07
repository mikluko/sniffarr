package ui

import (
	"log/slog"
)

// Severity is how an entry's outcome left the torrent.
type Severity int

const (
	// SeverityNormal is an outcome that is no failure.
	SeverityNormal Severity = iota
	// SeverityRetry is a failure the process carries on from.
	SeverityRetry
	// SeverityFinal is a failure that ended the process for the torrent.
	SeverityFinal
)

// String is the severity's name: "normal", "retry" or "final".
func (s Severity) String() string {
	switch s {
	case SeverityRetry:
		return "retry"
	case SeverityFinal:
		return "final"
	}
	return "normal"
}

// journalOutcome is the markup of one kind and outcome of entry.
type journalOutcome struct {
	level func(JournalEntry) Severity
	// summary is the keys of the particulars a collapsed row shows, in order.
	summary []string
}

// journalSpec maps every kind and outcome the store journals to its markup.
var journalSpec = map[string]map[string]journalOutcome{
	"sighted": {
		"queued":   {always(SeverityNormal), []string{"discovery"}},
		"promoted": {always(SeverityNormal), []string{"from", "to"}},
		"revived":  {always(SeverityNormal), []string{"discovery"}},
	},
	"leased": {
		"fetch": {always(SeverityNormal), []string{"attempt", "lease_until"}},
	},
	"fetched": {
		"stored":    {always(SeverityNormal), []string{"files", "size", "queued_at"}},
		"rejected":  {always(SeverityFinal), []string{"reason", "size"}},
		"duplicate": {always(SeverityNormal), nil},
	},
	"fetch_failed": {
		"no_peers": {failedFetch(SeverityFinal, "dead_until", "retry_at"), []string{"dead_until", "leases"}},
		"timeout":  {failedFetch(SeverityRetry, "retry_at", "dead_until"), []string{"attempt", "retry_at", "leases"}},
		"dropped":  {failedFetch(SeverityFinal, "dead_until", "retry_at"), []string{"attempt", "dead_until", "leases"}},
	},
	"evicted": {
		"aged":     {always(SeverityFinal), []string{"queued_at", "dead_until"}},
		"oversize": {always(SeverityFinal), []string{"queued_at", "dead_until"}},
	},
	"classified": {
		"jev":    {always(SeverityNormal), []string{"audio", "source", "source_confidence"}},
		"reuse":  {always(SeverityNormal), []string{"kind", "from"}},
		"failed": {always(SeverityFinal), []string{"error"}},
	},
	"matched": {
		"tmdb":    {always(SeverityNormal), []string{"tmdb_id", "type", "confidence"}},
		"jev":     {always(SeverityNormal), []string{"tmdb_id", "type", "confidence"}},
		"arr":     {always(SeverityNormal), []string{"tmdb_id", "type", "trigger"}},
		"earlier": {always(SeverityNormal), []string{"tmdb_id", "confidence", "layer"}},
		"none":    {always(SeverityRetry), []string{"trigger"}},
		"error":   {always(SeverityRetry), []string{"error", "trigger"}},
	},
	"arr": {
		"grab":   {always(SeverityNormal), []string{"app", "indexer", "title"}},
		"import": {always(SeverityNormal), []string{"app", "indexer", "title"}},
	},
	"feed": {
		"stored":         {always(SeverityNormal), []string{"source", "size", "published_at"}},
		"queued":         {always(SeverityNormal), []string{"source", "size", "published_at"}},
		"already_queued": {always(SeverityNormal), []string{"source", "size", "published_at"}},
		"rejected":       {always(SeverityFinal), []string{"source", "size", "published_at"}},
	},
	"probe_wanted": {
		"search":  {always(SeverityNormal), []string{"id", "query", "t"}},
		"request": {always(SeverityNormal), nil},
	},
	"probe_leased": {
		"request":    {always(SeverityNormal), []string{"attempt", "lease_until", "max_bytes"}},
		"retry":      {always(SeverityNormal), []string{"attempt", "lease_until", "max_bytes"}},
		"search":     {always(SeverityNormal), []string{"attempt", "lease_until", "max_bytes"}},
		"background": {always(SeverityNormal), []string{"attempt", "lease_until", "max_bytes"}},
	},
	"probed": {
		"tracks":   {always(SeverityNormal), []string{"audio", "tracks", "downloaded"}},
		"unknown":  {failedProbe(false), []string{"downloaded"}},
		"no_peers": {failedProbe(true), []string{"attempt", "retry_at", "peers_week"}},
		"timeout":  {failedProbe(true), []string{"attempt", "retry_at", "peers_week"}},
	},
	"deleted": {
		"idle":    {always(SeverityFinal), []string{"last_seen"}},
		"expired": {always(SeverityFinal), []string{"last_seen"}},
	},
}

// always is the level s whatever the entry's particulars.
func always(s Severity) func(JournalEntry) Severity {
	return func(JournalEntry) Severity { return s }
}

// failedFetch is the level of a failed fetch, s by its outcome alone; an
// entry without the key the outcome writes, or with the one it does not, is
// logged as a data error.
func failedFetch(s Severity, want, not string) func(JournalEntry) Severity {
	return func(e JournalEntry) Severity {
		_, has := e.Detail[want]
		_, wrong := e.Detail[not]
		if !has || wrong {
			dataError(e, "the detail disagrees with the outcome")
		}
		return s
	}
}

// failedProbe is the level of a failed probe by its final flag: a final
// failure when it is true or absent, a retry when it is false. An absent
// flag is logged as a data error where the outcome writes one.
func failedProbe(writesFinal bool) func(JournalEntry) Severity {
	return func(e JournalEntry) Severity {
		final, ok := e.Detail["final"].(bool)
		if !ok && writesFinal {
			dataError(e, "the detail has no final flag")
		}
		if ok && !final {
			return SeverityRetry
		}
		return SeverityFinal
	}
}

// dataError logs an entry the store should not have written.
func dataError(e JournalEntry, msg string) {
	slog.Error("journal entry: "+msg, "kind", e.Kind, "outcome", e.Outcome, "at", e.At, "detail", e.Detail)
}

// Level is the entry's severity by journalSpec; an entry of a kind and
// outcome it does not list is logged as a data error and is normal.
func (e JournalEntry) Level() Severity {
	o, ok := journalSpec[e.Kind][e.Outcome]
	if !ok {
		dataError(e, "the kind and outcome are unknown")
		return SeverityNormal
	}
	return o.level(e)
}

// JournalSummary returns the particulars of the entry a collapsed row shows,
// those of journalSpec's summary keys it has, in their order, written as
// JournalDetail writes them.
func (b Base) JournalSummary(e JournalEntry) []DetailPair {
	var out []DetailPair
	for _, k := range journalSpec[e.Kind][e.Outcome].summary {
		if v, ok := e.Detail[k]; ok {
			out = append(out, b.journalPair(e, k, v))
		}
	}
	return out
}
