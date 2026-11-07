package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func setPeersWeek(t *testing.T, s *Store, h Hash, n int64) {
	t.Helper()
	_, err := s.w.ExecContext(context.Background(), "UPDATE torrent_releases SET peers_week = ? WHERE infohash = ?", n, h[:])
	require.NoError(t, err)
}

func insertProbeable(t *testing.T, s *Store, b byte, name string, peers int64, paths ...string) Hash {
	t.Helper()
	tr := torrent(b, name, paths...)
	tr.Kind, tr.KindLayer = "movie", LayerRules
	if b == markedByte {
		tr.Audio = []Audio{{Language: "ru", Marker: true, Mark: "Rus", Probability: new(1.0), Layer: LayerRules}}
	}
	inserted, _, err := s.Insert(context.Background(), tr)
	require.NoError(t, err)
	require.True(t, inserted)
	setPeersWeek(t, s, tr.Hash, peers)
	return tr.Hash
}

// markedByte is the hash byte insertProbeable gives a Russian marker.
const markedByte = 3

// nextProbe returns what LeaseProbe would lease at now, leaving the store as
// it was.
func nextProbe(t *testing.T, s *Store, now time.Time, background bool) (ProbeCandidate, bool) {
	t.Helper()
	tx, err := s.w.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, tx.Rollback()) }()
	c, ok, err := leaseProbe(context.Background(), tx, 1, now, time.Minute, background)
	require.NoError(t, err)
	return c, ok
}

// leaseNext leases the next probe at now for 1000 hours.
func leaseNext(t *testing.T, s *Store, now time.Time, background bool) (ProbeCandidate, bool) {
	t.Helper()
	c, ok, err := s.LeaseProbe(context.Background(), 1, now, 1000*time.Hour, background)
	require.NoError(t, err)
	return c, ok
}

func TestNextProbeOrder(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	few := insertProbeable(t, s, 1, "Few.2001.1080p", 3, "Few.2001.1080p.mkv")
	many := insertProbeable(t, s, 2, "Many.2002.1080p", 50, "Many/Many.2002.1080p.MP4")
	insertProbeable(t, s, 3, "Nobody.2003.1080p", 0, "Nobody.2003.1080p.mkv")
	insertProbeable(t, s, 4, "Disc.2004.1080p", 90, "Disc/BDMV/STREAM/00001.m2ts")
	music := torrent(5, "Album 2005 FLAC", "Album/01.flac")
	music.Kind, music.KindLayer = "music", LayerRules
	_, _, err := s.Insert(ctx, music)
	require.NoError(t, err)
	setPeersWeek(t, s, music.Hash, 100)

	c, ok := nextProbe(t, s, t0, true)
	require.True(t, ok)
	require.Equal(t, many, c.Hash, "most peers first")
	require.False(t, c.Wanted)

	require.NoError(t, s.WantProbe(ctx, []Hash{few, hash(99)}, t0))
	c, ok = leaseNext(t, s, t0, true)
	require.True(t, ok)
	require.Equal(t, few, c.Hash, "a searched release before the rest")
	require.True(t, c.Wanted)
	n, err := s.ProbeLeases(ctx, t0)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	_, ok = nextProbe(t, s, t0, false)
	require.False(t, ok, "only searched releases without background, and the leased one is left out")

	c, ok = nextProbe(t, s, t0, true)
	require.True(t, ok)
	require.Equal(t, many, c.Hash)

	_, err = s.ProbeFailed(ctx, many, t0, 3, time.Hour)
	require.NoError(t, err)
	setPeersWeek(t, s, hash(3), 1)
	c, ok = nextProbe(t, s, t0.Add(2*time.Hour), true)
	require.True(t, ok)
	require.Equal(t, hash(3), c.Hash, "a release never probed before one due again, whatever their peers")
}

