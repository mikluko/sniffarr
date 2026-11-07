package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/rules"
)

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

// entry returns the one entry of the hash of the kind and outcome.
func entry(t *testing.T, s *Store, h Hash, kind, outcome string) JournalEntry {
	t.Helper()
	got, err := s.Journal(context.Background(), h, 1000)
	require.NoError(t, err)
	var found []JournalEntry
	for _, e := range got {
		if e.Kind == kind && e.Outcome == outcome {
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
		return journal(ctx, tx, hash(2), t0, JournalFetched, "duplicate", nil)
	}))
	got, err := s.Journal(ctx, hash(1), 10)
	require.NoError(t, err)
	require.Equal(t, []JournalEntry{
		{Hash: hash(1), At: t0, Kind: JournalSighted, Outcome: "queued", Detail: `{"discovery":"announce"}`},
		{Hash: hash(1), At: t0.Add(time.Second), Kind: JournalSighted, Outcome: "promoted", Detail: `{"discovery":"announce"}`},
		{Hash: hash(1), At: t0.Add(2 * time.Second), Kind: JournalSighted, Outcome: "revived", Detail: `{"discovery":"announce"}`},
	}, got, "oldest first, zero values left out of the detail")
	got, err = s.Journal(ctx, hash(1), 2)
	require.NoError(t, err)
	require.Equal(t, []string{"promoted", "revived"}, []string{got[0].Outcome, got[1].Outcome}, "the newest ones within the limit")
	got, err = s.Journal(ctx, hash(2), 10)
	require.NoError(t, err)
	require.Equal(t, []JournalEntry{{Hash: hash(2), At: t0, Kind: JournalFetched, Outcome: "duplicate"}}, got, "no detail reads as empty")
	got, err = s.Journal(ctx, hash(3), 10)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestSweepJournal(t *testing.T) {
	ctx := context.Background()
	s := open(t)
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
}

