package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/rules"
)

func TestKnown(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	_, _, err := s.Insert(ctx, torrent(1, "Alien.1979.mkv"))
	require.NoError(t, err)
	_, err = s.Reject(ctx, hash(2), rules.Book, t0)
	require.NoError(t, err)

	stored, rejected, err := s.Known(ctx, []Hash{hash(1), hash(2), hash(3)}, t0.Add(2*time.Hour))
	require.NoError(t, err)
	require.Equal(t, map[Hash]bool{hash(1): true}, stored)
	require.Equal(t, map[Hash]bool{hash(2): true}, rejected)
	_, _, err = s.Known(ctx, []Hash{hash(1)}, t0.Add(time.Hour))
	require.NoError(t, err)

	r, err := s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.Equal(t, t0, r.FirstSeen)
	require.Equal(t, t0.Add(2*time.Hour), r.LastSeen, "an older sighting never moves last_seen back")
}

func TestSearch(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for _, tr := range []Torrent{
		torrent(1, "Alien.1979.Director's.Cut.1080p.BluRay", "Alien.1979.mkv"),
		torrent(2, "Aliens.1986.1080p", "Aliens.1986.mkv"),
		torrent(3, "Чужой 1979 BDRip", "Чужой.mkv", "Audio/Rus/Чужой.ac3"),
		torrent(4, "Collection", "Ridley Scott/Alien 1979.mkv"),
		torrent(5, "Alien Parody 1979"),
	} {
		switch tr.Hash {
		case hash(3):
			tr.Audio = []Audio{{Language: "ru", Marker: true, ScriptHint: true, Probability: new(1.0), Layer: LayerRules}}
		case hash(4):
		default:
			tr.Audio = []Audio{{Language: "ru"}}
		}
		tr.SeenAt = t0.Add(time.Duration(tr.Hash[0]) * time.Minute)
		_, _, err := s.Insert(ctx, tr)
		require.NoError(t, err)
	}
	classify(t, s, 1, Classification{Kind: "movie", KindConfidence: 0.9, Source: "encode", Audio: map[string]float64{"ru": 0.2}})
	classify(t, s, 2, Classification{Kind: "movie", Audio: map[string]float64{"ru": 0.7, "uk": 0.9}})
	classify(t, s, 3, Classification{Kind: "movie", Audio: map[string]float64{"ru": 0.1}})
	classify(t, s, 5, Classification{Kind: "adult"})

	names := func(q Query) []string {
		t.Helper()
		if q.Limit == 0 {
			q.Limit = 10
		}
		rs, err := s.Search(ctx, q)
		require.NoError(t, err)
		var out []string
		for _, r := range rs {
			out = append(out, r.Name)
		}
		return out
	}

	require.ElementsMatch(t, []string{"Alien.1979.Director's.Cut.1080p.BluRay", "Collection", "Alien Parody 1979"}, names(Query{Text: "alien 1979"}))
	require.ElementsMatch(t, []string{"Alien.1979.Director's.Cut.1080p.BluRay", "Collection"}, names(Query{Text: "alien 1979", Exclude: []string{"adult"}}))
	require.Equal(t, []string{"Чужой 1979 BDRip"}, names(Query{Text: "чужой"}))
	require.Equal(t, []string{"Collection"}, names(Query{Text: "Ridley"}), "file paths are searched")
	require.Empty(t, names(Query{Text: `"); DROP TABLE torrent_releases; --`}))
	require.Equal(t, []string{"Collection"}, names(Query{Kinds: []string{""}}))
	require.Equal(t, []string{"Чужой 1979 BDRip", "Aliens.1986.1080p"}, names(Query{AudioLanguage: "ru", MinAudioProbability: new(0.5)}),
		"the classifier does not override a marker's 1")
	require.Equal(t, []string{"Collection", "Чужой 1979 BDRip", "Aliens.1986.1080p"},
		names(Query{AudioLanguage: "ru", MinAudioProbability: new(0.5), Unexamined: true}), "4 is unexamined and its audio unknown")
	require.Equal(t, []string{"Aliens.1986.1080p"}, names(Query{AudioLanguage: "uk", MinAudioProbability: new(0.5)}),
		"the filter reads the language's own entries")
	require.Equal(t, []string{"Alien Parody 1979", "Collection", "Чужой 1979 BDRip"}, names(Query{Limit: 3}), "most recently seen first")

	r, err := s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.Equal(t, Release{
		ID: 1, Hash: hash(1), Name: "Alien.1979.Director's.Cut.1080p.BluRay", Size: 1 << 30, FileCount: 1,
		Kind: "movie", KindLayer: LayerJev, KindConfidence: 0.9, Source: "encode", Classified: true,
		Audio:     []Audio{{Language: "ru", Probability: new(0.2), Layer: LayerJev}},
		FirstSeen: t0.Add(time.Minute), LastSeen: t0.Add(time.Minute),
	}, r)
	r, err = s.Get(ctx, hash(2))
	require.NoError(t, err)
	require.Equal(t, []Audio{{Language: "ru", Probability: new(0.7), Layer: LayerJev}}, r.Audio, "uk is not among the languages")
	r, err = s.Get(ctx, hash(4))
	require.NoError(t, err)
	require.Nil(t, r.Audio)
}

