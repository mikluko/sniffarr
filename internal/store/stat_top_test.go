package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/names"
)

// named returns a torrent of the name with the kind, keyed and featured as
// names.Parse reads the name, with the Russian audio entry.
func named(b byte, name, kind string, ru Audio) Torrent {
	t := torrent(b, name, name+".mkv")
	p := names.Parse(name)
	t.Kind, t.KindLayer, t.TitleKey = kind, LayerRules, p.Key
	t.Year, t.Season, t.Episode, t.Quality = p.Year, p.Season, p.Episode, p.Quality
	ru.Language = "ru"
	t.Audio = []Audio{ru}
	return t
}

var (
	marker = Audio{Marker: true, Mark: "Rus", Probability: new(1.0), Layer: LayerRules}
	likely = Audio{Probability: new(0.7), Layer: LayerJev}
	silent = Audio{}
)

// matchTo records the title key matched to the TMDB work by an English
// search answer with the title, and returns the work's ID.
func matchTo(t *testing.T, s *Store, key, typ string, tmdbID int, title string) int64 {
	t.Helper()
	id, _, err := s.RecordMatch(context.Background(), key, Found{Hit: &Hit{Type: typ, TMDBID: tmdbID, Lang: "en", Title: title, OriginalTitle: title},
		Confidence: 1, Layer: LayerTMDB}, t0)
	require.NoError(t, err)
	require.NotZero(t, id)
	return id
}

// Works of seeded.
var dune, interstellar, oppenheimer, severance int64

// seeded holds two Dune releases with Russian markers under one title key
// and a third under its Russian title, matched to one work; Interstellar
// with a likely Russian track; Oppenheimer without; two Severance episodes
// and a season pack; an album and software, matched to nothing; and peers
// for them at t0, with Interstellar's peers a day before too.
func seeded(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	s := open(t)
	k, err := s.HashKey(ctx)
	require.NoError(t, err)
	for _, tr := range []Torrent{
		named(1, "Dune.Part.Two.2024.1080p.BluRay.x264", "movie", marker),
		named(2, "Dune.Part.Two.2024.2160p.BluRay.Rus", "movie", marker),
		named(3, "Interstellar 2014 1080p BluRay x264", "movie", likely),
		named(4, "Oppenheimer 2023 1080p WEB-DL", "movie", silent),
		named(5, "Severance.S02E01.1080p.WEB-DL", "series", silent),
		named(6, "Severance.S02E02.720p.WEB-DL", "series", silent),
		named(7, "Pink Floyd - The Dark Side Of The Moon (1973) [FLAC]", "music", silent),
		named(8, "Ubuntu 24.04 Desktop", "software", silent),
		named(9, "Дюна: Часть вторая (2024) WEB-DL 720p", "movie", Audio{Marker: true, Mark: "LostFilm", Probability: new(1.0), Layer: LayerRules}),
		named(10, "Severance.S01.2160p.WEB-DL", "series", likely),
	} {
		_, _, err := s.Insert(ctx, tr)
		require.NoError(t, err)
	}
	dune = matchTo(t, s, "y:dune part two 2024", TypeMovie, 693134, "Dune: Part Two")
	require.Equal(t, dune, matchTo(t, s, "y:дюна часть вторая 2024", TypeMovie, 693134, "Dune: Part Two"), "two title keys, one work")
	interstellar = matchTo(t, s, "y:interstellar 2014", TypeMovie, 157336, "Interstellar")
	oppenheimer = matchTo(t, s, "y:oppenheimer 2023", TypeMovie, 872585, "Oppenheimer")
	severance = matchTo(t, s, "s:severance", TypeTV, 95396, "Severance")
	_, _, err = s.MergePeers(ctx, []PeerSketch{
		{Hash: hash(1), Hour: t0, Sketch: sketchOf(k, 0, 120)},
		{Hash: hash(2), Hour: t0.Add(-time.Hour), Sketch: sketchOf(k, 50, 100)},
		{Hash: hash(9), Hour: t0, Sketch: sketchOf(k, 2000, 20)},
		{Hash: hash(3), Hour: t0.Add(-2 * time.Hour), Sketch: sketchOf(k, 0, 80)},
		{Hash: hash(3), Hour: t0.Add(-30 * time.Hour), Sketch: sketchOf(k, 1000, 50)},
		{Hash: hash(4), Hour: t0, Sketch: sketchOf(k, 0, 20)},
		{Hash: hash(5), Hour: t0, Sketch: sketchOf(k, 0, 30)},
		{Hash: hash(6), Hour: t0, Sketch: sketchOf(k, 100, 30)},
		{Hash: hash(7), Hour: t0, Sketch: sketchOf(k, 0, 10)},
		{Hash: hash(8), Hour: t0, Sketch: sketchOf(k, 0, 500)},
	})
	require.NoError(t, err)
	require.NoError(t, s.ComputeTop(ctx, t0))
	return s
}

func works(es []TopEntry) []int64 {
	var out []int64
	for _, e := range es {
		out = append(out, e.Work.Work.ID)
	}
	return out
}

