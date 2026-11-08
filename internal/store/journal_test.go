package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"sarak.sh/mikluko/libarr/database"
	"sarak.sh/mikluko/sniffarr/internal/rules"
)

// hexOf is the hash as a journal detail writes it.
func hexOf(h Hash) string {
	return hex.EncodeToString(h[:])
}

// entries returns the journal of the hash as kind/outcome, oldest first.
func entries(t *testing.T, s *Store, h Hash) []string {
	t.Helper()
	got, err := s.Journal(context.Background(), h, 1000)
	require.NoError(t, err)
	out := make([]string, len(got))
	for i, e := range got {
		out[i] = e.Kind + "/" + e.Outcome
	}
	return out
}

// entry returns the one entry of the hash of the kind and outcome, its ID
// left out.
func entry(t *testing.T, s *Store, h Hash, kind, outcome string) JournalEntry {
	t.Helper()
	got, err := s.Journal(context.Background(), h, 1000)
	require.NoError(t, err)
	var found []JournalEntry
	for _, e := range got {
		if e.Kind == kind && e.Outcome == outcome {
			e.ID = 0
			found = append(found, e)
		}
	}
	require.Len(t, found, 1, "%s/%s of %x", kind, outcome, h[:1])
	return found[0]
}

func TestJournalOrderAndLimit(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	require.NoError(t, s.tx(ctx, func(tx *sql.Tx) error {
		for i, o := range []string{"queued", "promoted", "revived"} {
			if err := journal(ctx, tx, hash(1), t0.Add(time.Duration(i)*time.Second), JournalSighted, o, Detail("discovery", Announce, "priority", 0, "none", "", "at", time.Time{})); err != nil {
				return err
			}
		}
		if err := journal(ctx, tx, hash(1), t0.Add(-time.Hour), JournalArr, EventGrab, nil); err != nil {
			return err
		}
		return journal(ctx, tx, hash(2), t0, JournalFetched, "duplicate", nil)
	}))
	got, err := s.Journal(ctx, hash(1), 10)
	require.NoError(t, err)
	require.Equal(t, []JournalEntry{
		{ID: 4, Hash: hash(1), At: t0.Add(-time.Hour), Kind: JournalArr, Outcome: EventGrab},
		{ID: 1, Hash: hash(1), At: t0, Kind: JournalSighted, Outcome: "queued", Detail: `{"discovery":"announce"}`},
		{ID: 2, Hash: hash(1), At: t0.Add(time.Second), Kind: JournalSighted, Outcome: "promoted", Detail: `{"discovery":"announce"}`},
		{ID: 3, Hash: hash(1), At: t0.Add(2 * time.Second), Kind: JournalSighted, Outcome: "revived", Detail: `{"discovery":"announce"}`},
	}, got, "in time order, an entry written later of an earlier time first; zero values left out of the detail")
	got, err = s.Journal(ctx, hash(1), 2)
	require.NoError(t, err)
	require.Equal(t, []string{"promoted", "revived"}, []string{got[0].Outcome, got[1].Outcome}, "the newest ones within the limit")
	got, err = s.Journal(ctx, hash(2), 10)
	require.NoError(t, err)
	require.Equal(t, []JournalEntry{{ID: 5, Hash: hash(2), At: t0, Kind: JournalFetched, Outcome: "duplicate"}}, got, "no detail reads as empty")
	got, err = s.Journal(ctx, hash(3), 10)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestJournalPage(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	require.NoError(t, s.tx(ctx, func(tx *sql.Tx) error {
		for i, at := range []time.Duration{0, time.Second, -time.Hour, 2 * time.Second, 2 * time.Second} {
			if err := journal(ctx, tx, hash(1), t0.Add(at), JournalSighted, "queued", Detail("n", i+1)); err != nil {
				return err
			}
		}
		return journal(ctx, tx, hash(2), t0, JournalFetched, "duplicate", nil)
	}))
	ids := func(p JournalPage) []int64 {
		out := make([]int64, len(p.Entries))
		for i, e := range p.Entries {
			out[i] = e.ID
		}
		return out
	}
	h1, h3 := hash(1), hash(3)
	p, err := s.JournalPage(ctx, JournalFilter{Hash: &h1}, 0, 2)
	require.NoError(t, err)
	require.Equal(t, []int64{4, 5}, ids(p), "the newest, ordered by time, then by writing")
	require.Equal(t, JournalPage{Entries: p.Entries, Total: 5, Older: 3}, p)
	p, err = s.JournalPage(ctx, JournalFilter{Hash: &h1}, 4, 2)
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2}, ids(p), "those before the entry")
	require.Equal(t, 5, p.Total)
	require.Equal(t, 1, p.Older)
	p, err = s.JournalPage(ctx, JournalFilter{Hash: &h1}, 1, 2)
	require.NoError(t, err)
	require.Equal(t, []int64{3}, ids(p), "an entry written later of an earlier time comes before")
	require.Zero(t, p.Older)
	p, err = s.JournalPage(ctx, JournalFilter{Hash: &h1}, 6, 2)
	require.NoError(t, err)
	require.Empty(t, p.Entries, "before an entry of another hash, none")
	require.Equal(t, 5, p.Total)
	p, err = s.JournalPage(ctx, JournalFilter{Hash: &h3}, 0, 2)
	require.NoError(t, err)
	require.Equal(t, JournalPage{}, p)
}