func classify(t *testing.T, s *Store, id int64, c Classification) int {
	t.Helper()
	n, err := s.Classify(context.Background(), id, c, t0)
	require.NoError(t, err)
	return n
}

func TestUnclassified(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for i := byte(1); i <= 5; i++ {
		tr := torrent(i, "t", "a.mkv", "b.mkv", "c.mkv")
		tr.Audio = []Audio{{Language: "ru", ScriptHint: i == 2 || i == 5, Marker: i == 1 || i == 3}}
		if i == 5 {
			tr.Kind, tr.KindLayer = "series", LayerRules
		}
		_, _, err := s.Insert(ctx, tr)
		require.NoError(t, err)
	}
	classify(t, s, 1, Classification{Kind: "movie"})
	require.NoError(t, s.ClassifyFailed(ctx, 3, "422", t0))

	p, err := s.Unclassified(ctx, 0, 10, 2)
	require.NoError(t, err)
	require.Equal(t, []Pending{{ID: 2, Name: "t", Size: 1 << 30, Files: []rules.File{{Path: "a.mkv", Size: 1 << 30}, {Path: "b.mkv", Size: 1 << 30}}}}, p,
		"not 4, without a hint, nor 5, whose kind is known")
	p, err = s.Unclassified(ctx, 2, 10, 2)
	require.NoError(t, err)
	require.Empty(t, p)
	none, err := New(ctx, s.db, Options{})
	require.NoError(t, err)
	p, err = none.Unclassified(ctx, 0, 10, 2)
	require.NoError(t, err)
	require.Empty(t, p, "no hint counts without audio languages")

	p, err = s.Pending(ctx, []int64{1, 4, 5}, 1)
	require.NoError(t, err)
	require.Equal(t, []Pending{
		{ID: 4, Name: "t", Size: 1 << 30, Files: []rules.File{{Path: "a.mkv", Size: 1 << 30}}},
		{ID: 5, Name: "t", Size: 1 << 30, Files: []rules.File{{Path: "a.mkv", Size: 1 << 30}}, KindKnown: true},
	}, p, "1 is examined already")
}

func TestReuseByKey(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for i, k := range []string{"y:dune 2024", "y:dune 2024", "y:dune 2021", "y:arrival 2016"} {
		tr := torrent(byte(i+1), "t")
		tr.TitleKey = k
		_, _, err := s.Insert(ctx, tr)
		require.NoError(t, err)
	}
	kind, err := s.KindByKey(ctx, "y:arrival 2016")
	require.NoError(t, err)
	require.Empty(t, kind)

	require.Equal(t, 1, classify(t, s, 1, Classification{Kind: "movie"}), "copied to 2, not to 3 of another year")
	r, err := s.Get(ctx, hash(2))
	require.NoError(t, err)
	require.Equal(t, "movie", r.Kind)
	require.Equal(t, LayerReuse, r.KindLayer)
	require.False(t, r.Classified, "only the kind is copied")
	r, err = s.Get(ctx, hash(3))
	require.NoError(t, err)
	require.Empty(t, r.Kind)

	require.Zero(t, classify(t, s, 2, Classification{Kind: "other", Source: "web"}), "a known kind stays")
	r, err = s.Get(ctx, hash(2))
	require.NoError(t, err)
	require.Equal(t, "movie", r.Kind)
	require.Equal(t, "web", r.Source)

	kind, err = s.KindByKey(ctx, "y:dune 2024")
	require.NoError(t, err)
	require.NotEmpty(t, kind)

	tr := torrent(9, "Arrival.2016.1080p")
	tr.TitleKey, tr.Kind, tr.KindLayer = "y:arrival 2016", "movie", LayerRules
	_, copied, err := s.Insert(ctx, tr)
	require.NoError(t, err)
	require.Equal(t, 1, copied)
	r, err = s.Get(ctx, hash(4))
	require.NoError(t, err)
	require.Equal(t, "movie", r.Kind, "a later release with a known kind lends it too")
	require.Equal(t, LayerReuse, r.KindLayer)
}

