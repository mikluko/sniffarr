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

	"github.com/mikluko/sniffarr/internal/database"
	"github.com/mikluko/sniffarr/internal/rules"
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
	p, err := s.JournalPage(ctx, hash(1), 0, 2)
	require.NoError(t, err)
	require.Equal(t, []int64{4, 5}, ids(p), "the newest, ordered by time, then by writing")
	require.Equal(t, JournalPage{Entries: p.Entries, Total: 5, Older: 3}, p)
	p, err = s.JournalPage(ctx, hash(1), 4, 2)
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2}, ids(p), "those before the entry")
	require.Equal(t, 5, p.Total)
	require.Equal(t, 1, p.Older)
	p, err = s.JournalPage(ctx, hash(1), 1, 2)
	require.NoError(t, err)
	require.Equal(t, []int64{3}, ids(p), "an entry written later of an earlier time comes before")
	require.Zero(t, p.Older)
	p, err = s.JournalPage(ctx, hash(1), 6, 2)
	require.NoError(t, err)
	require.Empty(t, p.Entries, "before an entry of another hash, none")
	require.Equal(t, 5, p.Total)
	p, err = s.JournalPage(ctx, hash(3), 0, 2)
	require.NoError(t, err)
	require.Equal(t, JournalPage{}, p)
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
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{})
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