func TestJournalFilter(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	type ev struct {
		h             byte
		at            time.Duration
		kind, outcome string
	}
	evs := []ev{
		{1, 0, JournalProbed, "tracks"},
		{2, time.Second, JournalProbed, "no_peers"},
		{1, 2 * time.Second, JournalFetched, "stored"},
		{2, -time.Hour, JournalProbed, "tracks"},
		{3, 3 * time.Second, JournalProbed, "timeout"},
	}
	require.NoError(t, s.tx(ctx, func(tx *sql.Tx) error {
		for _, e := range evs {
			if err := journal(ctx, tx, hash(e.h), t0.Add(e.at), e.kind, e.outcome, nil); err != nil {
				return err
			}
		}
		return nil
	}))
	ids := func(f JournalFilter, before int64, limit int) []int64 {
		t.Helper()
		p, err := s.JournalPage(ctx, f, before, limit)
		require.NoError(t, err)
		out := make([]int64, len(p.Entries))
		for i, e := range p.Entries {
			out[i] = e.ID
		}
		return out
	}
	require.Equal(t, []int64{4, 1, 2, 3, 5}, ids(JournalFilter{}, 0, 10), "every hash's, in time order")
	require.Equal(t, []int64{3, 5}, ids(JournalFilter{}, 0, 2), "the newest")
	require.Equal(t, []int64{1, 2}, ids(JournalFilter{}, 3, 2), "before an entry of any hash")
	require.Equal(t, []int64{4, 1, 2, 5}, ids(JournalFilter{Kind: JournalProbed}, 0, 10), "of a kind, its outcomes merged in time order")
	require.Equal(t, []int64{2, 5}, ids(JournalFilter{Kind: JournalProbed}, 0, 2))
	require.Equal(t, []int64{4, 1}, ids(JournalFilter{Kind: JournalProbed}, 2, 10))
	require.Equal(t, []int64{4, 1}, ids(JournalFilter{Kind: JournalProbed, Outcome: "tracks"}, 0, 10))
	require.Empty(t, ids(JournalFilter{Kind: JournalProbed}, 99, 10), "before an ID not in the journal, none")

	h2 := hash(2)
	p, err := s.JournalPage(ctx, JournalFilter{Hash: &h2, Kind: JournalProbed, Outcome: "tracks"}, 0, 10)
	require.NoError(t, err)
	require.Len(t, p.Entries, 1)
	require.Equal(t, h2, p.Entries[0].Hash)
	require.Equal(t, 1, p.Total, "counted within the kind and outcome")
	p, err = s.JournalPage(ctx, JournalFilter{Hash: &h2}, 0, 1)
	require.NoError(t, err)
	require.Equal(t, []int{2, 1}, []int{p.Total, p.Older})
	p, err = s.JournalPage(ctx, JournalFilter{Kind: JournalProbed}, 0, 1)
	require.NoError(t, err)
	require.Equal(t, hash(3), p.Entries[0].Hash, "each entry with its hash")
	require.Equal(t, []int{0, 0}, []int{p.Total, p.Older}, "not counted without a hash")
}

func TestJournalDetailBounded(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	// reject rejects the hash of the byte with the name and returns its
	// fetched/rejected detail.
	reject := func(b byte, name string) string {
		t.Helper()
		ok, err := s.Reject(ctx, hash(b), rules.Script, name, 1<<20, t0)
		require.NoError(t, err)
		require.True(t, ok)
		return entry(t, s, hash(b), JournalFetched, "rejected").Detail
	}
	nameOf := func(detail string) string {
		t.Helper()
		var d struct{ Name string }
		require.NoError(t, json.Unmarshal([]byte(detail), &d))
		return d.Name
	}

	controls := reject(1, strings.Repeat("\x01", 1<<20))
	require.Less(t, len(controls), 8<<10, "a 1 MiB name of control characters")
	require.Equal(t, strings.Repeat("\x01", 1<<10)+"…", nameOf(controls))
	require.Equal(t, "x"+strings.Repeat("ж", 511)+"…", nameOf(reject(2, "x"+strings.Repeat("ж", 1000))), "cut at a rune boundary")
	short := reject(3, "Tom & Jerry <1940>")
	require.Contains(t, short, `"name":"Tom & Jerry <1940>"`, "a short name unchanged, not escaped for HTML")

	long, cut := strings.Repeat("x", 1<<20), strings.Repeat("x", 1<<10)+"…"
	_, _, err := s.Insert(ctx, titled(4, "Heat.1995.1080p", "y:heat 1995"))
	require.NoError(t, err)
	_, _, err = s.RecordMatch(ctx, "y:heat 1995", Found{Error: long}, t0)
	require.NoError(t, err)
	require.JSONEq(t, `{"error":"`+cut+`"}`, entry(t, s, hash(4), JournalMatched, "error").Detail, "a string set after Detail")

	require.NoError(t, s.tx(ctx, func(tx *sql.Tx) error {
		return journal(ctx, tx, hash(5), t0, JournalProbed, ProbeTracks, map[string]any{"tracks": []string{long, "audio"}, "list": []any{long, 1.0},
			"nested": map[string]any{"deep": []any{map[string]any{"s": long}}}, long: 1})
	}))
	var d map[string]any
	require.NoError(t, json.Unmarshal([]byte(entry(t, s, hash(5), JournalProbed, ProbeTracks).Detail), &d))
	require.Equal(t, map[string]any{"tracks": []any{cut, "audio"}, "list": []any{cut, 1.0}, "nested": map[string]any{"deep": []any{map[string]any{"s": cut}}}, cut: 1.0}, d,
		"every string, at any depth, keys among them")
}

