package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/names"
)

// named returns a torrent of the name with the kind, keyed as names.Parse
// keys it, with the Russian audio entry.
func named(b byte, name, kind string, ru Audio) Torrent {
	t := torrent(b, name, name+".mkv")
	p := names.Parse(name)
	t.Kind, t.KindLayer, t.TitleKey, t.WorkKey, t.Year, t.Season = kind, LayerRules, p.Key, p.Work, p.Year, p.Season
	ru.Language = "ru"
	t.Audio = []Audio{ru}
	return t
}

var (
	marker = Audio{Marker: true, Probability: new(1.0), Layer: LayerRules}
	likely = Audio{Probability: new(0.7), Layer: LayerJev}
	silent = Audio{}
)

// seeded holds two Dune releases with Russian markers, Interstellar with a
// likely Russian track, Oppenheimer without, two Severance episodes, an
// album, and peers for them at t0, with Interstellar's peers a day before
// too.
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
		named(6, "Severance.S02E02.1080p.WEB-DL", "series", silent),
		named(7, "Pink Floyd - The Dark Side Of The Moon (1973) [FLAC]", "music", silent),
		named(8, "Ubuntu 24.04 Desktop", "software", silent),
	} {
		_, _, err := s.Insert(ctx, tr)
		require.NoError(t, err)
	}
	_, _, err = s.MergePeers(ctx, []PeerSketch{
		{Hash: hash(1), Hour: t0, Sketch: sketchOf(k, 0, 120)},
		{Hash: hash(2), Hour: t0.Add(-time.Hour), Sketch: sketchOf(k, 50, 100)},
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

func keys(es []TopEntry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Work.Key)
	}
	return out
}

func TestComputeTop(t *testing.T) {
	ctx := context.Background()
	s := seeded(t)

	top, total, at, err := s.Top(ctx, "movie", WindowDay, VoiceAny, 10, 0)
	require.NoError(t, err)
	require.Equal(t, int64(3), total)
	require.Equal(t, t0, at)
	require.Equal(t, []string{"y:dune part two 2024", "y:interstellar 2014", "y:oppenheimer 2023"}, keys(top))
	require.Equal(t, []int{1, 2, 3}, []int{top[0].Rank, top[1].Rank, top[2].Rank})
	require.InDelta(t, 150, top[0].Peers, 15, "the two Dune releases' peers are unioned")
	require.Nil(t, top[0].Delta, "Dune had no peers the day before")
	require.NotNil(t, top[1].Delta)
	require.InDelta(t, 0.6, *top[1].Delta, 0.2, "Interstellar grew from 50 to 80")
	require.Equal(t, Work{Key: "y:dune part two 2024", Kind: "movie", Title: "Dune Part Two", Year: 2024, Rank: 1, Releases: 2,
		Audio: []Audio{{Language: "ru", Marker: true, Probability: new(1.0)}}, FirstSeen: t0}, top[0].Work)
	require.Equal(t, "Interstellar", top[1].Work.Title)
	require.Equal(t, 1, top[1].Work.Rank-1, "Interstellar is second in the week's top too")

	top, _, _, err = s.Top(ctx, "movie", WindowDay, VoiceLikely, 10, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"y:dune part two 2024", "y:interstellar 2014"}, keys(top), "p50 admits the marker and the likely track")
	top, _, _, err = s.Top(ctx, "movie", WindowDay, VoiceMarker, 10, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"y:dune part two 2024"}, keys(top), "explicit admits markers only")

	top, _, _, err = s.Top(ctx, "movie", WindowWeek, VoiceAny, 10, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"y:dune part two 2024", "y:interstellar 2014", "y:oppenheimer 2023"}, keys(top))
	require.InDelta(t, 130, top[1].Peers, 13, "the week has both of Interstellar's hours")
	require.Nil(t, top[1].Delta, "nothing the week before")
	top, _, _, err = s.Top(ctx, "movie", WindowWeek, VoiceAny, 1, 1)
	require.NoError(t, err)
	require.Equal(t, []string{"y:interstellar 2014"}, keys(top), "offset and limit page the top")

	top, _, _, err = s.Top(ctx, "series", WindowMonth, VoiceAny, 10, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"s:severance/2"}, keys(top))
	require.InDelta(t, 60, top[0].Peers, 6, "episodes fold into the season")
	require.Equal(t, 2, top[0].Work.Season)
	require.Equal(t, int64(2), top[0].Work.Releases)
	top, _, _, err = s.Top(ctx, "music", WindowDay, VoiceAny, 10, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"y:pink floyd the dark side of the moon 1973"}, keys(top))
	require.Equal(t, "Pink Floyd - The Dark Side Of The Moon", top[0].Work.Title)
	require.Equal(t, 1973, top[0].Work.Year)

	r, err := s.Get(ctx, hash(3))
	require.NoError(t, err)
	require.InDelta(t, 130, r.PeersWeek, 13, "peers_week is refreshed")
	r, err = s.Get(ctx, hash(8))
	require.NoError(t, err)
	require.InDelta(t, 500, r.PeersWeek, 50, "every torrent has peers_week, in a top or not")

	_, total, at, err = s.Top(ctx, "movie", WindowDay, VoiceAny, 10, 100)
	require.NoError(t, err)
	require.Equal(t, int64(3), total)
	require.Equal(t, t0, at)

	require.NoError(t, s.ComputeTop(ctx, t0.Add(8*24*time.Hour)))
	top, _, _, err = s.Top(ctx, "movie", WindowWeek, VoiceAny, 10, 0)
	require.NoError(t, err)
	require.Empty(t, top, "a week later nobody has peers")
	r, err = s.Get(ctx, hash(3))
	require.NoError(t, err)
	require.Zero(t, r.PeersWeek)
}