func TestJournalPlans(t *testing.T) {
	s := open(t)
	for query, want := range map[string]string{
		journalSweep: "INDEX torrent_journal_at (at<?)",
		"SELECT at, kind, outcome, detail FROM torrent_journal WHERE infohash = ? ORDER BY id DESC LIMIT ?": "INDEX torrent_journal_infohash (infohash=?)",
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
		}, "EXPLAIN QUERY PLAN "+query, 0, 1))
		require.Len(t, plan, 1, query)
		require.Contains(t, plan[0], want)
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
	work := matchTo(t, s, "y:heat 1995", TypeMovie, 949, "Heat")
	_, _, err = s.Insert(ctx, titled(4, "Heat.1995.2160p", "y:heat 1995"))
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"Heat.1995.2160p","size":1073741824,"discovery":"sample","kind":"movie","kind_layer":"rules","work":`+strconv.FormatInt(work, 10)+`}`,
		entry(t, s, hash(4), JournalFetched, "stored").Detail, "the work its title key is matched to")
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
	require.JSONEq(t, `{"owner":7,"attempt":1,"lease_until":`+unix(t0.Add(time.Minute))+`}`, entry(t, s, hash(1), JournalLeased, "fetch").Detail)
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
	require.JSONEq(t, `{"owner":7,"attempt":1,"retry_at":`+unix(first.Add(10*time.Minute))+`}`, got[2].Detail)
	require.JSONEq(t, `{"owner":7,"attempt":2,"lease_until":`+unix(first.Add(11*time.Minute))+`}`, got[3].Detail, "the attempt number carries over the retry")
	require.JSONEq(t, `{"owner":7,"attempt":3,"dead_until":`+unix(at.Add(24*time.Hour))+`}`, got[6].Detail)
	require.Empty(t, entries(t, s, hash(2)), "nor its failures")

	enqueueAt(t, q, at, Announce, 3)
	require.Len(t, lease(t, q, owner, 1, at), 1)
	require.NoError(t, q.NoPeers(ctx, hash(3), owner, at))
	require.JSONEq(t, `{"owner":7,"dead_until":`+unix(at.Add(time.Hour))+`}`, entry(t, s, hash(3), JournalFetchFailed, "no_peers").Detail)
	enqueueAt(t, q, at, Announce, 3)
	require.JSONEq(t, `{"discovery":"announce"}`, entry(t, s, hash(3), JournalSighted, "revived").Detail)
	require.Equal(t, []string{"sighted/queued", "leased/fetch", "fetch_failed/no_peers", "sighted/revived"}, entries(t, s, hash(3)), "a revival is not queued too")

	enqueueAt(t, q, at, Sample, 4)
	enqueueAt(t, q, at, Feed, 4)
	require.JSONEq(t, `{"discovery":"feed","from":"sample","to":"feed"}`, entry(t, s, hash(4), JournalSighted, "promoted").Detail)
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
		entry(t, s, hash(1), JournalClassified, LayerJev).Detail)
	for _, h := range []Hash{hash(2), hash(3)} {
		require.JSONEq(t, `{"kind":"movie","from":`+strconv.FormatInt(one.ID, 10)+`}`, entry(t, s, h, JournalClassified, LayerReuse).Detail)
	}
	classify(t, s, mustGet(t, s, hash(2)).ID, Classification{Source: "remux", Trigger: TriggerWorker})
	require.JSONEq(t, `{"source":"remux","trigger":"worker"}`, entry(t, s, hash(2), JournalClassified, LayerJev).Detail, "a kind known already is not asked about")

	require.NoError(t, s.ClassifyFailed(ctx, mustGet(t, s, hash(3)).ID, "malformed", t0))
	require.JSONEq(t, `{"error":"malformed"}`, entry(t, s, hash(3), JournalClassified, "failed").Detail)
	require.NoError(t, s.ClassifyFailed(ctx, 99, "malformed", t0), "an unknown ID is nothing")
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
	require.JSONEq(t, `{"kind":"movie","copied":51}`, entry(t, s, hash(1), JournalClassified, LayerJev).Detail)
}

func TestJournalMatched(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for _, x := range []Torrent{titled(1, "Alien.1979.1080p", "y:alien 1979"), titled(2, "Alien.1979.2160p", "y:alien 1979"), titled(3, "Heat.1995.1080p", "y:heat 1995")} {
		_, _, err := s.Insert(ctx, x)
		require.NoError(t, err)
	}
	work, _, err := s.RecordMatch(ctx, "y:alien 1979", Found{Hit: &Hit{Type: TypeMovie, TMDBID: 348, Title: "Alien"}, Confidence: 0.8, Layer: LayerJev, Trigger: TriggerWorker}, t0)
	require.NoError(t, err)
	for _, h := range []Hash{hash(1), hash(2)} {
		require.JSONEq(t, `{"work":`+strconv.FormatInt(work, 10)+`,"tmdb_id":348,"type":"movie","confidence":0.8,"trigger":"worker"}`, entry(t, s, h, JournalMatched, LayerJev).Detail)
	}
	require.Equal(t, []string{"fetched/stored"}, entries(t, s, hash(3)), "another key")

	other, _, err := s.RecordMatch(ctx, "y:alien 1979", Found{Hit: &Hit{Type: TypeMovie, TMDBID: 8077, Title: "Alien³"}, Confidence: 1, Layer: LayerTMDB, Trigger: TriggerSearch}, t0.Add(time.Hour))
	require.NoError(t, err)
	require.JSONEq(t, `{"work":`+strconv.FormatInt(other, 10)+`,"tmdb_id":8077,"type":"movie","confidence":1,"previous":`+strconv.FormatInt(work, 10)+`,"trigger":"search"}`,
		entry(t, s, hash(1), JournalMatched, LayerTMDB).Detail, "a rematch names the work before")
	_, _, err = s.RecordMatch(ctx, "y:alien 1979", Found{Hit: &Hit{Type: TypeMovie, TMDBID: 8077}, Confidence: 1, Layer: LayerTMDB}, t0.Add(2*time.Hour))
	require.NoError(t, err)
	require.Equal(t, []string{"fetched/stored", "matched/jev", "matched/tmdb"}, entries(t, s, hash(1)), "the same match again changes no release")

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
	e := grab(1, "abc", t0)
	e.Source, e.Season, e.Episode = "webhook", 0, 0
	rec, err := s.RecordArr(ctx, e)
	require.NoError(t, err)
	require.True(t, rec.New)
	w := strconv.FormatInt(rec.Work, 10)
	require.JSONEq(t, `{"app":"radarr","source":"webhook","indexer":"tracker","title":"Alien.1979.1080p.BluRay","size":1073741824,"work":`+w+`}`,
		entry(t, s, hash(1), JournalArr, EventGrab).Detail)
	require.JSONEq(t, `{"work":`+w+`,"tmdb_id":348,"type":"movie","trigger":"arr"}`, entry(t, s, hash(1), JournalMatched, LayerArr).Detail)
	require.JSONEq(t, `{"work":`+w+`,"tmdb_id":348,"type":"movie","trigger":"arr"}`, entry(t, s, hash(2), JournalMatched, LayerArr).Detail, "the key's other release follows")
	_, err = s.RecordArr(ctx, e)
	require.NoError(t, err)
	require.Equal(t, []string{"fetched/stored", "arr/grab", "matched/arr"}, entries(t, s, hash(1)), "a duplicate event writes nothing")

	pending := grab(3, "def", t0)
	pending.Source = "history"
	_, err = s.RecordArr(ctx, pending)
	require.NoError(t, err)
	require.Equal(t, []string{"arr/grab"}, entries(t, s, hash(3)), "the torrent is not stored yet")
	_, _, err = s.Insert(ctx, titled(3, "Alien.1979.720p", "y:alien 1979"))
	require.NoError(t, err)
	require.JSONEq(t, `{"work":`+w+`,"tmdb_id":348,"type":"movie","trigger":"arr","pending":true}`, entry(t, s, hash(3), JournalMatched, LayerArr).Detail)
	require.Equal(t, []string{"arr/grab", "fetched/stored", "matched/arr"}, entries(t, s, hash(3)))

	for _, x := range []Torrent{titled(5, "Aliens.1986.1080p", "y:aliens 1986"), titled(6, "Aliens.1986.720p", "y:aliens 1986")} {
		_, _, err := s.Insert(ctx, x)
		require.NoError(t, err)
	}
	_, err = s.RecordArr(ctx, grab(7, "ghi", t0))
	require.NoError(t, err)
	_, _, err = s.Insert(ctx, titled(7, "Aliens.1986.2160p", "y:aliens 1986"))
	require.NoError(t, err)
	require.JSONEq(t, `{"work":`+w+`,"tmdb_id":348,"type":"movie","trigger":"arr","pending":true}`, entry(t, s, hash(7), JournalMatched, LayerArr).Detail)
	require.JSONEq(t, `{"work":`+w+`,"tmdb_id":348,"type":"movie","trigger":"arr"}`, entry(t, s, hash(5), JournalMatched, LayerArr).Detail, "the sibling's row is not pending")

	hashless := grab(8, "jkl", t0)
	hashless.Hash = nil
	rec, err = s.RecordArr(ctx, hashless)
	require.NoError(t, err)
	require.True(t, rec.New)
	rec, err = s.RecordArr(ctx, grab(8, "jkl", t0.Add(time.Minute)))
	require.NoError(t, err)
	require.False(t, rec.New)
	_, err = s.RecordArr(ctx, grab(8, "jkl", t0.Add(2*time.Minute)))
	require.NoError(t, err)
	require.Equal(t, []string{"arr/grab"}, entries(t, s, hash(8)), "journaled once the event has its hash, and once")
	require.Equal(t, t0.Add(time.Minute), entry(t, s, hash(8), JournalArr, EventGrab).At)
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
	require.JSONEq(t, `{"attempt":2,"retry_at":`+unix(t0.Add(3*time.Hour))+`,"final":true,"peers_week":5}`, entry(t, s, h, JournalProbed, ProbeNoPeers).Detail)

	require.NoError(t, s.ProbeDone(ctx, h, Probe{Outcome: ProbeTracks, Path: "Alien.1979.1080p.mkv", Format: "matroska", Downloaded: 2 << 20, Tracks: []Track{
		{Kind: "video", Codec: "V_MPEG4/ISO/AVC"}, {Kind: "audio", Language: "rus", Codec: "A_AC3"}, {Kind: "subtitle", Language: "eng", Codec: "S_TEXT/UTF8"},
	}}, t0.Add(4*time.Hour)))
	require.JSONEq(t, `{"path":"Alien.1979.1080p.mkv","format":"matroska","downloaded":2097152,"tracks":["video::V_MPEG4/ISO/AVC","audio:rus:A_AC3","subtitle:eng:S_TEXT/UTF8"],"audio":{"ru":1}}`,
		entry(t, s, h, JournalProbed, ProbeTracks).Detail)
	require.NoError(t, s.ProbeDone(ctx, h, Probe{Outcome: ProbeUnknown, Downloaded: 3}, t0.Add(5*time.Hour)))
	require.JSONEq(t, `{"downloaded":3}`, entry(t, s, h, JournalProbed, ProbeUnknown).Detail)
	require.Equal(t, []string{"fetched/stored", "probe_wanted/search", "probed/timeout", "probed/no_peers", "probed/tracks", "probed/unknown"}, entries(t, s, h))

	require.NoError(t, s.JournalProbeLease(ctx, h, t0, "retry", Detail("attempt", 3, "max_bytes", 64<<20, "used", 0, "reserved", 128<<20)))
	require.JSONEq(t, `{"attempt":3,"max_bytes":67108864,"reserved":134217728}`, entry(t, s, h, JournalProbeLeased, "retry").Detail)
	require.NoError(t, s.JournalFeed(ctx, h, t0, "queued", Detail("source", "rutracker", "title", "Alien", "published_at", t0)))
	require.JSONEq(t, `{"source":"rutracker","title":"Alien","published_at":`+unix(t0)+`}`, entry(t, s, h, JournalFeed, "queued").Detail)

	require.NoError(t, s.WantProbe(ctx, []Hash{h}, ProbeWant{T: "movie", Query: "alien"}, t0.Add(23*time.Hour)))
	require.Len(t, entries(t, s, h), 8, "wanted again within a day is not journaled")
	require.NoError(t, s.WantProbe(ctx, []Hash{h}, ProbeWant{T: "movie", Query: "alien"}, t0.Add(25*time.Hour)))
	got, err := s.Journal(ctx, h, 1)
	require.NoError(t, err)
	require.Equal(t, "probe_wanted/search", got[0].Kind+"/"+got[0].Outcome, "a day after the last it is")
	n, err := s.RequestProbe(ctx, []Hash{h, hash(9)}, t0.Add(26*time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, JournalEntry{Hash: h, At: t0.Add(26 * time.Hour), Kind: JournalProbeWanted, Outcome: "request"}, entry(t, s, h, JournalProbeWanted, "request"))
}

func TestJournalDeleted(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	_, _, err := s.Insert(ctx, titled(1, "Alien.1979.1080p", "y:alien 1979"))
	require.NoError(t, err)
	work := matchTo(t, s, "y:alien 1979", TypeMovie, 348, "Alien")
	_, _, err = s.Insert(ctx, torrent(2, "Unknown.2024"))
	require.NoError(t, err)
	now := t0.Add(48 * time.Hour)
	sw, err := s.Sweep(ctx, t0.Add(36*time.Hour), t0.Add(time.Hour), now, 10)
	require.NoError(t, err)
	require.Equal(t, Swept{Idle: 1, Expired: 1}, sw)
	require.JSONEq(t, `{"name":"Alien.1979.1080p","last_seen":`+unix(t0)+`,"work":`+strconv.FormatInt(work, 10)+`}`, entry(t, s, hash(1), JournalDeleted, "expired").Detail)
	require.JSONEq(t, `{"name":"Unknown.2024","last_seen":`+unix(t0)+`}`, entry(t, s, hash(2), JournalDeleted, "idle").Detail)
	require.Equal(t, now, entry(t, s, hash(2), JournalDeleted, "idle").At)
	require.Equal(t, []string{"fetched/stored", "matched/tmdb", "deleted/expired"}, entries(t, s, hash(1)), "the trail outlives the torrent")
}