func TestJournalDetailBoundedAnyType(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	long, cut := strings.Repeat("x", 1<<20), strings.Repeat("x", 1<<10)+"…"
	require.NoError(t, s.tx(ctx, func(tx *sql.Tx) error {
		return journal(ctx, tx, hash(1), t0, JournalProbed, ProbeTracks, map[string]any{
			"maps":   []map[string]any{{"s": long}},
			"strs":   map[string]string{long: long, "k": "v"},
			"matrix": [][]string{{long, "a"}, {"b"}},
		})
	}))
	var d map[string]any
	require.NoError(t, json.Unmarshal([]byte(entry(t, s, hash(1), JournalProbed, ProbeTracks).Detail), &d))
	require.Equal(t, map[string]any{
		"maps":   []any{map[string]any{"s": cut}},
		"strs":   map[string]any{cut: cut, "k": "v"},
		"matrix": []any{[]any{cut, "a"}, []any{"b"}},
	}, d, "a type the cut has no case for is cut all the same")
}

func TestJournalDetailKeepsNumbers(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	require.NoError(t, s.tx(ctx, func(tx *sql.Tx) error {
		return journal(ctx, tx, hash(1), t0, JournalProbed, ProbeTracks, Detail("big", int64(1<<62), "ratio", 0.25, "at", t0))
	}))
	require.JSONEq(t, `{"big":4611686018427387904,"ratio":0.25,"at":`+unix(t0)+`}`, entry(t, s, hash(1), JournalProbed, ProbeTracks).Detail, "numbers as written, none through float64")
}

func TestJournalDetailNonFinite(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	require.NoError(t, s.tx(ctx, func(tx *sql.Tx) error {
		return journal(ctx, tx, hash(1), t0, JournalClassified, LayerClassifier, Detail("confidence", math.NaN(), "ratio", math.Inf(1),
			"audio", map[string]float64{"ru": math.NaN(), "en": 0.5}, "kind", "movie"))
	}), "a value JSON cannot hold fails neither the row nor the change it rides with")
	require.JSONEq(t, `{"audio":{"en":0.5},"kind":"movie"}`, entry(t, s, hash(1), JournalClassified, LayerClassifier).Detail)
}

func TestJournalProbedTracksBounded(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	h := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	require.NoError(t, s.ProbeDone(ctx, h, Probe{Outcome: ProbeTracks, Tracks: []Track{
		{Kind: "audio", Language: strings.Repeat("\x01", 1<<20), Codec: "aac"},
		{Kind: "audio", Language: "rus", Codec: "x" + strings.Repeat("ж", 1000)},
		{Kind: "video", Codec: "h264"},
	}}, t0))
	detail := entry(t, s, h, JournalProbed, ProbeTracks).Detail
	require.Less(t, len(detail), 16<<10, "a 1 MiB language of control characters")
	var d struct{ Tracks []string }
	require.NoError(t, json.Unmarshal([]byte(detail), &d))
	require.Equal(t, []string{
		"audio:" + strings.Repeat("\x01", 1<<10-len("audio:")) + "…",
		"audio:rus:x" + strings.Repeat("ж", (1<<10-len("audio:rus:x"))/2) + "…",
		"video::h264",
	}, d.Tracks, "each track cut at 1 KiB on a rune boundary")
	require.NotContains(t, detail, "tracks_total", "every track kept")

	many := make([]Track, journalTracks+8)
	for i := range many {
		many[i] = Track{Kind: "subtitle", Language: "eng", Codec: "S_TEXT/UTF8"}
	}
	require.NoError(t, s.ProbeDone(ctx, h, Probe{Outcome: ProbeTracks, Tracks: many}, t0.Add(time.Hour)))
	got, err := s.Journal(ctx, h, 1)
	require.NoError(t, err)
	var m struct {
		Tracks      []string
		TracksTotal int `json:"tracks_total"`
	}
	require.NoError(t, json.Unmarshal([]byte(got[0].Detail), &m))
	require.Len(t, m.Tracks, journalTracks, "the first ones")
	require.Equal(t, journalTracks+8, m.TracksTotal, "with how many there were")
}

// counters returns what the reader's counters counted, by name, unit and
// attributes.
func counters(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	got := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, p := range sum.DataPoints {
				got[strings.TrimSpace(m.Name+" "+m.Unit+" "+p.Attributes.Encoded(attribute.DefaultEncoder()))] += p.Value
			}
		}
	}
	return got
}

func TestSweepJournal(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{Service: "sniffarr"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	reader := sdkmetric.NewManualReader()
	s, err := New(ctx, db, Options{Meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))})
	require.NoError(t, err)
	require.NoError(t, s.tx(ctx, func(tx *sql.Tx) error {
		for i := range 3 {
			if err := journal(ctx, tx, hash(byte(i+1)), t0, JournalFetched, "stored", nil); err != nil {
				return err
			}
		}
		return journal(ctx, tx, hash(1), t0.Add(48*time.Hour), JournalDeleted, "idle", nil)
	}))
	n, err := s.SweepJournal(ctx, t0.Add(24*time.Hour), 2)
	require.NoError(t, err)
	require.Equal(t, 3, n, "the rows at t0, over two batches")
	require.Equal(t, []string{"deleted/idle"}, entries(t, s, hash(1)))
	require.Empty(t, entries(t, s, hash(2)))
	require.Equal(t, map[string]int64{"sniffarr.store.journal.deletions {entry}": 3}, counters(t, reader), "counted apart from the torrents swept")
}

// planOf returns the lines of the query plan of query.
func planOf(t *testing.T, s *Store, query string, args ...any) []string {
	t.Helper()
	var plan []string
	require.NoError(t, s.each(context.Background(), func(rows *sql.Rows) error {
		var id, parent, unused int
		var d string
		if err := rows.Scan(&id, &parent, &unused, &d); err != nil {
			return err
		}
		plan = append(plan, d)
		return nil
	}, "EXPLAIN QUERY PLAN "+query, args...))
	return plan
}