func TestProbeFailedRetries(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	h := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")

	final, err := s.ProbeFailed(ctx, h, t0, 2, time.Hour)
	require.NoError(t, err)
	require.False(t, final)
	_, ok := nextProbe(t, s, t0.Add(59*time.Minute), true)
	require.False(t, ok, "waits out the backoff")
	_, ok = nextProbe(t, s, t0.Add(time.Hour), true)
	require.True(t, ok)

	final, err = s.ProbeFailed(ctx, h, t0.Add(time.Hour), 2, time.Hour)
	require.NoError(t, err)
	require.True(t, final, "the attempts ran out")
	_, ok = nextProbe(t, s, t0.Add(1000*time.Hour), true)
	require.False(t, ok, "no more peers than at the last probe")

	setPeersWeek(t, s, h, 9)
	_, ok = nextProbe(t, s, t0.Add(1000*time.Hour), true)
	require.False(t, ok, "fewer than twice the peers of the last probe")
	setPeersWeek(t, s, h, 10)
	_, ok = nextProbe(t, s, t0.Add(2*time.Hour), true)
	require.False(t, ok, "twice the peers, but the backoff, 2h after the second failure, is not out")
	_, ok = nextProbe(t, s, t0.Add(3*time.Hour), true)
	require.True(t, ok, "twice the peers once the backoff is out")

	final, err = s.ProbeFailed(ctx, h, t0.Add(3*time.Hour), 2, time.Hour)
	require.NoError(t, err)
	require.True(t, final)
	_, ok = nextProbe(t, s, t0.Add(1000*time.Hour), true)
	require.False(t, ok, "the peers at the last probe are the new mark")
	setPeersWeek(t, s, h, 20)
	_, ok = nextProbe(t, s, t0.Add(4*time.Hour), true)
	require.False(t, ok, "the backoff stays at its last doubling")
	_, ok = nextProbe(t, s, t0.Add(5*time.Hour), true)
	require.True(t, ok)

	one := insertProbeable(t, s, 2, "Brazil.1985.1080p", 1, "Brazil.1985.1080p.mkv")
	_, err = s.ProbeFailed(ctx, one, t0, 1, time.Hour)
	require.NoError(t, err)
	setPeersWeek(t, s, one, 2)
	c, ok := leaseNext(t, s, t0.Add(1000*time.Hour), true)
	require.True(t, ok)
	require.Equal(t, h, c.Hash)
	_, ok = nextProbe(t, s, t0.Add(1000*time.Hour), true)
	require.False(t, ok, "one peer to two is twice, but not two more")
	setPeersWeek(t, s, one, 3)
	c, ok = nextProbe(t, s, t0.Add(1000*time.Hour), true)
	require.True(t, ok)
	require.Equal(t, one, c.Hash)
}

func TestProbeDoneOnce(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	h := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	require.NoError(t, s.ProbeDone(ctx, h, Probe{Outcome: ProbeUnknown}, t0))
	_, ok := nextProbe(t, s, t0.Add(1000*time.Hour), true)
	require.False(t, ok)
	require.NoError(t, s.WantProbe(ctx, []Hash{h}, t0))
	_, ok = nextProbe(t, s, t0.Add(1000*time.Hour), true)
	require.False(t, ok, "a search does not repeat a probe")
}

