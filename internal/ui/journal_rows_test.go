package ui

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden files of the journal's rows")

func TestJournalGroups(t *testing.T) {
	defer slog.SetDefault(slog.Default())
	slog.SetDefault(slog.New(slog.DiscardHandler))
	at := time.Date(2026, 10, 18, 21, 0, 0, 0, time.UTC)
	e := func(min int, kind, outcome string, detail map[string]any) JournalEntry {
		return JournalEntry{At: at.Add(time.Duration(min) * time.Minute), Kind: kind, Outcome: outcome, Detail: detail}
	}
	attempt := func(n float64) map[string]any { return map[string]any{"attempt": n} }
	final := func(f bool) map[string]any { return map[string]any{"final": f} }
	for _, c := range []struct {
		name string
		es   []JournalEntry
		want [][]int
	}{
		{"a fetch retried", []JournalEntry{
			e(0, "sighted", "queued", nil),
			e(1, "leased", "fetch", attempt(1)),
			e(2, "fetch_failed", "timeout", map[string]any{"attempt": 1.0, "retry_at": 1.0}),
			e(3, "leased", "fetch", attempt(2)),
			e(4, "fetched", "stored", nil),
		}, [][]int{{0}, {1, 2, 3, 4}}},
		{"a fetch given up", []JournalEntry{
			e(1, "leased", "fetch", attempt(1)),
			e(2, "fetch_failed", "no_peers", map[string]any{"dead_until": 1.0}),
			e(3, "sighted", "revived", nil),
			e(4, "leased", "fetch", attempt(1)),
		}, [][]int{{0, 1}, {2}, {3}}},
		{"an attempt leased anew", []JournalEntry{
			e(1, "leased", "fetch", attempt(1)),
			e(2, "leased", "fetch", attempt(1)),
		}, [][]int{{0}, {1}}},
		{"other kinds inside a chain", []JournalEntry{
			e(1, "leased", "fetch", attempt(1)),
			e(2, "sighted", "promoted", nil),
			e(3, "fetched", "stored", nil),
		}, [][]int{{1}, {0, 2}}},
		{"a chain cut by retention", []JournalEntry{
			e(1, "fetch_failed", "timeout", map[string]any{"attempt": 3.0, "retry_at": 1.0}),
			e(2, "leased", "fetch", attempt(4)),
			e(3, "fetched", "stored", nil),
		}, [][]int{{0, 1, 2}}},
		{"a probe", []JournalEntry{
			e(0, "probe_wanted", "search", nil),
			e(1, "matched", "tmdb", nil),
			e(2, "probe_leased", "search", attempt(1)),
			e(3, "probed", "tracks", nil),
		}, [][]int{{1}, {0, 2, 3}}},
		{"a probe retried", []JournalEntry{
			e(0, "probe_leased", "background", attempt(1)),
			e(1, "probed", "timeout", final(false)),
			e(2, "probe_wanted", "request", nil),
			e(3, "probe_leased", "retry", attempt(2)),
			e(4, "probed", "tracks", nil),
		}, [][]int{{0, 1, 2, 3, 4}}},
		{"a want of the next probe", []JournalEntry{
			e(0, "probe_leased", "background", attempt(1)),
			e(1, "probe_wanted", "request", nil),
			e(2, "probed", "timeout", final(true)),
			e(3, "probe_leased", "request", attempt(1)),
		}, [][]int{{0, 2}, {1, 3}}},
		{"wants never leased", []JournalEntry{
			e(0, "probe_wanted", "search", nil),
			e(1, "probe_wanted", "request", nil),
		}, [][]int{{0, 1}}},
		{"an unknown kind", []JournalEntry{e(0, "novel", "x", nil), e(1, "novel", "x", nil)}, [][]int{{0}, {1}}},
		{"nothing", nil, nil},
	} {
		levels := make([]Severity, len(c.es))
		for i, x := range c.es {
			levels[i] = x.Level()
		}
		if got := groupJournal(c.es, levels); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestJournalRowGap(t *testing.T) {
	b := Base{}
	at := time.Date(2026, 10, 18, 21, 0, 0, 0, time.UTC)
	rows := b.JournalRows([]JournalEntry{
		{At: at, Kind: "sighted", Outcome: "queued"},
		{At: at.Add(29 * time.Minute), Kind: "sighted", Outcome: "promoted"},
		{At: at.Add(59 * time.Minute), Kind: "sighted", Outcome: "promoted"},
	})
	var got []time.Duration
	for _, r := range rows {
		got = append(got, r.Gap)
	}
	if want := []time.Duration{30 * time.Minute, 0, 0}; !reflect.DeepEqual(got, want) {
		t.Errorf("gaps %v, want %v", got, want)
	}
}

func TestJournalOrdinal(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	en := r.Base(httptest.NewRequest("GET", "/t/x", nil), "en", "", nil, time.UTC)
	ru := r.Base(httptest.NewRequest("GET", "/t/x", nil), "ru", "", nil, time.UTC)
	for _, c := range []struct {
		b    Base
		n    int64
		want string
	}{
		{en, 1, "stored on the 1st attempt"}, {en, 2, "stored on the 2nd attempt"}, {en, 3, "stored on the 3rd attempt"},
		{en, 4, "stored on the 4th attempt"}, {en, 11, "stored on the 11th attempt"}, {en, 12, "stored on the 12th attempt"},
		{en, 13, "stored on the 13th attempt"}, {en, 21, "stored on the 21st attempt"}, {en, 102, "stored on the 102nd attempt"},
		{ru, 2, "сохранены со 2-й попытки"}, {ru, 3, "сохранены с 3-й попытки"}, {ru, 12, "сохранены с 12-й попытки"},
	} {
		if got := c.b.onAttempt(c.b.T("journal.outcome.fetched.stored"), c.n); got != c.want {
			t.Errorf("%s %d: %q, want %q", c.b.Lang, c.n, got, c.want)
		}
	}
}

// The rows of the journals the designer names match their golden files in
// testdata/journal; go test -run TestJournalRowsGolden -update rewrites them.
func TestJournalRowsGolden(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	onMissing = func(lang, key string) { missing = append(missing, lang+":"+key) }
	defer func() { onMissing = nil }()

	loc := time.FixedZone("CEST", 2*3600)
	now := time.Date(2026, 10, 20, 14, 5, 0, 0, loc)
	card := sampleJournal(now.Add(-41*time.Hour), "Игра на понижение.2015.WEB-DL.1080р.Open Matte.mkv")
	for _, c := range []struct {
		name string
		es   []JournalEntry
	}{
		{"card", card},
		{"retry", card[1:5]},
		{"probe", card[9:12]},
		{"journal-only", SampleTorrent(Base{Now: now}, false, "journal").Torrent.Journal},
		{"tracks-32", journalTracks32(now)},
		{"entries-200", journal200(now)},
	} {
		var out strings.Builder
		for _, lang := range []string{"en", "ru"} {
			b := r.Base(httptest.NewRequest("GET", "/t/x", nil), lang, "", []string{"ru"}, loc)
			b.Now = now
			fmt.Fprintf(&out, "== %s\n", lang)
			writeJournalRows(&out, b, b.JournalRows(c.es))
		}
		path := filepath.Join("testdata", "journal", c.name+".golden")
		if *update {
			if err := os.WriteFile(path, []byte(out.String()), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if out.String() != string(want) {
			t.Errorf("%s: rows differ from %s:\n%s", c.name, path, out.String())
		}
	}
	if len(missing) > 0 {
		t.Errorf("untranslated keys: %v", missing)
	}
}

// writeJournalRows writes the rows as text, a row to a line and its steps,
// details and tracks indented under it.
func writeJournalRows(w *strings.Builder, b Base, rows []JournalRow) {
	pairs := func(ps []DetailPair) string {
		s := make([]string, len(ps))
		for i, p := range ps {
			s[i] = p.Key + ": " + p.Value
			if p.Href != "" {
				s[i] += " <" + p.Href + ">"
			}
		}
		return strings.Join(s, " · ")
	}
	summary := func(ps []SummaryPair) string {
		s := make([]string, len(ps))
		for i, p := range ps {
			s[i] = p.Text()
			if p.Href != "" {
				s[i] += " <" + p.Href + ">"
			}
		}
		return strings.Join(s, " · ")
	}
	for _, r := range rows {
		fmt.Fprintf(w, "%s +%v %s [%s/%s] %s | %s\n", b.TimeFull(r.At), r.Gap, r.Kind, r.Severity, r.End, r.Outcome, summary(r.Summary))
		for _, s := range r.Steps {
			fmt.Fprintf(w, "  step %s %s/%s [%s] | %s | %s\n", b.TimeFull(s.At), s.Kind, s.Outcome, s.Severity, summary(s.Summary), pairs(s.Details))
		}
		fmt.Fprintf(w, "  details | %s\n", pairs(r.Details))
		for _, tr := range r.Tracks {
			fmt.Fprintf(w, "  track %s|%s|%s\n", tr.Type, tr.Lang, tr.Codec)
		}
	}
}

// journalTracks32 is a probe that read 40 tracks, the first 32 of them
// listed as the store journals them.
func journalTracks32(now time.Time) []JournalEntry {
	at := now.Add(-time.Hour)
	tracks := []any{"video::V_MPEGH/ISO/HEVC"}
	langs := []string{"rus", "eng", "ukr", "kaz", "ger", "spa", "fre", "ita"}
	for i := range 16 {
		tracks = append(tracks, "audio:"+langs[i%len(langs)]+":A_EAC3")
	}
	for i := range 15 {
		tracks = append(tracks, "subtitle:"+langs[i%len(langs)]+":S_TEXT/UTF8")
	}
	return []JournalEntry{
		{At: at, Kind: "probe_wanted", Outcome: "request"},
		{At: at.Add(time.Minute), Kind: "probe_leased", Outcome: "request", Detail: map[string]any{"attempt": 1.0, "max_bytes": 67108864.0, "lease_until": float64(at.Add(11 * time.Minute).Unix())}},
		{At: at.Add(3 * time.Minute), Kind: "probed", Outcome: "tracks", Detail: map[string]any{"path": "Show.S01.mkv", "format": "matroska",
			"downloaded": 8388608.0, "tracks": tracks, "tracks_total": 40.0, "audio": map[string]any{"ru": 0.97}}},
	}
}

// journal200 is a torrent's journal at the store's 200 entries: a fetch
// retried, then failed matches and probes retried in turn.
func journal200(now time.Time) []JournalEntry {
	at := now.Add(-20 * 24 * time.Hour)
	sec := func(t time.Time) float64 { return float64(t.Unix()) }
	es := []JournalEntry{
		{At: at, Kind: "sighted", Outcome: "queued", Detail: map[string]any{"discovery": "announce"}},
		{At: at.Add(time.Minute), Kind: "leased", Outcome: "fetch", Detail: map[string]any{"attempt": 1.0, "lease_until": sec(at.Add(2 * time.Minute))}},
		{At: at.Add(3 * time.Minute), Kind: "fetch_failed", Outcome: "timeout", Detail: map[string]any{"attempt": 1.0, "retry_at": sec(at.Add(5 * time.Minute)), "leases": 1.0}},
		{At: at.Add(5 * time.Minute), Kind: "leased", Outcome: "fetch", Detail: map[string]any{"attempt": 2.0, "lease_until": sec(at.Add(6 * time.Minute))}},
		{At: at.Add(6 * time.Minute), Kind: "fetched", Outcome: "stored", Detail: map[string]any{"name": "x", "size": 1073741824.0, "files": 1.0, "leases": 2.0}},
	}
	for i := 0; len(es) < 200; i++ {
		t := at.Add(time.Duration(i+1) * 2 * time.Hour)
		probed := map[string]any{"attempt": float64(i%3 + 1), "final": i%3 == 2}
		if i%3 != 2 {
			probed["retry_at"] = sec(t.Add(time.Hour))
		}
		es = append(es,
			JournalEntry{At: t.Add(2 * time.Minute), Kind: "matched", Outcome: "none", Detail: map[string]any{"trigger": "worker"}},
			JournalEntry{At: t.Add(40 * time.Minute), Kind: "probe_leased", Outcome: "background", Detail: map[string]any{"attempt": float64(i%3 + 1)}},
			JournalEntry{At: t.Add(45 * time.Minute), Kind: "probed", Outcome: "timeout", Detail: probed},
		)
	}
	return es[:200]
}