func TestJournalPlans(t *testing.T) {
	s := open(t)
	h := hash(1)
	byHash, byHashArgs := journalSelect(JournalFilter{Hash: &h}, 2, 1)
	byOutcome, byOutcomeArgs := journalSelect(JournalFilter{Kind: JournalProbed, Outcome: "tracks"}, 2, 1)
	all, allArgs := journalSelect(JournalFilter{}, 2, 1)
	for _, c := range []struct {
		query string
		args  []any
		want  []string
	}{
		{journalSweep, []any{0, 1}, []string{"INDEX torrent_journal_at (at<?)"}},
		{byHash, byHashArgs, []string{"INDEX torrent_journal_infohash (infohash=? AND at<?)", "SCALAR SUBQUERY", "INTEGER PRIMARY KEY (rowid=?)", "REUSE SUBQUERY"}},
		{byOutcome, byOutcomeArgs, []string{"INDEX torrent_journal_kind (kind=? AND outcome=? AND at<?)", "SCALAR SUBQUERY", "INTEGER PRIMARY KEY (rowid=?)", "REUSE SUBQUERY"}},
		{all, allArgs, []string{"INDEX torrent_journal_at (at<?)", "SCALAR SUBQUERY", "INTEGER PRIMARY KEY (rowid=?)", "REUSE SUBQUERY"}},
	} {
		plan := planOf(t, s, c.query, c.args...)
		require.Len(t, plan, len(c.want), c.query)
		for i, w := range c.want {
			require.Contains(t, plan[i], w)
		}
	}
}

// Of a kind alone, each outcome is read in the index's order, and only the
// limited rows of each are sorted to merge them.
func TestJournalKindPlan(t *testing.T) {
	s := open(t)
	q, args := journalSelect(JournalFilter{Kind: JournalProbed}, 0, 1)
	plan := planOf(t, s, q, args...)
	var searches int
	for i, line := range plan {
		if strings.HasPrefix(line, "SEARCH torrent_journal") {
			searches++
			require.Contains(t, line, "INDEX torrent_journal_kind (kind=? AND outcome=?)")
		}
		if strings.Contains(line, "TEMP B-TREE") {
			require.Contains(t, plan[i-1], "SCAN (subquery-", "a sort of a subquery's rows, not of the journal's")
		}
	}
	require.Equal(t, len(JournalOutcomes[JournalProbed]), searches, strings.Join(plan, "\n"))
}

// unix is the time in Unix seconds, as a journal detail writes it.
func unix(t time.Time) string {
	return strconv.FormatInt(t.Unix(), 10)
}

func TestJournalFetched(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	tr := torrent(1, "Alien.1979.1080p.BluRay", "Alien.mkv", "Alien.srt")
	tr.Discovery, tr.Kind, tr.KindLayer = Announce, "movie", LayerRules
	_, _, err := s.Insert(ctx, tr)
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"Alien.1979.1080p.BluRay","size":1073741824,"files":2,"discovery":"announce","kind":"movie","kind_layer":"rules"}`,
		entry(t, s, hash(1), JournalFetched, "stored").Detail)
	_, _, err = s.Insert(ctx, tr)
	require.NoError(t, err)
	require.Equal(t, []string{"fetched/stored", "fetched/duplicate"}, entries(t, s, hash(1)))

	ok, err := s.Reject(ctx, hash(2), rules.Adult, "xxx", 1<<20, t0)
	require.NoError(t, err)
	require.True(t, ok)
	require.JSONEq(t, `{"reason":"adult","name":"xxx","size":1048576}`, entry(t, s, hash(2), JournalFetched, "rejected").Detail)
	_, err = s.Reject(ctx, hash(2), rules.Adult, "xxx", 1<<20, t0)
	require.NoError(t, err)
	_, _, err = s.Insert(ctx, torrent(2, "xxx"))
	require.NoError(t, err)
	require.Equal(t, []string{"fetched/rejected"}, entries(t, s, hash(2)), "a second rejection and a store of a rejected hash write nothing")

	_, _, err = s.Insert(ctx, titled(3, "Heat.1995.1080p", "y:heat 1995"))
	require.NoError(t, err)
	matchTo(t, s, "y:heat 1995", TypeMovie, 949, "Heat")
	stored := titled(4, "Heat.1995.2160p", "y:heat 1995")
	stored.SeenAt = t0.Add(time.Hour)
	_, _, err = s.Insert(ctx, stored)
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"Heat.1995.2160p","size":1073741824,"discovery":"sample","kind":"movie","kind_layer":"rules"}`,
		entry(t, s, hash(4), JournalFetched, "stored").Detail)
	require.Equal(t, []string{"fetched/stored", "matched/earlier"}, entries(t, s, hash(4)), "the match its title key took before, not a search of its own")
	m := entry(t, s, hash(4), JournalMatched, "earlier")
	require.Equal(t, stored.SeenAt, m.At, "at the time it is stored")
	require.JSONEq(t, `{"tmdb_id":949,"type":"movie","confidence":1,"layer":"tmdb","key_matched_at":`+unix(t0)+`}`, m.Detail,
		"with the layer and the time of the key's match")

	_, _, err = s.RecordMatch(ctx, "y:ronin 1998", Found{}, t0)
	require.NoError(t, err)
	_, _, err = s.Insert(ctx, titled(5, "Ronin.1998.1080p", "y:ronin 1998"))
	require.NoError(t, err)
	require.Equal(t, []string{"fetched/stored"}, entries(t, s, hash(5)), "a key matched to nothing gives no work")
}