func TestJournalPlans(t *testing.T) {
	s := open(t)
	for _, c := range []struct {
		query string
		args  []any
		want  []string
	}{
		{journalSweep, []any{0, 1}, []string{"INDEX torrent_journal_at (at<?)"}},
		{journalQuery, []any{0, 0, 1}, []string{"INDEX torrent_journal_infohash (infohash=?)", "SCALAR SUBQUERY", "INTEGER PRIMARY KEY (rowid=?)"}},
	} {
		var plan []string
		require.NoError(t, s.each(context.Background(), func(rows *sql.Rows) error {
			var id, parent, unused int
			var d string
			if err := rows.Scan(&id, &parent, &unused, &d); err != nil {
				return err
			}
			plan = append(plan, d)
			return nil
		}, "EXPLAIN QUERY PLAN "+c.query, c.args...))
		require.Len(t, plan, len(c.want), c.query)
		for i, w := range c.want {
			require.Contains(t, plan[i], w)
		}
	}
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

func TestJournalQueue(t *testing.T) {
	ctx := context.Background()
	s, db := openAt(t, filepath.Join(t.TempDir(), "test.db"))
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	q, err := NewTasks(ctx, db, TaskOptions{MaxSize: 1, MaxAge: time.Hour, DeadFor: 24 * time.Hour, NoPeersFor: time.Hour})
	require.NoError(t, err)
	const owner = 7
	enqueue(t, q, Announce, 1)
	enqueue(t, q, Sample, 2)
	require.JSONEq(t, `{"discovery":"announce"}`, entry(t, s, hash(1), JournalSighted, "queued").Detail)
	require.Empty(t, entries(t, s, hash(2)), "a sample's sighting is not journaled")

	require.Equal(t, []Hash{hash(1), hash(2)}, lease(t, q, owner, 2, t0))
	require.JSONEq(t, `{"attempt":1,"lease_until":`+unix(t0.Add(time.Minute))+`}`, entry(t, s, hash(1), JournalLeased, "fetch").Detail)
	require.Empty(t, entries(t, s, hash(2)), "nor its lease")
	first := t0.Add(time.Minute)
	require.Equal(t, []Hash{hash(1), hash(2)}, lease(t, q, owner, 2, first), "the leases expired")
	require.Equal(t, []string{"sighted/queued", "leased/fetch"}, entries(t, s, hash(1)), "a lease of the same attempt again is not journaled")

	at := first
	for attempt := 1; attempt <= 3; attempt++ {
		dropped, err := q.Fail(ctx, hash(1), owner, at, 3, 10*time.Minute)
		require.NoError(t, err)
		require.Equal(t, attempt == 3, dropped)
		_, err = q.Fail(ctx, hash(2), owner, at, 3, 10*time.Minute)
		require.NoError(t, err)
		if attempt < 3 {
			at = at.Add(10 * time.Minute << (attempt - 1))
			require.Equal(t, []Hash{hash(1), hash(2)}, lease(t, q, owner, 2, at))
		}
	}
	require.Equal(t, []string{"sighted/queued", "leased/fetch", "fetch_failed/timeout", "leased/fetch", "fetch_failed/timeout", "leased/fetch", "fetch_failed/dropped"},
		entries(t, s, hash(1)))
	got, err := s.Journal(ctx, hash(1), 10)
	require.NoError(t, err)
	require.JSONEq(t, `{"attempt":1,"retry_at":`+unix(first.Add(10*time.Minute))+`,"leases":2}`, got[2].Detail, "the attempt's lease expired once")
	require.JSONEq(t, `{"attempt":2,"lease_until":`+unix(first.Add(11*time.Minute))+`}`, got[3].Detail, "the attempt number carries over the retry")
	require.JSONEq(t, `{"attempt":3,"dead_until":`+unix(at.Add(24*time.Hour))+`}`, got[6].Detail)
	require.Empty(t, entries(t, s, hash(2)), "nor its failures")

	enqueueAt(t, q, at, Announce, 3)
	require.Len(t, lease(t, q, owner, 1, at), 1)
	require.Len(t, lease(t, q, owner, 1, at.Add(time.Minute)), 1)
	require.NoError(t, q.NoPeers(ctx, hash(3), owner, at))
	require.JSONEq(t, `{"dead_until":`+unix(at.Add(time.Hour))+`,"leases":2}`, entry(t, s, hash(3), JournalFetchFailed, "no_peers").Detail)
	enqueueAt(t, q, at, Announce, 3)
	require.JSONEq(t, `{"discovery":"announce"}`, entry(t, s, hash(3), JournalSighted, "revived").Detail)
	require.Equal(t, []string{"sighted/queued", "leased/fetch", "fetch_failed/no_peers", "sighted/revived"}, entries(t, s, hash(3)), "a revival is not queued too")

	enqueueAt(t, q, at, Sample, 4)
	enqueueAt(t, q, at, Feed, 4)
	require.JSONEq(t, `{"from":"sample","to":"feed"}`, entry(t, s, hash(4), JournalSighted, "promoted").Detail)
	enqueueAt(t, q, at, Announce, 4)
	require.Equal(t, []string{"sighted/promoted"}, entries(t, s, hash(4)), "a later discovery does not promote")

	tr := torrent(4, "Four.2024.1080p")
	tr.SeenAt = at
	_, _, err = s.Insert(ctx, tr)
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"Four.2024.1080p","size":1073741824,"discovery":"sample","queued_at":`+unix(at)+`}`, entry(t, s, hash(4), JournalFetched, "stored").Detail,
		"the wait in the queue is derivable while the queue row is there")
	require.NoError(t, q.Complete(ctx, TaskFetch, hash(4)))

	later := at.Add(2 * time.Hour)
	enqueueAt(t, q, later, Announce, 5, 6)
	trimmed, err := q.Trim(ctx, later)
	require.NoError(t, err)
	require.Equal(t, Trimmed{Aged: 1, Oversize: 1}, trimmed, "3 aged; 5 over the size")
	require.JSONEq(t, `{"queued_at":`+unix(at)+`,"discovery":"announce","dead_until":`+unix(later.Add(24*time.Hour))+`}`, entry(t, s, hash(3), JournalEvicted, "aged").Detail)
	require.JSONEq(t, `{"queued_at":`+unix(later)+`,"discovery":"announce","dead_until":`+unix(later.Add(24*time.Hour))+`}`, entry(t, s, hash(5), JournalEvicted, "oversize").Detail)
	require.Equal(t, []string{"sighted/queued"}, entries(t, s, hash(6)))
}

func TestJournalFetchedLeases(t *testing.T) {
	ctx := context.Background()
	s, db := openAt(t, filepath.Join(t.TempDir(), "test.db"))
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	q, err := NewTasks(ctx, db, TaskOptions{})
	require.NoError(t, err)
	enqueue(t, q, Announce, 1, 2)
	require.Equal(t, []Hash{hash(1), hash(2)}, lease(t, q, 7, 2, t0))
	require.Equal(t, []Hash{hash(1), hash(2)}, lease(t, q, 7, 2, t0.Add(time.Minute)), "the first leases expired")
	require.Equal(t, []Hash{hash(1)}, lease(t, q, 7, 1, t0.Add(2*time.Minute)), "and hash 1's second")
	require.NoError(t, q.Complete(ctx, TaskFetch, hash(2)))
	for _, b := range []byte{1, 2} {
		tr := torrent(b, "Alien.1979."+strconv.Itoa(int(b)))
		tr.SeenAt = t0.Add(3 * time.Minute)
		_, _, err = s.Insert(ctx, tr)
		require.NoError(t, err)
	}
	require.JSONEq(t, `{"name":"Alien.1979.1","size":1073741824,"discovery":"sample","queued_at":`+unix(t0)+`,"leases":3}`, entry(t, s, hash(1), JournalFetched, "stored").Detail,
		"the leases its attempt took, as the failure rows say")
	require.NotContains(t, entry(t, s, hash(2), JournalFetched, "stored").Detail, "leases", "none for a hash no longer queued")
}

func TestJournalLeasedAfterPromotion(t *testing.T) {
	ctx := context.Background()
	s, db := openAt(t, filepath.Join(t.TempDir(), "test.db"))
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	q, err := NewTasks(ctx, db, TaskOptions{})
	require.NoError(t, err)
	const owner = 7
	enqueue(t, q, Sample, 1)
	require.Equal(t, []Hash{hash(1)}, lease(t, q, owner, 1, t0))
	moved := t0.Add(2 * time.Minute)
	enqueueAt(t, q, moved, Announce, 1)
	require.Equal(t, []Hash{hash(1)}, lease(t, q, owner, 1, moved), "the sample's lease expired")
	require.JSONEq(t, `{"attempt":1,"lease_until":`+unix(moved.Add(time.Minute))+`}`, entry(t, s, hash(1), JournalLeased, "fetch").Detail,
		"the first lease since an announce moved it ahead")

	again := moved.Add(2 * time.Minute)
	enqueueAt(t, q, again, Announce, 1)
	require.Equal(t, []Hash{hash(1)}, lease(t, q, owner, 1, again))
	_, err = q.Fail(ctx, hash(1), owner, again, 3, time.Minute)
	require.NoError(t, err)
	require.Equal(t, []string{"sighted/promoted", "leased/fetch", "fetch_failed/timeout"}, entries(t, s, hash(1)), "a sighting that moves it nowhere keeps the count")
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
	require.NoError(t, s.WantProbe(ctx, []Hash{h, hash(9)}, ProbeWant{T: "tvsearch", Query: "alien", ID: "tmdbid:348", Season: 1, Episode: 2}, t0))
	require.JSONEq(t, `{"t":"tvsearch","query":"alien","id":"tmdbid:348","season":1,"episode":2}`, entry(t, s, h, JournalProbeWanted, "search").Detail)
	require.Empty(t, entries(t, s, hash(9)), "a hash not stored is ignored")

	final, err := s.ProbeFailed(ctx, h, ProbeTimeout, 1<<20, t0, 2, time.Hour)
	require.NoError(t, err)
	require.False(t, final)
	require.JSONEq(t, `{"attempt":1,"retry_at":`+unix(t0.Add(time.Hour))+`,"final":false,"downloaded":1048576,"peers_week":5}`, entry(t, s, h, JournalProbed, ProbeTimeout).Detail)
	final, err = s.ProbeFailed(ctx, h, ProbeNoPeers, 0, t0.Add(time.Hour), 2, time.Hour)
	require.NoError(t, err)
	require.True(t, final)
	require.JSONEq(t, `{"attempt":2,"final":true,"peers_week":5}`, entry(t, s, h, JournalProbed, ProbeNoPeers).Detail, "a final failure is due at no time")

	require.NoError(t, s.ProbeDone(ctx, h, Probe{Outcome: ProbeTracks, Path: "Alien.1979.1080p.mkv", Format: "matroska", Downloaded: 2 << 20, Tracks: []Track{
		{Kind: "video", Codec: "V_MPEG4/ISO/AVC"}, {Kind: "audio", Language: "rus", Codec: "A_AC3"}, {Kind: "subtitle", Language: "eng", Codec: "S_TEXT/UTF8"},
	}}, t0.Add(4*time.Hour)))
	require.JSONEq(t, `{"path":"Alien.1979.1080p.mkv","format":"matroska","downloaded":2097152,"tracks":["video::V_MPEG4/ISO/AVC","audio:rus:A_AC3","subtitle:eng:S_TEXT/UTF8"],"audio":{"ru":1}}`,
		entry(t, s, h, JournalProbed, ProbeTracks).Detail)
	require.NoError(t, s.ProbeDone(ctx, h, Probe{Outcome: ProbeUnknown, Downloaded: 3}, t0.Add(5*time.Hour)))
	require.JSONEq(t, `{"downloaded":3}`, entry(t, s, h, JournalProbed, ProbeUnknown).Detail)
	require.Equal(t, []string{"fetched/stored", "probe_wanted/search", "probed/timeout", "probed/no_peers", "probed/tracks", "probed/unknown"}, entries(t, s, h))

	require.NoError(t, s.JournalFeed(ctx, h, t0, "queued", Detail("source", "rutracker", "title", "Alien", "published_at", t0)))
	require.JSONEq(t, `{"source":"rutracker","title":"Alien","published_at":`+unix(t0)+`}`, entry(t, s, h, JournalFeed, "queued").Detail)

	require.NoError(t, s.WantProbe(ctx, []Hash{h}, ProbeWant{T: "movie", Query: "alien"}, t0.Add(25*time.Hour)))
	require.Len(t, entries(t, s, h), 7, "a search does not want a probe done")
	n, _, err := s.RequestProbe(ctx, []Hash{h, hash(9)}, t0.Add(26*time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, JournalEntry{Hash: h, At: t0.Add(26 * time.Hour), Kind: JournalProbeWanted, Outcome: "request"}, entry(t, s, h, JournalProbeWanted, "request"))
}

func TestJournalProbeWanted(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	queued := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	probed := insertProbeable(t, s, 2, "Heat.1995.1080p", 5, "Heat.1995.1080p.mkv")
	spent := insertProbeable(t, s, 4, "Brazil.1985.1080p", 5, "Brazil.1985.1080p.mkv")
	unreadable := insertProbeable(t, s, 5, "Ronin.1998.DVDRip", 5, "Ronin.1998.DVDRip.avi")
	requested := insertProbeable(t, s, 6, "Fargo.1996.1080p", 5, "Fargo.1996.1080p.mkv")
	require.NoError(t, s.ProbeDone(ctx, probed, Probe{Outcome: ProbeUnknown}, t0))
	final, err := s.ProbeFailed(ctx, spent, ProbeTimeout, 0, t0, 1, time.Hour)
	require.NoError(t, err)
	require.True(t, final)
	_, _, err = s.RequestProbe(ctx, []Hash{requested}, t0)
	require.NoError(t, err)
	search := func(at time.Time) {
		t.Helper()
		require.NoError(t, s.WantProbe(ctx, []Hash{queued, probed, spent, unreadable, requested}, ProbeWant{T: "movie", Query: "x"}, at))
	}

	search(t0)
	require.Equal(t, []string{"fetched/stored", "probe_wanted/search"}, entries(t, s, queued))
	require.Equal(t, []string{"fetched/stored", "probed/unknown"}, entries(t, s, probed), "a search that queues no probe is not journaled")
	require.Equal(t, []string{"fetched/stored", "probed/timeout"}, entries(t, s, spent), "nor one of a release whose attempts ran out")
	require.Equal(t, []string{"fetched/stored"}, entries(t, s, unreadable), "nor one of a release a probe cannot read")
	require.Equal(t, []string{"fetched/stored", "probe_wanted/request"}, entries(t, s, requested), "nor one of a release requested")
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
	_, err = s.ProbeFailed(ctx, again, ProbeTimeout, 0, t0.Add(time.Minute), 3, time.Hour)
	require.NoError(t, err)
	require.NoError(t, s.WantProbe(ctx, []Hash{again}, ProbeWant{T: "movie", Query: "fargo"}, t0.Add(2*time.Hour)))
	require.Equal(t, []string{"fetched/stored", "probe_wanted/request", "probed/timeout", "probe_wanted/search"}, entries(t, s, again),
		"a request within the day does not hide a search that queued the probe again")
}

// leasedProbes returns the hash's probe_leased entries as outcome/attempt,
// oldest first.
func leasedProbes(t *testing.T, s *Store, h Hash) []string {
	t.Helper()
	got, err := s.Journal(context.Background(), h, 1000)
	require.NoError(t, err)
	var out []string
	for _, e := range got {
		if e.Kind != JournalProbeLeased {
			continue
		}
		var d struct{ Attempt int }
		require.NoError(t, json.Unmarshal([]byte(e.Detail), &d))
		out = append(out, e.Outcome+"/"+strconv.Itoa(d.Attempt))
	}
	return out
}

// requestRows returns how many probe_wanted/request entries the hash has.
func requestRows(t *testing.T, s *Store, h Hash) int {
	t.Helper()
	got, err := s.Journal(context.Background(), h, 1000)
	require.NoError(t, err)
	n := 0
	for _, e := range got {
		if e.Kind == JournalProbeWanted && e.Outcome == "request" {
			n++
		}
	}
	return n
}

func TestJournalProbeRequestRepeated(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	h := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	request := func(at time.Time) {
		t.Helper()
		n, _, err := s.RequestProbe(ctx, []Hash{h}, at)
		require.NoError(t, err)
		require.Equal(t, 1, n, "still counted as stored")
	}

	request(t0)
	request(t0.Add(time.Minute))
	require.Equal(t, 1, requestRows(t, s, h), "a request while it waits for its first lease")

	_, ok := leaseNext(t, s, t0.Add(2*time.Minute), false)
	require.True(t, ok)
	request(t0.Add(3 * time.Minute))
	require.Equal(t, 2, requestRows(t, s, h), "a request after a lease began")
	request(t0.Add(4 * time.Minute))
	require.Equal(t, 2, requestRows(t, s, h), "none since that one")

	s2 := open(t)
	moved := insertProbeable(t, s2, 2, "Heat.1995.1080p", 5, "Heat.1995.1080p.mkv")
	require.NoError(t, s2.WantProbe(ctx, []Hash{moved}, ProbeWant{T: "movie", Query: "heat"}, t0))
	_, _, err := s2.RequestProbe(ctx, []Hash{moved}, t0.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 1, requestRows(t, s2, moved), "a request that moves a searched probe ahead is the first")
}

func TestJournalProbeLeasedOncePerAttempt(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name       string
		search     bool
		background bool
		outcome    string
	}{
		{name: "queued by its first lease", background: true, outcome: "background"},
		{name: "queued by a search", search: true, outcome: "search"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db := openAt(t, filepath.Join(t.TempDir(), "test.db"))
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			q, err := NewTasks(ctx, db, TaskOptions{})
			require.NoError(t, err)
			h := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
			if tc.search {
				require.NoError(t, s.WantProbe(ctx, []Hash{h}, ProbeWant{T: "movie", Query: "alien"}, t0))
			}
			lease := func(at time.Time) {
				t.Helper()
				c, ok, err := s.LeaseProbe(ctx, ProbeRequest{Owner: 7, Now: at, Lease: 10 * time.Minute, MaxBytes: 64 << 20, Admit: admitting(tc.background)})
				require.NoError(t, err)
				require.True(t, ok)
				require.Equal(t, h, c.Hash)
			}

			lease(t0)
			require.JSONEq(t, `{"attempt":1,"max_bytes":67108864,"lease_until":`+unix(t0.Add(10*time.Minute))+`}`, entry(t, s, h, JournalProbeLeased, tc.outcome).Detail,
				"when the lease ends, and nothing of the day's other probes")
			lease(t0.Add(10 * time.Minute))
			require.Equal(t, []string{tc.outcome + "/1"}, leasedProbes(t, s, h), "a lease again after one expired is the same attempt")
			require.NoError(t, q.Reset(ctx))
			lease(t0.Add(11 * time.Minute))
			require.Equal(t, []string{tc.outcome + "/1"}, leasedProbes(t, s, h), "so is one after a restart freed it")

			final, err := s.ProbeFailed(ctx, h, ProbeTimeout, 0, t0.Add(12*time.Minute), 3, time.Hour)
			require.NoError(t, err)
			require.False(t, final)
			require.Contains(t, entry(t, s, h, JournalProbed, ProbeTimeout).Detail, `"leases":3`, "an attempt of three leases")
			lease(t0.Add(2 * time.Hour))
			lease(t0.Add(2*time.Hour + 10*time.Minute))
			require.Equal(t, []string{tc.outcome + "/1", "retry/2"}, leasedProbes(t, s, h), "a failure starts the next attempt")
			require.NoError(t, s.ProbeDone(ctx, h, Probe{Outcome: ProbeUnknown}, t0.Add(2*time.Hour+20*time.Minute)))
			require.JSONEq(t, `{"leases":2}`, entry(t, s, h, JournalProbed, ProbeUnknown).Detail, "the leases of its own attempt")
		})
	}
}

func TestJournalProbeLeasedRequest(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	h := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	_, err := s.ProbeFailed(ctx, h, ProbeTimeout, 0, t0, 3, time.Hour)
	require.NoError(t, err)
	_, _, err = s.RequestProbe(ctx, []Hash{h}, t0.Add(time.Minute))
	require.NoError(t, err)
	_, ok := leaseNext(t, s, t0.Add(time.Minute), false)
	require.True(t, ok)
	require.Equal(t, []string{"request/2"}, leasedProbes(t, s, h), "a request after a failure is a request")

	_, err = s.ProbeFailed(ctx, h, ProbeTimeout, 0, t0.Add(2*time.Minute), 3, time.Hour)
	require.NoError(t, err)
	_, ok = leaseNext(t, s, t0.Add(3*time.Hour), false)
	require.True(t, ok)
	require.Equal(t, []string{"request/2", "retry/3"}, leasedProbes(t, s, h), "its failure ends the request")

	require.NoError(t, s.ProbeDone(ctx, h, Probe{Outcome: ProbeUnknown}, t0.Add(4*time.Hour)))
	_, _, err = s.RequestProbe(ctx, []Hash{h}, t0.Add(5*time.Hour))
	require.NoError(t, err)
	c, ok := leaseNext(t, s, t0.Add(5*time.Hour), false)
	require.True(t, ok)
	require.Zero(t, c.Attempts)
	require.Equal(t, []string{"request/2", "retry/3", "request/1"}, leasedProbes(t, s, h), "a probe that did not fail starts the count over")
}

func TestJournalProbeLeasedRequestInFlight(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	searched := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	requested := insertProbeable(t, s, 2, "Heat.1995.1080p", 5, "Heat.1995.1080p.mkv")
	require.NoError(t, s.WantProbe(ctx, []Hash{searched}, ProbeWant{T: "movie", Query: "alien"}, t0))
	_, _, err := s.RequestProbe(ctx, []Hash{requested}, t0)
	require.NoError(t, err)
	for range 2 {
		_, ok := leaseNext(t, s, t0, false)
		require.True(t, ok)
	}
	_, _, err = s.RequestProbe(ctx, []Hash{searched, requested}, t0.Add(time.Minute))
	require.NoError(t, err)
	for _, h := range []Hash{searched, requested} {
		_, err = s.ProbeFailed(ctx, h, ProbeTimeout, 0, t0.Add(2*time.Minute), 3, time.Hour)
		require.NoError(t, err)
	}
	for range 2 {
		_, ok := leaseNext(t, s, t0.Add(3*time.Minute), false)
		require.True(t, ok)
	}
	for _, h := range []Hash{searched, requested} {
		require.NotContains(t, entry(t, s, h, JournalProbed, ProbeTimeout).Detail, "retry_at", "kept due at once by the request")
	}
	require.Equal(t, []string{"search/1", "request/2"}, leasedProbes(t, s, searched), "a request made while the attempt was in flight outlives its failure")
	require.Equal(t, []string{"request/1", "request/2"}, leasedProbes(t, s, requested), "and so does one made again")
}

func TestJournalProbeLeasedAfterMove(t *testing.T) {
	ctx := context.Background()
	type move func(*Store, Hash, time.Time) error
	request := func(s *Store, h Hash, at time.Time) error {
		_, _, err := s.RequestProbe(ctx, []Hash{h}, at)
		return err
	}
	search := func(s *Store, h Hash, at time.Time) error {
		return s.WantProbe(ctx, []Hash{h}, ProbeWant{T: "movie", Query: "alien"}, at)
	}
	for _, tc := range []struct {
		name  string
		moves []move
		want  []string
	}{
		{name: "requested twice", moves: []move{request, request}, want: []string{"background/1", "request/1"}},
		{name: "searched twice", moves: []move{search, search}, want: []string{"background/1", "search/1"}},
		{name: "requested, then searched", moves: []move{request, search}, want: []string{"background/1", "request/1"}},
		{name: "searched, then requested", moves: []move{search, request}, want: []string{"background/1", "search/1", "request/1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := open(t)
			h := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
			lease := func(at time.Time) {
				t.Helper()
				c, ok, err := s.LeaseProbe(ctx, ProbeRequest{Owner: 7, Now: at, Lease: 10 * time.Minute})
				require.NoError(t, err)
				require.True(t, ok)
				require.Equal(t, h, c.Hash)
			}
			at := t0
			lease(at)
			for _, m := range tc.moves {
				at = at.Add(20 * time.Minute)
				require.NoError(t, m(s, h, at))
				lease(at)
			}
			require.Equal(t, tc.want, leasedProbes(t, s, h), "the first lease since a move ahead is journaled; a move to where it is keeps the count")
		})
	}
}

func TestJournalProbeLeaseFails(t *testing.T) {
	ctx := context.Background()
	// failing opens a store with a probeable release, queued for a probe by
	// a search when searched, whose probe_leased entries fail to write.
	failing := func(t *testing.T, searched bool) (*Store, Hash) {
		t.Helper()
		s := open(t)
		h := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
		if searched {
			require.NoError(t, s.WantProbe(ctx, []Hash{h}, ProbeWant{T: "movie", Query: "alien"}, t0))
		}
		_, err := s.w.ExecContext(ctx, `CREATE TRIGGER probe_leased_fails BEFORE INSERT ON torrent_journal WHEN NEW.kind = '`+JournalProbeLeased+`'
			BEGIN SELECT RAISE(ABORT, 'probe_leased'); END`)
		require.NoError(t, err)
		return s, h
	}
	lease := func(t *testing.T, s *Store) {
		t.Helper()
		c, ok, err := s.LeaseProbe(ctx, ProbeRequest{Owner: 7, Now: t0, Lease: 10 * time.Minute})
		require.Error(t, err)
		require.False(t, ok)
		require.Equal(t, ProbeCandidate{}, c, "no candidate with the error")
	}

	t.Run("queued by its lease", func(t *testing.T) {
		s, _ := failing(t, false)
		lease(t, s)
		var n int
		require.NoError(t, s.r.QueryRowContext(ctx, "SELECT count(*) FROM task_queue WHERE kind = 'probe'").Scan(&n))
		require.Zero(t, n, "the task is not queued")
	})
	t.Run("queued by a search", func(t *testing.T) {
		s, h := failing(t, true)
		lease(t, s)
		var leased bool
		var leases int
		require.NoError(t, s.r.QueryRowContext(ctx, "SELECT lease_owner IS NOT NULL, leases FROM task_queue WHERE kind = 'probe' AND infohash = ?", h[:]).Scan(&leased, &leases))
		require.False(t, leased, "the task is free")
		require.Zero(t, leases, "its count unchanged")
	})
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

func TestJournalPeersWanted(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	q, err := NewTasks(ctx, s.db, TaskOptions{})
	require.NoError(t, err)
	a := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	b := insertProbeable(t, s, 2, "Heat.1995.1080p", 5, "Heat.1995.1080p.mkv")
	wanted := func(h Hash) []time.Time {
		t.Helper()
		got, err := s.Journal(ctx, h, 1000)
		require.NoError(t, err)
		var out []time.Time
		for _, e := range got {
			if e.Kind == JournalPeersWanted {
				require.Equal(t, "request", e.Outcome)
				require.Empty(t, e.Detail)
				out = append(out, e.At)
			}
		}
		return out
	}

	_, err = s.WantPeers(ctx, []Hash{a, hash(99)}, t0)
	require.NoError(t, err)
	require.Equal(t, []time.Time{t0}, wanted(a))
	require.Empty(t, entries(t, s, hash(99)), "nor one of a release not stored")
	_, err = s.WantPeers(ctx, []Hash{a, b}, t0.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, []time.Time{t0}, wanted(a), "not again while the lookup waits")
	require.Equal(t, []time.Time{t0.Add(time.Minute)}, wanted(b))

	_, _, err = q.Lease(ctx, 1, 2, t0.Add(2*time.Minute), time.Minute)
	require.NoError(t, err)
	_, err = s.WantPeers(ctx, []Hash{a}, t0.Add(3*time.Minute))
	require.NoError(t, err)
	require.Equal(t, []time.Time{t0}, wanted(a), "nor while it runs")
	require.NoError(t, q.Complete(ctx, TaskPeers, a))
	_, err = s.WantPeers(ctx, []Hash{a}, t0.Add(4*time.Minute))
	require.NoError(t, err)
	require.Equal(t, []time.Time{t0, t0.Add(4 * time.Minute)}, wanted(a), "once it is done, again")
}