// fill stores five torrents: 1 a rules movie with a Russian marker, 2 a
// classified web series, 3 and 4 of unknown kind with a script hint, 3 seen
// a day after the rest, and 5 a large adult one; and rejects two hashes.
func fill(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	for _, tr := range []Torrent{
		{Hash: hash(1), Name: "Alien.1979.Rus.1080p", Size: 8 << 30, Discovery: "announce", SeenAt: t0, Kind: "movie", KindLayer: LayerRules,
			Files: []rules.File{{Path: "Alien.mkv", Size: 8 << 30}, {Path: "Alien.srt", Size: 1 << 10}},
			Audio: []Audio{{Language: "ru", Marker: true, ScriptHint: false, Probability: new(1.0), Layer: LayerRules}}},
		{Hash: hash(2), Name: "Severance S02", Size: 40 << 30, Discovery: "sample", SeenAt: t0,
			Audio: []Audio{{Language: "ru"}}},
		{Hash: hash(3), Name: "Чужой", Size: 2 << 30, Discovery: "sample", SeenAt: t0.Add(24 * time.Hour),
			Audio: []Audio{{Language: "ru", ScriptHint: true}}},
		{Hash: hash(4), Name: "Сборник", Size: 1 << 30, Discovery: "sample", SeenAt: t0,
			Audio: []Audio{{Language: "ru", ScriptHint: true}}},
		{Hash: hash(5), Name: "Something", Size: 90 << 30, Discovery: "sample", SeenAt: t0, Kind: "adult", KindLayer: LayerRules},
	} {
		_, _, err := s.Insert(ctx, tr)
		require.NoError(t, err)
	}
	r, err := s.Get(ctx, hash(2))
	require.NoError(t, err)
	_, err = s.Classify(ctx, r.ID, Classification{Kind: "series", KindConfidence: 0.9, Source: "web", SourceConfidence: 0.8,
		Audio: map[string]float64{"ru": 0.7}}, t0)
	require.NoError(t, err)
	r, err = s.Get(ctx, hash(4))
	require.NoError(t, err)
	_, err = s.Classify(ctx, r.ID, Classification{Audio: map[string]float64{"ru": 0.2}}, t0)
	require.NoError(t, err)
	for _, h := range []Hash{hash(10), hash(11)} {
		_, err := s.Reject(ctx, h, rules.Small, t0)
		require.NoError(t, err)
	}
	_, err = s.Reject(ctx, hash(12), rules.Adult, t0)
	require.NoError(t, err)
}

func hashes(rs []Release) []Hash {
	var out []Hash
	for _, r := range rs {
		out = append(out, r.Hash)
	}
	return out
}

func TestPage(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	fill(t, s)
	for _, tt := range []struct {
		name  string
		b     Browse
		want  []Hash
		total int64
	}{
		{name: "latest first", b: Browse{Exclude: []string{"adult"}, Limit: 2}, want: []Hash{hash(3), hash(4)}, total: 4},
		{name: "offset", b: Browse{Exclude: []string{"adult"}, Offset: 2, Limit: 2}, want: []Hash{hash(2), hash(1)}, total: 4},
		{name: "text", b: Browse{Text: "alien", Limit: 10}, want: []Hash{hash(1)}, total: 1},
		{name: "infohash", b: Browse{Text: "0400000000000000000000000000000000000000", Limit: 10}, want: []Hash{hash(4)}, total: 1},
		{name: "kind", b: Browse{Kinds: []string{"series"}, Limit: 10}, want: []Hash{hash(2)}, total: 1},
		{name: "kinds", b: Browse{Kinds: []string{"series", "adult"}, Limit: 10}, want: []Hash{hash(5), hash(2)}, total: 2},
		{name: "unknown kind", b: Browse{Kinds: []string{""}, Limit: 10}, want: []Hash{hash(3), hash(4)}, total: 2},
		{name: "marker", b: Browse{AudioLanguage: "ru", Voice: VoiceMarker, Limit: 10}, want: []Hash{hash(1)}, total: 1},
		{name: "likely", b: Browse{AudioLanguage: "ru", Voice: VoiceLikely, Limit: 10}, want: []Hash{hash(2), hash(1)}, total: 2},
		{name: "unmatched", b: Browse{Matched: new(false), Exclude: []string{"adult"}, Limit: 1}, want: []Hash{hash(3)}, total: 4},
		{name: "matched", b: Browse{Matched: new(true), Limit: 10}, total: 0},
		{name: "source", b: Browse{Source: "web", Limit: 10}, want: []Hash{hash(2)}, total: 1},
		{name: "layer", b: Browse{KindLayer: LayerRules, Exclude: []string{"adult"}, Limit: 10}, want: []Hash{hash(1)}, total: 1},
		{name: "size", b: Browse{MinSize: 8 << 30, Exclude: []string{"adult"}, Limit: 10}, want: []Hash{hash(2), hash(1)}, total: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rs, total, err := s.Page(ctx, tt.b)
			require.NoError(t, err)
			require.Equal(t, tt.want, hashes(rs))
			require.Equal(t, tt.total, total)
		})
	}
	rs, _, err := s.Page(ctx, Browse{Kinds: []string{"movie"}, Limit: 1})
	require.NoError(t, err)
	require.Len(t, rs[0].Audio, 1, "with the audio of each release")
}

