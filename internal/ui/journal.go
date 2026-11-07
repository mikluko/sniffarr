package ui

import (
	"log/slog"
	"maps"
	"math"
	"net/url"
	"slices"
	"strings"
	"time"
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
			out = append(out, b.journalPair(e, JournalField{k, e.typed(k, v)}))
		}
	}
	return out
}

// Bytes is a particular the store writes as a number of bytes.
type Bytes int64

// Times is how many times an attempt was leased.
type Times int64

// ID is a particular that identifies a record elsewhere, written ungrouped.
type ID int64

// Infohash is the hex form of an infohash.
type Infohash string

// Link is a page and the label it is written by.
type Link struct {
	URL, Label string
}

// Track is a track a probe read, as the store journals it:
// "kind:language:codec".
type Track struct {
	Type, Lang, Codec string
}

// JournalField is one particular of an entry, its value typed by Fields.
type JournalField struct {
	Key   string
	Value any
}

// idKeys are the particulars whose numbers are IDs.
var idKeys = map[string]bool{"tmdb_id": true, "previous": true}

// Fields returns the entry's particulars in the order of their keys, each
// value typed by its key and form: a moment as time.Time, bytes as Bytes, the
// leases of an attempt as Times, an identifier as ID, another whole number as
// int64; the hex form of an infohash as Infohash, a feed item's http or https
// page as a Link labelled by the entry's source; a track that parses as Track.
// Lists and objects keep their shape, their elements typed; the audio
// probabilities stay float64.
func (e JournalEntry) Fields() []JournalField {
	out := make([]JournalField, 0, len(e.Detail))
	for _, k := range slices.Sorted(maps.Keys(e.Detail)) {
		out = append(out, JournalField{k, e.typed(k, e.Detail[k])})
	}
	return out
}

// Tracks returns the tracks of the entry's particulars that parse.
func (e JournalEntry) Tracks() []Track {
	list, _ := e.Detail["tracks"].([]any)
	var out []Track
	for _, v := range list {
		if s, ok := v.(string); ok {
			if t, ok := parseTrack(s); ok {
				out = append(out, t)
			}
		}
	}
	return out
}

// typed is the decoded JSON value of the key typed as Fields does.
func (e JournalEntry) typed(key string, v any) any {
	switch x := v.(type) {
	case float64:
		return typedNumber(key, x)
	case string:
		return e.typedString(key, x)
	case []any:
		out := make([]any, len(x))
		for i, el := range x {
			out[i] = e.typed(key, el)
			if s, ok := el.(string); ok && key == "tracks" {
				if t, ok := parseTrack(s); ok {
					out[i] = t
				}
			}
		}
		return out
	case map[string]any:
		if key == "audio" {
			return x
		}
		out := make(map[string]any, len(x))
		for k, el := range x {
			out[k] = e.typed(k, el)
		}
		return out
	}
	return v
}

// typedNumber is a number of the key typed as Fields does.
func typedNumber(key string, x float64) any {
	whole := x == math.Trunc(x) && math.Abs(x) < 1<<53
	switch {
	case detailTime(key):
		return time.Unix(int64(x), 0)
	case detailSize(key):
		return Bytes(x)
	case !whole:
		return x
	case key == "leases":
		return Times(x)
	case idKeys[key]:
		return ID(x)
	}
	return int64(x)
}

// typedString is a string of the key typed as Fields does.
func (e JournalEntry) typedString(key, s string) any {
	if isInfohash(s) {
		return Infohash(strings.ToLower(s))
	}
	if u, err := url.Parse(s); key == "url" && err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		label, _ := e.Detail["source"].(string)
		if label == "" {
			label = s
		}
		return Link{URL: s, Label: label}
	}
	return s
}

// parseTrack parses a track the store journals as "kind:language:codec"; a
// codec may hold colons of its own.
func parseTrack(s string) (Track, bool) {
	kind, rest, _ := strings.Cut(s, ":")
	lang, codec, ok := strings.Cut(rest, ":")
	return Track{kind, lang, codec}, ok
}