func TestProbeDoneTracksAndAudio(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	ru := insertProbeable(t, s, 1, "Coraline.2009.2160p.HDR.BDRip.Master5", 5, "Coraline.2009.2160p.HDR.BDRip.Master5.mkv")
	en := insertProbeable(t, s, 2, "Heat.1995.1080p", 5, "Heat.1995.1080p.mkv")
	marked := insertProbeable(t, s, markedByte, "Alien.1979.1080p.Rus", 5, "Alien.1979.1080p.Rus.mkv")
	und := insertProbeable(t, s, 4, "Brazil.1985.1080p", 5, "Brazil.1985.1080p.mkv")
	_, err := s.Classify(ctx, mustGet(t, s, und).ID, Classification{Audio: map[string]float64{"ru": 0.4}}, t0)
	require.NoError(t, err)

	video := Track{Kind: "video", Language: "und", Codec: "V_MPEGH/ISO/HEVC", Width: 3840, Height: 2160, HDR: "DV"}
	require.NoError(t, s.ProbeDone(ctx, ru, Probe{Outcome: ProbeTracks, Path: "Coraline.2009.2160p.HDR.BDRip.Master5.mkv", Format: "matroska", Downloaded: 16 << 20, Tracks: []Track{
		video,
		{Kind: "audio", Language: "rus", Name: "Дубляж", Codec: "A_AC3", Channels: 6},
		{Kind: "audio", Language: "eng", Name: "Original", Codec: "A_DTS", Channels: 6},
	}}, t0))
	require.NoError(t, s.ProbeDone(ctx, en, Probe{Outcome: ProbeTracks, Tracks: []Track{video, {Kind: "audio", Language: "eng", LanguageBCP47: "en-US"}}}, t0))
	require.NoError(t, s.ProbeDone(ctx, marked, Probe{Outcome: ProbeTracks, Tracks: []Track{{Kind: "audio", Language: "eng"}}}, t0))
	require.NoError(t, s.ProbeDone(ctx, und, Probe{Outcome: ProbeTracks, Tracks: []Track{{Kind: "audio", Language: "eng"}, {Kind: "audio"}}}, t0))

	r := mustGet(t, s, ru)
	require.Len(t, r.Tracks, 3)
	require.Equal(t, "Дубляж", r.Tracks[1].Name)
	require.Equal(t, video, r.Tracks[0])
	requireAudio(t, r, 1, LayerProbe)
	requireAudio(t, mustGet(t, s, en), 0, LayerProbe)
	requireAudio(t, mustGet(t, s, marked), 0, LayerProbe)
	requireAudio(t, mustGet(t, s, und), 0.4, LayerJev)

	_, err = s.Classify(ctx, r.ID, Classification{Audio: map[string]float64{"ru": 0.2}}, t0)
	require.NoError(t, err)
	requireAudio(t, mustGet(t, s, ru), 1, LayerProbe)

	rs, err := s.Search(ctx, Query{Text: "coraline", Limit: 10})
	require.NoError(t, err)
	require.Len(t, rs, 1)
	require.Len(t, rs[0].Tracks, 3)
}

func TestProbeLeavesMarkerOfSeparateAudio(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	tr := torrent(1, "Alien.1979.1080p", "Alien.1979.1080p.mkv", "Rus Sound/Alien.1979.Rus.AC3")
	tr.Kind, tr.KindLayer = "movie", LayerRules
	tr.Audio = []Audio{{Language: "ru", Marker: true, Mark: "Rus", Probability: new(1.0), Layer: LayerRules}}
	_, _, err := s.Insert(ctx, tr)
	require.NoError(t, err)
	require.NoError(t, s.ProbeDone(ctx, tr.Hash, Probe{Outcome: ProbeTracks, Tracks: []Track{{Kind: "audio", Language: "eng"}}}, t0))
	requireAudio(t, mustGet(t, s, tr.Hash), 1, LayerRules)
}

func TestProbeTracksMatchLanguageTags(t *testing.T) {
	for _, tc := range []struct {
		track Track
		want  bool
	}{
		{Track{Language: "rus"}, true},
		{Track{Language: "eng", LanguageBCP47: "ru-RU"}, true},
		{Track{Language: "ru"}, true},
		{Track{Language: "ukr"}, false},
		{Track{Language: "und"}, false},
		{Track{}, false},
	} {
		require.Equal(t, tc.want, tc.track.In("ru"), "%+v", tc.track)
	}
	require.True(t, Track{Language: "und"}.Undetermined())
	require.True(t, Track{}.Undetermined())
	require.False(t, Track{LanguageBCP47: "en"}.Undetermined())
}

