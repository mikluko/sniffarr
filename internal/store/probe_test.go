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

// background returns the background probes at now.
func background(t *testing.T, s *Store, now time.Time) []Hash {
	t.Helper()
	hs, err := s.BackgroundProbes(context.Background(), 10, now)
	require.NoError(t, err)
	return hs
}

// due reports whether h is due a probe at now.
func due(t *testing.T, s *Store, h Hash, now time.Time) bool {
	t.Helper()
	ok, err := s.ProbeDue(context.Background(), h, now)
	require.NoError(t, err)
	return ok
}

// probeFailed records a timed-out probe of h at the time.
func probeFailed(t *testing.T, s *Store, h Hash, at time.Time, attempts int) bool {
	t.Helper()
	final, _, err := s.ProbeFailed(context.Background(), h, ProbeTimeout, 0, 1, at, attempts, time.Hour)
	require.NoError(t, err)
	return final
}

func TestBackgroundProbes(t *testing.T) {
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

	require.Equal(t, []Hash{many, few}, background(t, s, t0), "most peers first; no peers, no readable file or no video kind is left out")
	hs, err := s.BackgroundProbes(ctx, 1, t0)
	require.NoError(t, err)
	require.Equal(t, []Hash{many}, hs)

	probeFailed(t, s, many, t0, 3)
	require.Equal(t, []Hash{few}, background(t, s, t0), "a failed probe waits out its backoff")
	setPeersWeek(t, s, hash(3), 1)
	require.Equal(t, []Hash{few, hash(3), many}, background(t, s, t0.Add(2*time.Hour)), "a release never probed before one due again, whatever their peers")
}

func TestProbeFailedRetries(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	h := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	require.True(t, due(t, s, h, t0), "never probed")

	final, retry, err := s.ProbeFailed(ctx, h, ProbeTimeout, 0, 1, t0, 2, time.Hour)
	require.NoError(t, err)
	require.False(t, final)
	require.Equal(t, t0.Add(time.Hour), retry)
	require.False(t, due(t, s, h, t0.Add(59*time.Minute)), "waits out the backoff")
	require.True(t, due(t, s, h, t0.Add(time.Hour)))

	final, retry, err = s.ProbeFailed(ctx, h, ProbeNoPeers, 0, 1, t0.Add(time.Hour), 2, time.Hour)
	require.NoError(t, err)
	require.True(t, final, "the attempts ran out")
	require.Equal(t, t0.Add(3*time.Hour), retry, "the backoff doubles")
	require.False(t, due(t, s, h, t0.Add(1000*time.Hour)), "no more peers than at the last probe")

	setPeersWeek(t, s, h, 9)
	require.False(t, due(t, s, h, t0.Add(1000*time.Hour)), "fewer than twice the peers of the last probe")
	setPeersWeek(t, s, h, 10)
	require.False(t, due(t, s, h, t0.Add(2*time.Hour)), "twice the peers, but the backoff, 2h after the second failure, is not out")
	require.True(t, due(t, s, h, t0.Add(3*time.Hour)), "twice the peers once the backoff is out")

	require.True(t, probeFailed(t, s, h, t0.Add(3*time.Hour), 2))
	require.False(t, due(t, s, h, t0.Add(1000*time.Hour)), "the peers at the last probe are the new mark")
	setPeersWeek(t, s, h, 20)
	require.False(t, due(t, s, h, t0.Add(4*time.Hour)), "the backoff stays at its last doubling")
	require.True(t, due(t, s, h, t0.Add(5*time.Hour)))

	one := insertProbeable(t, s, 2, "Brazil.1985.1080p", 1, "Brazil.1985.1080p.mkv")
	probeFailed(t, s, one, t0, 1)
	setPeersWeek(t, s, one, 2)
	require.False(t, due(t, s, one, t0.Add(1000*time.Hour)), "one peer to two is twice, but not two more")
	setPeersWeek(t, s, one, 3)
	require.True(t, due(t, s, one, t0.Add(1000*time.Hour)))
}

