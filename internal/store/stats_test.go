package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/rules"
)

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
		{name: "kind", b: Browse{Kind: "series", Limit: 10}, want: []Hash{hash(2)}, total: 1},
		{name: "unknown kind", b: Browse{UnknownKind: true, Limit: 10}, want: []Hash{hash(3), hash(4)}, total: 2},
		{name: "marker", b: Browse{AudioLanguage: "ru", AudioMarker: true, Limit: 10}, want: []Hash{hash(1)}, total: 1},
		{name: "probability", b: Browse{AudioLanguage: "ru", MinAudioProbability: new(0.5), Limit: 10}, want: []Hash{hash(2), hash(1)}, total: 2},
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
	rs, _, err := s.Page(ctx, Browse{Kind: "movie", Limit: 1})
	require.NoError(t, err)
	require.Len(t, rs[0].Audio, 1, "with the audio of each release")
}

func TestCard(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	fill(t, s)
	c, err := s.Card(ctx, hash(1), 1)
	require.NoError(t, err)
	require.Equal(t, "announce", c.Discovery)
	require.Equal(t, 2, c.Stored)
	require.Equal(t, []rules.File{{Path: "Alien.mkv", Size: 8 << 30}}, c.Files)
	_, err = s.Card(ctx, hash(9), 8)
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