func TestProbeUsage(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	tr, err := s.ProbeTraffic(ctx, "2026-10-01")
	require.NoError(t, err)
	require.Zero(t, tr)
	require.NoError(t, s.AddProbeTraffic(ctx, "2026-10-01", ProbeTraffic{Rx: 3 << 20}))
	require.NoError(t, s.AddProbeTraffic(ctx, "2026-10-01", ProbeTraffic{Rx: 1 << 20, Tx: 2 << 20}))
	require.NoError(t, s.AddProbeTraffic(ctx, "2026-10-01", ProbeTraffic{Tx: 5 << 20}))
	tr, err = s.ProbeTraffic(ctx, "2026-10-01")
	require.NoError(t, err)
	require.Equal(t, ProbeTraffic{Rx: 4 << 20, Tx: 7 << 20}, tr)
}

func mustGet(t *testing.T, s *Store, h Hash) Release {
	t.Helper()
	r, err := s.Get(context.Background(), h)
	require.NoError(t, err)
	return r
}

func requireAudio(t *testing.T, r Release, p float64, layer string) {
	t.Helper()
	require.Len(t, r.Audio, 1)
	require.NotNil(t, r.Audio[0].Probability, r.Name)
	require.InDelta(t, p, *r.Audio[0].Probability, 1e-9, r.Name)
	require.Equal(t, layer, r.Audio[0].Layer, r.Name)
}

func TestProbePlans(t *testing.T) {
	s := open(t)
	for _, tt := range []struct {
		query, want string
		args        []any
	}{
		{queuedProbeQuery, "SEARCH q USING INDEX sqlite_autoindex_task_queue_1 (kind=?)", append([]any{0, PriorityArr, false}, probeArgs()...)},
		{backgroundProbeQuery, "SEARCH t USING INDEX torrent_releases_peers_week (peers_week>?)", probeArgs()},
	} {
		var plan []string
		require.NoError(t, s.each(context.Background(), func(rows *sql.Rows) error {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				return err
			}
			plan = append(plan, detail)
			return nil
		}, "EXPLAIN QUERY PLAN "+tt.query, tt.args...))
		require.Contains(t, plan, tt.want, tt.query)
	}
}

func TestProbeDoneStudios(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	tr := torrent(1, "Silo.S02.HDRezka.1080p", "Silo.S02E01.mkv")
	tr.Kind, tr.KindLayer = "movie", LayerRules
	tr.Audio = []Audio{{Language: "ru", Marker: true, Mark: "HDRezka", Studios: []string{"HDRezka"}, Probability: new(1.0), Layer: LayerRules}}
	_, _, err := s.Insert(ctx, tr)
	require.NoError(t, err)
	bare := insertProbeable(t, s, 2, "Severance.S02.1080p", 5, "Severance.S02E01.mkv")

	require.NoError(t, s.ProbeDone(ctx, tr.Hash, Probe{Outcome: ProbeTracks, Tracks: []Track{
		{Kind: "audio", Language: "rus", Name: "MVO LostFilm"}, {Kind: "audio", Language: "rus", Name: "HDRezka"}, {Kind: "audio", Language: "eng", Name: "NewStudio"}}}, t0))
	require.Equal(t, []string{"HDRezka", "LostFilm"}, mustGet(t, s, tr.Hash).Audio[0].Studios, "the stored studios first, then those the Russian tracks name, each once; an English track's name is not read")

	require.NoError(t, s.ProbeDone(ctx, bare, Probe{Outcome: ProbeTracks, Tracks: []Track{{Kind: "audio", Language: "rus", Name: "Дубляж"}}}, t0))
	require.Nil(t, mustGet(t, s, bare).Audio[0].Studios, "a track name without a studio")
	require.NoError(t, s.ProbeDone(ctx, bare, Probe{Outcome: ProbeTracks, Tracks: []Track{{Kind: "audio", Language: "rus", Name: "Кубик в Кубе"}}}, t0))
	require.Equal(t, []string{"Кубик в Кубе"}, mustGet(t, s, bare).Audio[0].Studios, "a studio with no mark in the name")
}