func TestJournalClassified(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for _, x := range []Torrent{torrent(1, "Alien.1979.1080p"), torrent(2, "Alien.1979.2160p"), torrent(3, "Alien.1979.720p")} {
		x.TitleKey = "y:alien 1979"
		_, _, err := s.Insert(ctx, x)
		require.NoError(t, err)
	}
	one := mustGet(t, s, hash(1))
	copied := classify(t, s, one.ID, Classification{Kind: "movie", KindConfidence: 0.9, Source: "web", SourceConfidence: 0.8, Audio: map[string]float64{"ru": 0.7}, Trigger: TriggerSearch})
	require.Equal(t, 2, copied)
	require.JSONEq(t, `{"kind":"movie","kind_confidence":0.9,"source":"web","source_confidence":0.8,"audio":{"ru":0.7},"copied":2,"trigger":"search"}`,
		entry(t, s, hash(1), JournalClassified, LayerClassifier).Detail)
	for _, h := range []Hash{hash(2), hash(3)} {
		require.JSONEq(t, `{"kind":"movie","from":"`+hexOf(hash(1))+`"}`, entry(t, s, h, JournalClassified, LayerReuse).Detail, "the sibling by infohash")
	}
	classify(t, s, mustGet(t, s, hash(2)).ID, Classification{Source: "remux", Trigger: TriggerWorker})
	require.JSONEq(t, `{"source":"remux","trigger":"worker"}`, entry(t, s, hash(2), JournalClassified, LayerClassifier).Detail, "a kind known already is not asked about")
	classify(t, s, mustGet(t, s, hash(3)).ID, Classification{Kind: "series", KindConfidence: 0.6, Source: "web", Trigger: TriggerWorker})
	require.JSONEq(t, `{"source":"web","trigger":"worker"}`, entry(t, s, hash(3), JournalClassified, LayerClassifier).Detail, "a kind a sibling gave first is not applied")
	require.Equal(t, "movie", mustGet(t, s, hash(3)).Kind)

	require.NoError(t, s.ClassifyFailed(ctx, mustGet(t, s, hash(3)).ID, "malformed", t0))
	require.JSONEq(t, `{"error":"malformed"}`, entry(t, s, hash(3), JournalClassified, "failed").Detail)
	require.NoError(t, s.ClassifyFailed(ctx, 99, "malformed", t0), "an unknown ID is nothing")

	unknown := torrent(4, "Heat.1995.1080p")
	unknown.TitleKey = "y:heat 1995"
	_, _, err := s.Insert(ctx, unknown)
	require.NoError(t, err)
	_, _, err = s.Insert(ctx, titled(5, "Heat.1995.2160p", "y:heat 1995"))
	require.NoError(t, err)
	require.JSONEq(t, `{"kind":"movie","from":"`+hexOf(hash(5))+`"}`, entry(t, s, hash(4), JournalClassified, LayerReuse).Detail, "a kind a stored sibling brings")
}

func TestJournalReuseCapped(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for i := 1; i <= journalReuse+2; i++ {
		x := torrent(byte(i), "Alien.1979."+strconv.Itoa(i))
		x.TitleKey = "y:alien 1979"
		_, _, err := s.Insert(ctx, x)
		require.NoError(t, err)
	}
	copied := classify(t, s, mustGet(t, s, hash(1)).ID, Classification{Kind: "movie"})
	require.Equal(t, journalReuse+1, copied)
	require.Equal(t, []string{"fetched/stored"}, entries(t, s, hash(2)), "a copy to more siblings than the cap is journaled on none")
	require.JSONEq(t, `{"kind":"movie","copied":51}`, entry(t, s, hash(1), JournalClassified, LayerClassifier).Detail)
}

func TestJournalMatched(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for _, x := range []Torrent{titled(1, "Alien.1979.1080p", "y:alien 1979"), titled(2, "Alien.1979.2160p", "y:alien 1979"), titled(3, "Heat.1995.1080p", "y:heat 1995")} {
		_, _, err := s.Insert(ctx, x)
		require.NoError(t, err)
	}
	work, _, err := s.RecordMatch(ctx, "y:alien 1979", Found{Hit: &Hit{Type: TypeMovie, TMDBID: 348, Title: "Alien"}, Confidence: 0.8, Layer: LayerClassifier, Trigger: TriggerWorker}, t0)
	require.NoError(t, err)
	for _, h := range []Hash{hash(1), hash(2)} {
		require.JSONEq(t, `{"tmdb_id":348,"type":"movie","confidence":0.8,"trigger":"worker"}`, entry(t, s, h, JournalMatched, LayerClassifier).Detail)
	}
	require.Equal(t, []string{"fetched/stored"}, entries(t, s, hash(3)), "another key")

	_, _, err = s.RecordMatch(ctx, "y:alien 1979", Found{Hit: &Hit{Type: TypeMovie, TMDBID: 8077, Title: "Alien³"}, Confidence: 1, Layer: LayerTMDB, Trigger: TriggerSearch}, t0.Add(time.Hour))
	require.NoError(t, err)
	require.JSONEq(t, `{"tmdb_id":8077,"type":"movie","confidence":1,"previous":`+strconv.FormatInt(work, 10)+`,"trigger":"search"}`,
		entry(t, s, hash(1), JournalMatched, LayerTMDB).Detail, "a rematch names the work before")
	_, _, err = s.RecordMatch(ctx, "y:alien 1979", Found{Hit: &Hit{Type: TypeMovie, TMDBID: 8077}, Confidence: 1, Layer: LayerTMDB}, t0.Add(2*time.Hour))
	require.NoError(t, err)
	require.Equal(t, []string{"fetched/stored", "matched/classifier", "matched/tmdb"}, entries(t, s, hash(1)), "the same match again changes no release")

	_, _, err = s.RecordMatch(ctx, "y:heat 1995", Found{Trigger: TriggerTorznab}, t0)
	require.NoError(t, err)
	require.JSONEq(t, `{"trigger":"torznab"}`, entry(t, s, hash(3), JournalMatched, "none").Detail)
	_, _, err = s.RecordMatch(ctx, "y:heat 1995", Found{Error: "tmdb: 401"}, t0.Add(time.Hour))
	require.NoError(t, err)
	require.JSONEq(t, `{"error":"tmdb: 401"}`, entry(t, s, hash(3), JournalMatched, "error").Detail)
	_, _, err = s.RecordMatch(ctx, "y:heat 1995", Found{Error: "tmdb: 503"}, t0.Add(2*time.Hour))
	require.NoError(t, err)
	_, _, err = s.RecordMatch(ctx, "y:heat 1995", Found{Trigger: TriggerWorker}, t0.Add(3*time.Hour))
	require.NoError(t, err)
	_, _, err = s.RecordMatch(ctx, "y:heat 1995", Found{Trigger: TriggerWorker}, t0.Add(4*time.Hour))
	require.NoError(t, err)
	require.Equal(t, []string{"fetched/stored", "matched/none", "matched/error", "matched/none"}, entries(t, s, hash(3)),
		"no match, or an error, again is not journaled; a change between them is")
	_, _, err = s.RecordMatch(ctx, "y:heat 1995", Found{Trigger: TriggerRequest}, t0.Add(5*time.Hour))
	require.NoError(t, err)
	require.Equal(t, []string{"fetched/stored", "matched/none", "matched/error", "matched/none", "matched/none"}, entries(t, s, hash(3)),
		"a lookup a person asked for is journaled though it finds nothing again")
	got, err := s.Journal(ctx, hash(3), 1)
	require.NoError(t, err)
	require.JSONEq(t, `{"trigger":"request"}`, got[0].Detail)

	for i := 10; i < 10+journalNoMatch+1; i++ {
		_, _, err := s.Insert(ctx, titled(byte(i), "Ronin.1998."+strconv.Itoa(i), "y:ronin 1998"))
		require.NoError(t, err)
	}
	_, _, err = s.RecordMatch(ctx, "y:ronin 1998", Found{}, t0)
	require.NoError(t, err)
	require.Equal(t, []string{"fetched/stored"}, entries(t, s, hash(10)), "a none on a key of many releases is journaled on none")
}