func TestProbeDoneOnce(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	h := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	require.NoError(t, s.ProbeDone(ctx, h, Probe{Outcome: ProbeUnknown}, t0))
	require.False(t, due(t, s, h, t0.Add(1000*time.Hour)))
	require.Empty(t, background(t, s, t0.Add(1000*time.Hour)))
	orders, err := s.WantProbe(ctx, []Hash{h}, ProbeWant{T: "search", Query: "alien"}, t0)
	require.NoError(t, err)
	require.Empty(t, orders, "a search does not repeat a probe")
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
	requireAudio(t, mustGet(t, s, und), 0.4, LayerClassifier)

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
		{backgroundProbeQuery, "SEARCH t USING INDEX torrent_releases_peers_week (peers_week>?)", append(probeArgs(), 1)},
		{retryProbeQuery, "SCAN p", append(append([]any{0}, probeArgs()...), 1)},
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

func TestWantProbe(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	done := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	spent := insertProbeable(t, s, 2, "Heat.1995.1080p", 5, "Heat.1995.1080p.mkv")
	fresh := insertProbeable(t, s, 4, "Brazil.1985.1080p", 5, "Brazil.1985.1080p.mkv")
	backoff := insertProbeable(t, s, 5, "Fargo.1996.1080p", 5, "Fargo.1996.1080p.mkv")
	require.NoError(t, s.ProbeDone(ctx, done, Probe{Outcome: ProbeUnknown}, t0))
	require.True(t, probeFailed(t, s, spent, t0, 1))
	require.False(t, probeFailed(t, s, backoff, t0, 3))

	orders, err := s.WantProbe(ctx, []Hash{fresh, done, spent, backoff, hash(99)}, ProbeWant{T: "movie", Query: "x"}, t0.Add(time.Minute))
	require.NoError(t, err)
	require.ElementsMatch(t, []ProbeOrder{{Hash: fresh}, {Hash: backoff, At: t0.Add(time.Hour)}}, orders,
		"neither a done probe nor one whose attempts ran out; a failed one due once its backoff is out")

	orders, err = s.WantProbe(ctx, []Hash{backoff}, ProbeWant{T: "movie", Query: "x"}, t0.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, []ProbeOrder{{Hash: backoff}}, orders, "due at once when its backoff is out")
}

func TestWantProbeReadableOnly(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	readable := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	nobody := insertProbeable(t, s, 3, "Nobody.2003.1080p", 0, "Nobody.2003.1080p.mkv")
	avi := insertProbeable(t, s, 2, "Heat.1995.DVDRip", 5, "Heat.1995.DVDRip.avi")
	disc := insertProbeable(t, s, 4, "Disc.2004.1080p", 5, "Disc/BDMV/STREAM/00001.m2ts")
	music := torrent(5, "Album 2005", "Album/clip.mkv")
	music.Kind, music.KindLayer = "music", LayerRules
	_, _, err := s.Insert(ctx, music)
	require.NoError(t, err)
	setPeersWeek(t, s, music.Hash, 5)

	orders, err := s.WantProbe(ctx, []Hash{readable, nobody, avi, disc, music.Hash}, ProbeWant{T: "movie"}, t0)
	require.NoError(t, err)
	require.Equal(t, []ProbeOrder{{Hash: readable}}, orders, "a release without peers, or a file or kind a probe cannot read, is not ordered")
}

func TestRequestProbe(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	readable := insertProbeable(t, s, 1, "Alien.1979.1080p", 0, "Alien.1979.1080p.mkv")
	avi := insertProbeable(t, s, 2, "Heat.1995.DVDRip", 5, "Heat.1995.DVDRip.avi")
	disc := insertProbeable(t, s, 4, "Disc.2004.1080p", 5, "Disc/BDMV/STREAM/00001.m2ts")
	require.NoError(t, s.ProbeDone(ctx, readable, Probe{Outcome: ProbeUnknown}, t0))

	got, stored, err := s.RequestProbe(ctx, []Hash{readable, avi, disc, hash(99)}, t0)
	require.NoError(t, err)
	require.Equal(t, []Hash{readable}, got, "only a release a probe can read, whatever its peers and its last probe")
	require.Equal(t, 3, stored)

	got, stored, err = s.RequestProbe(ctx, []Hash{avi}, t0)
	require.NoError(t, err)
	require.Empty(t, got)
	require.Equal(t, 1, stored)
}

func TestProbeDoneResetsFailures(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	h := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	for i := range 2 {
		final, _, err := s.ProbeFailed(ctx, h, ProbeTimeout, 0, 1, t0.Add(time.Duration(i)*time.Hour), 3, time.Hour)
		require.NoError(t, err)
		require.False(t, final)
	}
	require.NoError(t, s.ProbeDone(ctx, h, Probe{Outcome: ProbeUnknown}, t0.Add(2*time.Hour)))
	final, _, err := s.ProbeFailed(ctx, h, ProbeTimeout, 0, 1, t0.Add(3*time.Hour), 3, time.Hour)
	require.NoError(t, err)
	require.False(t, final, "the failures before a read do not count")
}

func TestWidthQuality(t *testing.T) {
	for _, tc := range []struct {
		width int
		want  string
	}{
		{0, ""},
		{720, "sd"},
		{1199, "sd"},
		{1200, "720p"},
		{1280, "720p"},
		{1800, "1080p"},
		{1920, "1080p"},
		{3200, "2160p"},
		{3840, "2160p"},
	} {
		require.Equal(t, tc.want, widthQuality(tc.width), tc.width)
	}
}

func TestVideoTrack(t *testing.T) {
	audio := Track{Kind: "audio", Codec: "A_AC3", Default: true}
	cover := Track{Kind: "video", Codec: "jpeg", Width: 600, Default: true}
	first := Track{Kind: "video", Codec: "hvc1", Width: 1280}
	second := Track{Kind: "video", Codec: "hvc1", Width: 1920, Default: true}
	for _, tc := range []struct {
		name   string
		tracks []Track
		want   Track
		ok     bool
	}{
		{"none", []Track{audio}, Track{}, false},
		{"a picture alone", []Track{cover}, Track{}, false},
		{"the first without a default", []Track{audio, first, {Kind: "video", Codec: "avc1", Width: 720}}, first, true},
		{"the default", []Track{cover, first, second}, second, true},
	} {
		got, ok := videoTrack(tc.tracks)
		require.Equal(t, tc.ok, ok, tc.name)
		require.Equal(t, tc.want, got, tc.name)
	}
}

// insertQuality stores a release of the name with a video file and the
// quality its name gives.
func insertQuality(t *testing.T, s *Store, b byte, name, quality string) Hash {
	t.Helper()
	tr := torrent(b, name, name+".mkv")
	tr.Quality = quality
	_, _, err := s.Insert(context.Background(), tr)
	require.NoError(t, err)
	return tr.Hash
}

// video returns the quality, HDR and codec of the stored release of h.
func video(t *testing.T, s *Store, h Hash) []string {
	t.Helper()
	r := mustGet(t, s, h)
	return []string{r.Quality, r.HDR, r.Codec}
}

func TestProbeDoneVideo(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	cropped := insertQuality(t, s, 1, "Dune.2021.2160p", "2160p")
	named := insertQuality(t, s, 2, "Heat.1995.1080p", "1080p")
	unprobed := insertQuality(t, s, 3, "Alien.1979.1080p", "1080p")
	widthless := insertQuality(t, s, 4, "Brazil.1985.1080p", "1080p")

	require.NoError(t, s.ProbeDone(ctx, cropped, Probe{Outcome: ProbeTracks, Tracks: []Track{
		{Kind: "video", Codec: "V_MPEGH/ISO/HEVC", Width: 3840, Height: 1600, HDR: "DV", Default: true}}}, t0))
	require.NoError(t, s.ProbeDone(ctx, named, Probe{Outcome: ProbeTracks, Tracks: []Track{
		{Kind: "video", Codec: "avc1", Width: 1280, Height: 534}}}, t0))
	require.NoError(t, s.ProbeDone(ctx, widthless, Probe{Outcome: ProbeTracks, Tracks: []Track{{Kind: "video", Codec: "V_MS/VFW/FOURCC"}}}, t0))

	require.Equal(t, []string{"2160p", "DV", "V_MPEGH/ISO/HEVC"}, video(t, s, cropped), "a cropped frame keeps its class")
	require.Equal(t, []string{"720p", "", "avc1"}, video(t, s, named), "the probe's quality wins over the name's")
	require.Equal(t, []string{"1080p", "", ""}, video(t, s, unprobed))
	require.Equal(t, []string{"1080p", "", "V_MS/VFW/FOURCC"}, video(t, s, widthless), "a track without a width leaves the name's quality")
	require.True(t, mustGet(t, s, cropped).Tracks[0].Default)

	got, err := s.Journal(ctx, named, 10)
	require.NoError(t, err)
	require.Contains(t, got[len(got)-1].Detail, `"quality":"720p"`)

	rs, _, err := s.Page(ctx, Browse{Quality: "720p", Limit: 10})
	require.NoError(t, err)
	require.Len(t, rs, 1)
	require.Equal(t, named, rs[0].Hash, "the quality filter takes the probe's")

	require.NoError(t, s.ProbeDone(ctx, named, Probe{Outcome: ProbeUnknown}, t0.Add(time.Hour)))
	require.Equal(t, []string{"1080p", "", ""}, video(t, s, named), "a probe that read no tracks gives back the name's quality")
}