func TestRequestProbe(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	done := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	failed := insertProbeable(t, s, 2, "Heat.1995.1080p", 5, "Heat.1995.1080p.mkv")
	wanted := insertProbeable(t, s, 4, "Brazil.1985.1080p", 5, "Brazil.1985.1080p.mkv")
	require.NoError(t, s.ProbeDone(ctx, done, Probe{Outcome: ProbeUnknown}, t0))
	final, err := s.ProbeFailed(ctx, failed, t0, 1, time.Hour)
	require.NoError(t, err)
	require.True(t, final)
	require.NoError(t, s.WantProbe(ctx, []Hash{wanted, done, failed}, t0.Add(2*time.Minute)))
	c, ok := nextProbe(t, s, t0.Add(time.Minute), false)
	require.True(t, ok)
	require.Equal(t, wanted, c.Hash, "a search queues neither a done probe nor one whose attempts ran out")

	n, _, err := s.RequestProbe(ctx, []Hash{done, failed, hash(99)}, t0.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 2, n, "only stored releases")

	c, ok = leaseNext(t, s, t0.Add(time.Minute), false)
	require.True(t, ok)
	require.True(t, c.Wanted)
	require.Equal(t, failed, c.Hash, "a request before a search, whatever the last probe, the newest first")
	c2, ok := leaseNext(t, s, t0.Add(time.Minute), false)
	require.True(t, ok)
	require.Equal(t, done, c2.Hash)

	require.NoError(t, s.ProbeDone(ctx, c2.Hash, Probe{Outcome: ProbeUnknown}, t0.Add(time.Minute)))
	final, err = s.ProbeFailed(ctx, c.Hash, t0.Add(time.Minute), 3, time.Hour)
	require.NoError(t, err)
	require.False(t, final)
	c, ok = leaseNext(t, s, t0.Add(time.Minute), false)
	require.True(t, ok)
	require.Equal(t, wanted, c.Hash, "a recorded probe ends the request")
	_, ok = nextProbe(t, s, t0.Add(time.Minute), false)
	require.False(t, ok, "a failed request waits out its backoff")
	c, ok = nextProbe(t, s, t0.Add(3*time.Hour), false)
	require.True(t, ok)
	require.Equal(t, failed, c.Hash, "a failed request stays searched, due once its backoff is out")
}

// queuedProbes returns the hashes queued for a probe, in hash order.
func queuedProbes(t *testing.T, s *Store) []Hash {
	t.Helper()
	var hs []Hash
	require.NoError(t, s.each(context.Background(), func(rows *sql.Rows) error {
		var h []byte
		if err := rows.Scan(&h); err != nil {
			return err
		}
		hs = append(hs, Hash(h))
		return nil
	}, "SELECT infohash FROM task_queue WHERE kind = 'probe' ORDER BY infohash"))
	return hs
}

func TestWantProbeReadableOnly(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	readable := insertProbeable(t, s, 1, "Alien.1979.1080p", 0, "Alien.1979.1080p.mkv")
	avi := insertProbeable(t, s, 2, "Heat.1995.DVDRip", 5, "Heat.1995.DVDRip.avi")
	disc := insertProbeable(t, s, 4, "Disc.2004.1080p", 5, "Disc/BDMV/STREAM/00001.m2ts")
	music := torrent(5, "Album 2005", "Album/clip.mkv")
	music.Kind, music.KindLayer = "music", LayerRules
	_, _, err := s.Insert(ctx, music)
	require.NoError(t, err)
	setPeersWeek(t, s, music.Hash, 5)

	require.NoError(t, s.WantProbe(ctx, []Hash{readable, avi, disc, music.Hash}, t0))
	require.Equal(t, []Hash{readable}, queuedProbes(t, s), "a release without peers waits; a file or kind a probe cannot read is not queued")
}