func TestComputeTop(t *testing.T) {
	ctx := context.Background()
	s := seeded(t)

	top, total, at, err := s.Top(ctx, "movie", WindowDay, VoiceAny, "", 10, 0)
	require.NoError(t, err)
	require.Equal(t, int64(3), total)
	require.Equal(t, t0, at)
	require.Equal(t, []int64{dune, interstellar, oppenheimer}, works(top))
	require.Equal(t, []int{1, 2, 3}, []int{top[0].Rank, top[1].Rank, top[2].Rank})
	require.InDelta(t, 170, top[0].Peers, 17, "the peers of the three Dune releases, under both title keys, are unioned")
	require.Nil(t, top[0].Delta, "Dune had no peers the day before")
	require.NotNil(t, top[1].Delta)
	require.InDelta(t, 0.6, *top[1].Delta, 0.2, "Interstellar grew from 50 to 80")
	d := top[0].Work
	require.Equal(t, "Dune: Part Two", d.Work.Texts["en"].Title)
	require.Equal(t, int64(3), d.Releases)
	require.Equal(t, 1, d.Rank)
	require.Equal(t, t0, d.FirstSeen)
	require.Equal(t, []WorkAudio{{Language: "ru", Marked: 3, Marks: []string{"Rus", "LostFilm"}}}, d.Audio, "marks most frequent first")
	require.Equal(t, 2, top[1].Work.Rank, "Interstellar is second in the week's top too")

	top, _, _, err = s.Top(ctx, "movie", WindowDay, VoiceLikely, "ru", 10, 0)
	require.NoError(t, err)
	require.Equal(t, []int64{dune, interstellar}, works(top), "p50 admits markers and the likely track")
	top, _, _, err = s.Top(ctx, "movie", WindowDay, VoiceMarker, "ru", 10, 0)
	require.NoError(t, err)
	require.Equal(t, []int64{dune}, works(top), "explicit admits markers only")
	top, _, _, err = s.Top(ctx, "movie", WindowDay, VoiceMarker, "uk", 10, 0)
	require.NoError(t, err)
	require.Empty(t, top, "a language not configured has no top")

	top, _, _, err = s.Top(ctx, "movie", WindowWeek, VoiceAny, "", 10, 0)
	require.NoError(t, err)
	require.Equal(t, []int64{dune, interstellar, oppenheimer}, works(top))
	require.InDelta(t, 130, top[1].Peers, 13, "the week has both of Interstellar's hours")
	require.Nil(t, top[1].Delta, "nothing the week before")
	top, _, _, err = s.Top(ctx, "movie", WindowWeek, VoiceAny, "", 1, 1)
	require.NoError(t, err)
	require.Equal(t, []int64{interstellar}, works(top), "offset and limit page the top")

	top, _, _, err = s.Top(ctx, "series", WindowMonth, VoiceAny, "", 10, 0)
	require.NoError(t, err)
	require.Equal(t, []int64{severance}, works(top))
	require.InDelta(t, 60, top[0].Peers, 6, "episodes and seasons fold into the series")
	require.Equal(t, int64(3), top[0].Work.Releases)
	top, total, _, err = s.Top(ctx, "music", WindowDay, VoiceAny, "", 10, 0)
	require.NoError(t, err)
	require.Empty(t, top, "music has no top")
	require.Zero(t, total)

	r, err := s.Get(ctx, hash(3))
	require.NoError(t, err)
	require.InDelta(t, 130, r.PeersWeek, 13, "peers_week is refreshed")
	r, err = s.Get(ctx, hash(8))
	require.NoError(t, err)
	require.InDelta(t, 500, r.PeersWeek, 50, "every torrent has peers_week, in a top or not")
	ws, err := s.Works(ctx, []int64{dune, interstellar})
	require.NoError(t, err)
	require.InDelta(t, 170, ws[dune].PeersWeek, 17, "and every work")
	require.InDelta(t, 130, ws[interstellar].PeersWeek, 13)

	require.NoError(t, s.ComputeTop(ctx, t0.Add(8*24*time.Hour)))
	top, _, _, err = s.Top(ctx, "movie", WindowWeek, VoiceAny, "", 10, 0)
	require.NoError(t, err)
	require.Empty(t, top, "a week later nobody has peers")
	r, err = s.Get(ctx, hash(3))
	require.NoError(t, err)
	require.Zero(t, r.PeersWeek)
	ws, err = s.Works(ctx, []int64{dune})
	require.NoError(t, err)
	require.Zero(t, ws[dune].PeersWeek)
}