func TestJournalArr(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for _, x := range []Torrent{titled(1, "Alien.1979.1080p.BluRay", "y:alien 1979"), titled(2, "Alien.1979.2160p", "y:alien 1979")} {
		_, _, err := s.Insert(ctx, x)
		require.NoError(t, err)
	}
	grabbed, now := t0.Add(-90*24*time.Hour), t0.Add(time.Hour)
	e := grab(1, "abc", grabbed)
	e.Source, e.Season, e.Episode = "history", 0, 0
	rec, err := s.RecordArr(ctx, e, now)
	require.NoError(t, err)
	require.True(t, rec.New)
	got := entry(t, s, hash(1), JournalArr, EventGrab)
	require.JSONEq(t, `{"app":"radarr","source":"history","indexer":"tracker","title":"Alien.1979.1080p.BluRay","size":1073741824,"recorded_at":`+unix(now)+`}`, got.Detail)
	require.Equal(t, grabbed, got.At, "at the time of the grab, with the time it was recorded")
	matched := `{"tmdb_id":348,"type":"movie","trigger":"arr","event_at":` + unix(grabbed) + `}`
	for _, h := range []Hash{hash(1), hash(2)} {
		got := entry(t, s, h, JournalMatched, LayerArr)
		require.JSONEq(t, matched, got.Detail, "the key's other release follows")
		require.Equal(t, now, got.At)
	}
	_, err = s.RecordArr(ctx, e, now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, []string{"arr/grab", "fetched/stored", "matched/arr"}, entries(t, s, hash(1)), "in time order; a duplicate event writes nothing")
	swept, err := s.SweepJournal(ctx, now.Add(-30*24*time.Hour), 100)
	require.NoError(t, err)
	require.Equal(t, 1, swept, "a grab older than the retention is swept on the next sweep")
	require.Equal(t, []string{"fetched/stored", "matched/arr"}, entries(t, s, hash(1)))

	later := now.Add(time.Hour)
	pending := grab(3, "def", grabbed)
	pending.Source = "history"
	_, err = s.RecordArr(ctx, pending, now)
	require.NoError(t, err)
	require.Equal(t, []string{"arr/grab"}, entries(t, s, hash(3)), "the torrent is not stored yet")
	stored := titled(3, "Alien.1979.720p", "y:alien 1979")
	stored.SeenAt = later
	_, _, err = s.Insert(ctx, stored)
	require.NoError(t, err)
	got = entry(t, s, hash(3), JournalMatched, LayerArr)
	require.JSONEq(t, `{"tmdb_id":348,"type":"movie","trigger":"arr","pending":true,"event_at":`+unix(grabbed)+`}`, got.Detail)
	require.Equal(t, later, got.At, "applied as the torrent is stored")
	require.Equal(t, []string{"arr/grab", "fetched/stored", "matched/arr"}, entries(t, s, hash(3)))

	for _, x := range []Torrent{titled(5, "Aliens.1986.1080p", "y:aliens 1986"), titled(6, "Aliens.1986.720p", "y:aliens 1986")} {
		_, _, err := s.Insert(ctx, x)
		require.NoError(t, err)
	}
	_, err = s.RecordArr(ctx, grab(7, "ghi", grabbed), now)
	require.NoError(t, err)
	_, _, err = s.Insert(ctx, titled(7, "Aliens.1986.2160p", "y:aliens 1986"))
	require.NoError(t, err)
	require.JSONEq(t, `{"tmdb_id":348,"type":"movie","trigger":"arr","pending":true,"event_at":`+unix(grabbed)+`}`, entry(t, s, hash(7), JournalMatched, LayerArr).Detail)
	require.JSONEq(t, matched, entry(t, s, hash(5), JournalMatched, LayerArr).Detail, "the sibling's row is not pending")

	hashless := grab(8, "jkl", t0)
	hashless.Hash = nil
	rec, err = s.RecordArr(ctx, hashless, t0)
	require.NoError(t, err)
	require.True(t, rec.New)
	rec, err = s.RecordArr(ctx, grab(8, "jkl", t0.Add(time.Minute)), t0.Add(2*time.Minute))
	require.NoError(t, err)
	require.False(t, rec.New)
	_, err = s.RecordArr(ctx, grab(8, "jkl", t0.Add(3*time.Minute)), t0.Add(3*time.Minute))
	require.NoError(t, err)
	require.Equal(t, []string{"arr/grab"}, entries(t, s, hash(8)), "journaled once the event has its hash, and once")
	got = entry(t, s, hash(8), JournalArr, EventGrab)
	require.Equal(t, t0.Add(time.Minute), got.At)
	require.NotContains(t, got.Detail, "recorded_at", "recorded within a minute of the event")
}