func TestWork(t *testing.T) {
	ctx := context.Background()
	s := seeded(t)
	v, err := s.Work(ctx, "y:dune part two 2024", WindowDay, VoiceAny, t0)
	require.NoError(t, err)
	w, peers, rs := v.Work, v.Peers, v.Releases
	require.Equal(t, 1, v.Rank)
	require.Nil(t, v.Delta)
	require.Equal(t, "movie", w.Kind)
	require.Equal(t, "Dune Part Two", w.Title)
	require.Equal(t, 1, w.Rank)
	require.InDelta(t, 150, peers, 15)
	require.Len(t, rs, 2)
	require.Equal(t, hash(1), rs[0].Hash, "the release with more peers over the window comes first")
	require.InDelta(t, 120, rs[0].Peers, 12)
	require.InDelta(t, 100, rs[1].Peers, 10)
	require.Len(t, rs[0].Audio, 1)

	v, err = s.Work(ctx, "y:interstellar 2014", WindowDay, VoiceAny, t0)
	require.NoError(t, err)
	require.Equal(t, 2, v.Rank, "the place in the top of the window and voice asked for")
	require.NotNil(t, v.Delta)
	v, err = s.Work(ctx, "y:interstellar 2014", WindowDay, VoiceMarker, t0)
	require.NoError(t, err)
	w, peers, rs = v.Work, v.Peers, v.Releases
	require.Zero(t, v.Rank, "outside the explicit top")
	require.Nil(t, v.Delta)
	require.Equal(t, "Interstellar", w.Title, "the work is named even when the voice admits no release")
	require.Zero(t, w.Releases)
	require.Zero(t, peers)
	require.Empty(t, rs)

	_, err = s.Work(ctx, "y:nothing 2000", WindowDay, VoiceAny, t0)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestWorks(t *testing.T) {
	ctx := context.Background()
	s := seeded(t)
	ws, err := s.Works(ctx, "1080p", t0, 10)
	require.NoError(t, err)
	var ks []string
	for _, w := range ws {
		ks = append(ks, w.Work.Key)
	}
	require.Equal(t, []string{"y:dune part two 2024", "y:interstellar 2014", "s:severance/2", "y:oppenheimer 2023"}, ks)
	require.InDelta(t, 150, ws[0].Peers, 15)
	require.Equal(t, int64(2), ws[0].Work.Releases)
	require.Equal(t, 1, ws[0].Work.Rank)
	require.InDelta(t, 60, ws[2].Peers, 6, "a match on one episode counts the season's peers")
	ws, err = s.Works(ctx, "ubuntu", t0, 10)
	require.NoError(t, err)
	require.Empty(t, ws, "software has no works")
	ws, err = s.Works(ctx, "", t0, 10)
	require.NoError(t, err)
	require.Empty(t, ws)
}

func TestHome(t *testing.T) {
	ctx := context.Background()
	s := seeded(t)
	h, err := s.Home(ctx, t0.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, Home{Total: 8, NewDay: 8}, h)
	h, err = s.Home(ctx, t0.Add(25*time.Hour))
	require.NoError(t, err)
	require.Equal(t, Home{Total: 8}, h)
}