func TestTitlePage(t *testing.T) {
	ctx := context.Background()
	s := seeded(t)
	ranks, err := s.Ranks(ctx, dune)
	require.NoError(t, err)
	require.Equal(t, [3]int{1, 1, 1}, ranks)
	ranks, err = s.Ranks(ctx, oppenheimer)
	require.NoError(t, err)
	require.Equal(t, [3]int{3, 3, 3}, ranks)
	none := matchTo(t, s, "y:nothing 2000", TypeMovie, 1, "Nothing")
	ranks, err = s.Ranks(ctx, none)
	require.NoError(t, err)
	require.Zero(t, ranks, "a work without peers is in no top")

	peers, err := s.WorkPeers(ctx, interstellar, t0)
	require.NoError(t, err)
	require.InDelta(t, 80, peers.Day, 8)
	require.InDelta(t, 130, peers.Week, 13)
	require.InDelta(t, 130, peers.Month, 13)

	sums, err := s.Summaries(ctx, []int64{severance, none, 999})
	require.NoError(t, err)
	require.Len(t, sums, 2, "an ID with no work is absent")
	require.Equal(t, int64(3), sums[severance].Releases)
	require.Empty(t, sums[severance].Sources, "no release has a source until it is classified")
	require.Equal(t, []WorkAudio{{Language: "ru", Likely: 1}}, sums[severance].Audio)
	require.Zero(t, sums[none].Releases, "a work without releases has a summary")
	require.Equal(t, []WorkAudio{{Language: "ru"}}, sums[none].Audio)

	names := func(f ReleaseFilter) []string {
		t.Helper()
		if f.Limit == 0 {
			f.Limit = 10
		}
		p, err := s.WorkReleases(ctx, severance, f)
		require.NoError(t, err)
		require.Equal(t, int64(3), p.Total)
		require.Equal(t, []int{1, 2}, p.Seasons)
		var out []string
		for _, r := range p.Releases {
			out = append(out, r.Name)
		}
		require.Equal(t, int(p.Matched), len(out))
		return out
	}
	require.Equal(t, []string{"Severance.S02E02.720p.WEB-DL", "Severance.S02E01.1080p.WEB-DL", "Severance.S01.2160p.WEB-DL"}, names(ReleaseFilter{}),
		"most peers first; even peers, the newest")
	require.Equal(t, []string{"Severance.S02E01.1080p.WEB-DL"}, names(ReleaseFilter{Quality: "1080p"}))
	require.Equal(t, []string{"Severance.S01.2160p.WEB-DL"}, names(ReleaseFilter{Season: 1}))
	require.Equal(t, []string{"Severance.S02E02.720p.WEB-DL"}, names(ReleaseFilter{Season: 2, Episode: 2}))
	require.Equal(t, []string{"Severance.S01.2160p.WEB-DL"}, names(ReleaseFilter{AudioLanguage: "ru", Voice: VoiceLikely}))
	require.Empty(t, names(ReleaseFilter{AudioLanguage: "ru", Voice: VoiceMarker}))
	require.Empty(t, names(ReleaseFilter{MaxSize: 1 << 29}))
	require.Len(t, names(ReleaseFilter{MinSize: 1 << 30, MaxSize: 1 << 30}), 3)
	p, err := s.WorkReleases(ctx, severance, ReleaseFilter{Sort: SortFirstSeen, Offset: 1, Limit: 1})
	require.NoError(t, err)
	require.Len(t, p.Releases, 1)
	require.Equal(t, int64(3), p.Matched, "Matched counts past the page")
	require.Equal(t, 2, p.Releases[0].Season)
	require.Len(t, p.Releases[0].Audio, 1, "with audio")

	p, err = s.WorkReleases(ctx, dune, ReleaseFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, p.Releases, 3)
	require.Equal(t, "Rus", p.Releases[0].Audio[0].Mark)
	require.Empty(t, p.Seasons)
}

func TestFindWorks(t *testing.T) {
	ctx := context.Background()
	s := seeded(t)
	_, err := s.RecordSearch(ctx, TypeMovie, "дюна", []Hit{{Type: TypeMovie, TMDBID: 693134, Lang: "ru", Title: "Дюна: Часть вторая"},
		{Type: TypeMovie, TMDBID: 438631, Lang: "ru", Title: "Дюна", OriginalTitle: "Dune", Popularity: 50}}, t0)
	require.NoError(t, err)
	tmdbOnly, err := s.WorkByTMDB(ctx, TypeMovie, 438631)
	require.NoError(t, err)

	for _, tt := range []struct {
		text, typ string
		want      []int64
	}{
		{"дюна", "", []int64{dune, tmdbOnly}},
		{"part two", "", []int64{dune}},
		{"severance", "", []int64{severance}},
		{"severance", TypeMovie, nil},
		{"1080p", TypeMovie, []int64{dune, interstellar, oppenheimer}},
		{"pink floyd", "", nil},
		{"", "", nil},
	} {
		got, err := s.FindWorks(ctx, tt.text, tt.typ)
		require.NoError(t, err)
		require.Equal(t, tt.want, got, "%q %q", tt.text, tt.typ)
	}
}

func TestHome(t *testing.T) {
	ctx := context.Background()
	s := seeded(t)
	h, err := s.Home(ctx, t0.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, Home{Total: 10, NewDay: 10}, h)
	h, err = s.Home(ctx, t0.Add(25*time.Hour))
	require.NoError(t, err)
	require.Equal(t, Home{Total: 10}, h)
}