func TestJournalProbes(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	h := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	_, err := s.WantProbe(ctx, []Hash{h, hash(9)}, ProbeWant{T: "tvsearch", Query: "alien", ID: "tmdbid:348", Season: 1, Episode: 2}, t0)
	require.NoError(t, err)
	require.JSONEq(t, `{"t":"tvsearch","query":"alien","id":"tmdbid:348","season":1,"episode":2}`, entry(t, s, h, JournalProbeWanted, "search").Detail)
	require.Empty(t, entries(t, s, hash(9)), "a hash not stored is ignored")

	final, _, err := s.ProbeFailed(ctx, h, ProbeTimeout, 1<<20, 1, t0, 2, time.Hour)
	require.NoError(t, err)
	require.False(t, final)
	require.JSONEq(t, `{"attempt":1,"retry_at":`+unix(t0.Add(time.Hour))+`,"final":false,"downloaded":1048576,"peers_week":5}`, entry(t, s, h, JournalProbed, ProbeTimeout).Detail)
	final, _, err = s.ProbeFailed(ctx, h, ProbeNoPeers, 0, 3, t0.Add(time.Hour), 2, time.Hour)
	require.NoError(t, err)
	require.True(t, final)
	require.JSONEq(t, `{"attempt":2,"final":true,"peers_week":5,"leases":3}`, entry(t, s, h, JournalProbed, ProbeNoPeers).Detail,
		"a final failure is due at no time; an attempt of three leases")

	require.NoError(t, s.ProbeDone(ctx, h, Probe{Outcome: ProbeTracks, Path: "Alien.1979.1080p.mkv", Format: "matroska", Downloaded: 2 << 20, Leases: 1, Tracks: []Track{
		{Kind: "video", Codec: "V_MPEG4/ISO/AVC"}, {Kind: "audio", Language: "rus", Codec: "A_AC3"}, {Kind: "subtitle", Language: "eng", Codec: "S_TEXT/UTF8"},
	}}, t0.Add(4*time.Hour)))
	require.JSONEq(t, `{"path":"Alien.1979.1080p.mkv","format":"matroska","downloaded":2097152,"tracks":["video::V_MPEG4/ISO/AVC","audio:rus:A_AC3","subtitle:eng:S_TEXT/UTF8"],"audio":{"ru":1}}`,
		entry(t, s, h, JournalProbed, ProbeTracks).Detail)
	require.NoError(t, s.ProbeDone(ctx, h, Probe{Outcome: ProbeUnknown, Downloaded: 3, Leases: 2}, t0.Add(5*time.Hour)))
	require.JSONEq(t, `{"downloaded":3,"leases":2}`, entry(t, s, h, JournalProbed, ProbeUnknown).Detail)
	require.Equal(t, []string{"fetched/stored", "probe_wanted/search", "probed/timeout", "probed/no_peers", "probed/tracks", "probed/unknown"}, entries(t, s, h))

	_, err = s.WantProbe(ctx, []Hash{h}, ProbeWant{T: "movie", Query: "alien"}, t0.Add(25*time.Hour))
	require.NoError(t, err)
	require.Len(t, entries(t, s, h), 6, "a search does not want a probe done")
	_, n, err := s.RequestProbe(ctx, []Hash{h, hash(9)}, t0.Add(26*time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, JournalEntry{Hash: h, At: t0.Add(26 * time.Hour), Kind: JournalProbeWanted, Outcome: "request"}, entry(t, s, h, JournalProbeWanted, "request"))
	_, _, err = s.RequestProbe(ctx, []Hash{h}, t0.Add(27*time.Hour))
	require.NoError(t, err)
	require.Len(t, entries(t, s, h), 8, "every request is journaled")
}

func TestJournalProbeWanted(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	queued := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	probed := insertProbeable(t, s, 2, "Heat.1995.1080p", 5, "Heat.1995.1080p.mkv")
	spent := insertProbeable(t, s, 4, "Brazil.1985.1080p", 5, "Brazil.1985.1080p.mkv")
	unreadable := insertProbeable(t, s, 5, "Ronin.1998.DVDRip", 5, "Ronin.1998.DVDRip.avi")
	require.NoError(t, s.ProbeDone(ctx, probed, Probe{Outcome: ProbeUnknown}, t0))
	require.True(t, probeFailed(t, s, spent, t0, 1))
	search := func(at time.Time) {
		t.Helper()
		_, err := s.WantProbe(ctx, []Hash{queued, probed, spent, unreadable}, ProbeWant{T: "movie", Query: "x"}, at)
		require.NoError(t, err)
	}

	search(t0)
	require.Equal(t, []string{"fetched/stored", "probe_wanted/search"}, entries(t, s, queued))
	require.Equal(t, []string{"fetched/stored", "probed/unknown"}, entries(t, s, probed), "a search that orders no probe is not journaled")
	require.Equal(t, []string{"fetched/stored", "probed/timeout"}, entries(t, s, spent), "nor one of a release whose attempts ran out")
	require.Equal(t, []string{"fetched/stored"}, entries(t, s, unreadable), "nor one of a release a probe cannot read")
	search(t0.Add(23 * time.Hour))
	require.Len(t, entries(t, s, queued), 2, "wanted again within a day is not journaled")
	search(t0.Add(25 * time.Hour))
	got, err := s.Journal(ctx, queued, 1)
	require.NoError(t, err)
	require.Equal(t, JournalEntry{ID: got[0].ID, Hash: queued, At: t0.Add(25 * time.Hour), Kind: JournalProbeWanted, Outcome: "search", Detail: `{"query":"x","t":"movie"}`}, got[0],
		"a day after the last it is")
	require.Len(t, entries(t, s, probed), 2)
	require.Len(t, entries(t, s, spent), 2)

	again := insertProbeable(t, s, 7, "Fargo.1996.2160p", 5, "Fargo.1996.2160p.mkv")
	_, _, err = s.RequestProbe(ctx, []Hash{again}, t0)
	require.NoError(t, err)
	_, err = s.WantProbe(ctx, []Hash{again}, ProbeWant{T: "movie", Query: "fargo"}, t0.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, []string{"fetched/stored", "probe_wanted/request", "probe_wanted/search"}, entries(t, s, again), "a request does not hide a search")
}

func TestJournalProbeLeased(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	h := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	lease := func(priority int, at time.Time) JournalEntry {
		t.Helper()
		require.NoError(t, s.RecordProbeLease(ctx, ProbeLease{Hash: h, Priority: priority, MaxBytes: 64 << 20, Until: at.Add(10 * time.Minute)}, at))
		got, err := s.Journal(ctx, h, 1)
		require.NoError(t, err)
		return got[0]
	}

	e := lease(PrioritySample, t0)
	require.Equal(t, "background", e.Outcome)
	require.JSONEq(t, `{"attempt":1,"max_bytes":67108864,"lease_until":`+unix(t0.Add(10*time.Minute))+`}`, e.Detail)
	require.Equal(t, "search", lease(PriorityArr, t0).Outcome)
	require.Equal(t, "request", lease(PriorityRequest, t0).Outcome)

	probeFailed(t, s, h, t0, 3)
	e = lease(PriorityArr, t0.Add(time.Hour))
	require.Equal(t, "retry", e.Outcome, "a release whose probe failed before")
	require.Contains(t, e.Detail, `"attempt":2`)
	require.Equal(t, "request", lease(PriorityRequest, t0.Add(time.Hour)).Outcome, "a request after a failure is a request")

	require.NoError(t, s.ProbeDone(ctx, h, Probe{Outcome: ProbeUnknown}, t0.Add(2*time.Hour)))
	require.Contains(t, lease(PriorityRequest, t0.Add(3*time.Hour)).Detail, `"attempt":1`, "a probe that did not fail starts the count over")

	n := len(entries(t, s, hash(9)))
	require.NoError(t, s.RecordProbeLease(ctx, ProbeLease{Hash: hash(9)}, t0))
	require.Len(t, entries(t, s, hash(9)), n, "a hash not stored is ignored")
}

func TestJournalDeleted(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	_, _, err := s.Insert(ctx, titled(1, "Alien.1979.1080p", "y:alien 1979"))
	require.NoError(t, err)
	matchTo(t, s, "y:alien 1979", TypeMovie, 348, "Alien")
	_, _, err = s.Insert(ctx, torrent(2, "Unknown.2024"))
	require.NoError(t, err)
	now := t0.Add(48 * time.Hour)
	sw, err := s.Sweep(ctx, t0.Add(36*time.Hour), t0.Add(time.Hour), now, 10)
	require.NoError(t, err)
	require.Equal(t, Swept{Idle: 1, Expired: 1}, sw)
	require.JSONEq(t, `{"name":"Alien.1979.1080p","last_seen":`+unix(t0)+`}`, entry(t, s, hash(1), JournalDeleted, "expired").Detail)
	require.JSONEq(t, `{"name":"Unknown.2024","last_seen":`+unix(t0)+`}`, entry(t, s, hash(2), JournalDeleted, "idle").Detail)
	require.Equal(t, now, entry(t, s, hash(2), JournalDeleted, "idle").At)
	require.Equal(t, []string{"fetched/stored", "matched/tmdb", "deleted/expired"}, entries(t, s, hash(1)), "the trail outlives the torrent")
}

func TestJournalFetchedLeases(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	tr := torrent(1, "Alien.1979.1")
	tr.SeenAt, tr.QueuedAt, tr.Leases = t0.Add(3*time.Minute), t0, 3
	_, _, err := s.Insert(ctx, tr)
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"Alien.1979.1","size":1073741824,"discovery":"sample","queued_at":`+unix(t0)+`,"leases":3}`, entry(t, s, hash(1), JournalFetched, "stored").Detail,
		"when its order was queued and the leases its attempt took, as the failure rows say")
	tr = torrent(2, "Alien.1979.2")
	tr.Leases = 1
	_, _, err = s.Insert(ctx, tr)
	require.NoError(t, err)
	require.NotContains(t, entry(t, s, hash(2), JournalFetched, "stored").Detail, "leases", "none for an attempt of one lease")
}