func TestRequestProbeReadableOnly(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	readable := insertProbeable(t, s, 1, "Alien.1979.1080p", 0, "Alien.1979.1080p.mkv")
	avi := insertProbeable(t, s, 2, "Heat.1995.DVDRip", 5, "Heat.1995.DVDRip.avi")
	disc := insertProbeable(t, s, 4, "Disc.2004.1080p", 5, "Disc/BDMV/STREAM/00001.m2ts")

	queued, stored, err := s.RequestProbe(ctx, []Hash{readable, avi, disc, hash(99)}, t0)
	require.NoError(t, err)
	require.Equal(t, 1, queued, "only a release a probe can read")
	require.Equal(t, 3, stored)
	require.Equal(t, []Hash{readable}, queuedProbes(t, s))

	queued, stored, err = s.RequestProbe(ctx, []Hash{avi}, t0)
	require.NoError(t, err)
	require.Zero(t, queued)
	require.Equal(t, 1, stored)
}

func TestRequestProbeInFlight(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	h := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	final := insertProbeable(t, s, 2, "Heat.1995.1080p", 5, "Heat.1995.1080p.mkv")
	require.NoError(t, s.WantProbe(ctx, []Hash{h, final}, t0))
	for range 2 {
		_, ok := leaseNext(t, s, t0, false)
		require.True(t, ok)
	}
	_, _, err := s.RequestProbe(ctx, []Hash{h, final}, t0.Add(time.Minute))
	require.NoError(t, err)
	_, err = s.ProbeFailed(ctx, h, t0.Add(2*time.Minute), 3, time.Hour)
	require.NoError(t, err)
	last, err := s.ProbeFailed(ctx, final, t0.Add(2*time.Minute), 1, time.Hour)
	require.NoError(t, err)
	require.True(t, last)

	got := map[Hash]bool{}
	for range 2 {
		c, ok := leaseNext(t, s, t0.Add(3*time.Minute), false)
		require.True(t, ok, "a request made while an attempt was in flight outlives its failure, final or not")
		require.True(t, c.Wanted)
		got[c.Hash] = true
	}
	require.Equal(t, map[Hash]bool{h: true, final: true}, got)

	searched := insertProbeable(t, s, 4, "Brazil.1985.1080p", 5, "Brazil.1985.1080p.mkv")
	_, _, err = s.RequestProbe(ctx, []Hash{searched}, t0.Add(4*time.Minute))
	require.NoError(t, err)
	c, ok := leaseNext(t, s, t0.Add(4*time.Minute), false)
	require.True(t, ok)
	require.Equal(t, searched, c.Hash)
	require.NoError(t, s.WantProbe(ctx, []Hash{searched}, t0.Add(5*time.Minute)))
	_, err = s.ProbeFailed(ctx, searched, t0.Add(6*time.Minute), 3, time.Hour)
	require.NoError(t, err)
	_, ok = nextProbe(t, s, t0.Add(7*time.Minute), false)
	require.False(t, ok, "a search while in flight is no new request")
}

func TestProbeDoneResetsFailures(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	h := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	for i := range 2 {
		final, err := s.ProbeFailed(ctx, h, t0.Add(time.Duration(i)*time.Hour), 3, time.Hour)
		require.NoError(t, err)
		require.False(t, final)
	}
	require.NoError(t, s.ProbeDone(ctx, h, Probe{Outcome: ProbeUnknown}, t0.Add(2*time.Hour)))
	final, err := s.ProbeFailed(ctx, h, t0.Add(3*time.Hour), 3, time.Hour)
	require.NoError(t, err)
	require.False(t, final, "the failures before a read do not count")
}