func TestCard(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	fill(t, s)
	c, err := s.Card(ctx, hash(1), 1, t0)
	require.NoError(t, err)
	require.Equal(t, "announce", c.Discovery)
	require.Equal(t, 2, c.Stored)
	require.Equal(t, []rules.File{{Path: "Alien.mkv", Size: 8 << 30}}, c.Files)
	_, err = s.Card(ctx, hash(9), 8, t0)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestStats(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	fill(t, s)
	st, err := s.Stats(ctx, t0.Add(24*time.Hour+time.Minute), 3)
	require.NoError(t, err)
	require.EqualValues(t, 5, st.Total)
	require.Equal(t, map[string]int64{"movie": 1, "series": 1, "adult": 1, "": 2}, st.Kinds)
	require.Equal(t, map[string]int64{LayerRules: 2, LayerJev: 1, "": 2}, st.Layers)
	require.Equal(t, map[string]int64{"web": 1, "": 4}, st.Sources)
	require.Equal(t, map[string]AudioCounts{"ru": {Marker: 1, Likely: 1, Unlikely: 1}}, st.Audio)
	require.Equal(t, map[string]int64{"small": 2, "adult": 1}, st.Rejected)
	require.Equal(t, []int64{1, 4, 0}, st.Days, "3 in the last 24 hours, the rest in the 24 before")

	pending, err := s.Unclassified(ctx, 0, 100, 0)
	require.NoError(t, err)
	require.EqualValues(t, len(pending), st.Pending, "what Unclassified selects")
	require.EqualValues(t, 1, st.Pending)
}

func TestCardPeersAndWork(t *testing.T) {
	ctx := context.Background()
	s := seeded(t)
	c, err := s.Card(ctx, hash(2), 10, t0)
	require.NoError(t, err)
	require.InDelta(t, 100, c.Peers.Day, 10)
	require.Equal(t, c.Peers.Day, c.Peers.Week)
	require.Equal(t, c.Peers.Day, c.Peers.Month)
	require.NotNil(t, c.Work)
	require.Equal(t, dune, c.Work.Work.ID)
	require.Equal(t, "movie", c.Work.Work.Kind())
	require.Equal(t, 1, c.Work.Rank)
	require.Equal(t, int64(3), c.Work.Releases, "the releases of both title keys")
	require.Equal(t, 2, c.RankInWork, "fewer peers than release 1, more than release 9")
	other, err := s.Card(ctx, hash(1), 10, t0)
	require.NoError(t, err)
	require.Equal(t, 1, other.RankInWork)

	c, err = s.Card(ctx, hash(8), 10, t0)
	require.NoError(t, err)
	require.Nil(t, c.Work, "no work while unmatched")
	require.Zero(t, c.RankInWork)
	require.InDelta(t, 500, c.Peers.Day, 50)
}

func TestPageSort(t *testing.T) {
	ctx := context.Background()
	s := seeded(t)
	rs, _, err := s.Page(ctx, Browse{Sort: SortPeers, Limit: 2})
	require.NoError(t, err)
	require.Equal(t, []Hash{hash(8), hash(3)}, []Hash{rs[0].Hash, rs[1].Hash}, "most peers over the week first")
	rs, _, err = s.Page(ctx, Browse{Text: "1080p", Sort: SortPeers, Limit: 1})
	require.NoError(t, err)
	require.Equal(t, hash(3), rs[0].Hash, "a query sorts by peers too")
	rs, _, err = s.Page(ctx, Browse{Sort: SortSize, Limit: 1})
	require.NoError(t, err)
	require.Equal(t, hash(10), rs[0].Hash, "all are the same size; the newest wins")
	rs, total, err := s.Page(ctx, Browse{Matched: new(true), Quality: "720p", Limit: 10})
	require.NoError(t, err)
	require.Equal(t, []Hash{hash(9), hash(6)}, hashes(rs), "matched releases of a quality")
	require.Equal(t, int64(2), total)
}
