package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/rules"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

func hash(b byte) Hash {
	var h Hash
	h[0] = b
	return h
}

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func torrent(b byte, name string, paths ...string) Torrent {
	t := Torrent{Hash: hash(b), Name: name, Size: 1 << 30, Discovery: "sample", SeenAt: t0}
	for _, p := range paths {
		t.Files = append(t.Files, rules.File{Path: p, Size: 1 << 30})
	}
	return t
}

func TestReopenKeepsData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(ctx, path)
	require.NoError(t, err)
	_, err = s.Insert(ctx, torrent(1, "Alien.1979.mkv"))
	require.NoError(t, err)
	require.NoError(t, s.Close())

	s, err = Open(ctx, path)
	require.NoError(t, err)
	defer s.Close()
	_, err = s.Get(ctx, hash(1))
	require.NoError(t, err)
}

func TestInsertRejectUnknown(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	ok, err := s.Insert(ctx, torrent(1, "Alien.1979.1080p.BluRay", "Alien.1979.1080p.BluRay.mkv"))
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = s.Insert(ctx, torrent(1, "Alien.1979.1080p.BluRay"))
	require.NoError(t, err)
	require.False(t, ok, "second insert")

	ok, err = s.Reject(ctx, hash(2), rules.Adult, t0)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = s.Reject(ctx, hash(2), rules.Adult, t0)
	require.NoError(t, err)
	require.False(t, ok, "second reject")
	ok, err = s.Reject(ctx, hash(1), rules.Small, t0)
	require.NoError(t, err)
	require.False(t, ok, "reject of a stored hash")
	ok, err = s.Insert(ctx, torrent(2, "x"))
	require.NoError(t, err)
	require.False(t, ok, "insert of a rejected hash")

	unknown, err := s.Unknown(ctx, []Hash{hash(1), hash(2), hash(3), hash(3), hash(4)}, t0)
	require.NoError(t, err)
	require.Equal(t, []Hash{hash(3), hash(4)}, unknown)

	_, err = s.Get(ctx, hash(2))
	require.ErrorIs(t, err, ErrNotFound)
}

func TestLastSeen(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	_, err := s.Insert(ctx, torrent(1, "Alien.1979.mkv"))
	require.NoError(t, err)

	seen, err := s.Seen(ctx, hash(1), t0.Add(time.Hour))
	require.NoError(t, err)
	require.True(t, seen)
	_, err = s.Unknown(ctx, []Hash{hash(1)}, t0.Add(2*time.Hour))
	require.NoError(t, err)
	seen, err = s.Seen(ctx, hash(1), t0.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, seen)

	r, err := s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.Equal(t, t0, r.FirstSeen)
	require.Equal(t, t0.Add(2*time.Hour), r.LastSeen, "an older sighting never moves last_seen back")

	_, err = s.Reject(ctx, hash(2), rules.Book, t0)
	require.NoError(t, err)
	seen, err = s.Seen(ctx, hash(2), t0)
	require.NoError(t, err)
	require.True(t, seen)
	seen, err = s.Seen(ctx, hash(3), t0)
	require.NoError(t, err)
	require.False(t, seen)
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
		tr.RussianMarker = tr.Hash == hash(3)
		tr.SeenAt = t0.Add(time.Duration(tr.Hash[0]) * time.Minute)
		_, err := s.Insert(ctx, tr)
		require.NoError(t, err)
	}
	require.NoError(t, s.Classify(ctx, 1, Classification{Kind: "movie", KindConfidence: 0.9, Source: "encode", RussianAudio: 0.2}, t0))
	require.NoError(t, s.Classify(ctx, 2, Classification{Kind: "movie", RussianAudio: 0.7}, t0))
	require.NoError(t, s.Classify(ctx, 3, Classification{Kind: "movie", RussianAudio: 0.1}, t0))
	require.NoError(t, s.Classify(ctx, 5, Classification{Kind: "adult"}, t0))

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
	require.Empty(t, names(Query{Text: `"); DROP TABLE torrents; --`}))
	require.Equal(t, []string{"Collection"}, names(Query{Kinds: []string{""}}))
	require.Equal(t, []string{"Чужой 1979 BDRip", "Aliens.1986.1080p"}, names(Query{MinRussianAudio: new(0.5)}), "an explicit marker counts as 1")
	require.Equal(t, []string{"Alien Parody 1979", "Collection", "Чужой 1979 BDRip"}, names(Query{Limit: 3}), "most recently seen first")

	r, err := s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.Equal(t, Release{
		Hash: hash(1), Name: "Alien.1979.Director's.Cut.1080p.BluRay", Size: 1 << 30, FileCount: 1,
		Kind: "movie", KindConfidence: 0.9, Source: "encode", RussianAudio: new(0.2),
		FirstSeen: t0.Add(time.Minute), LastSeen: t0.Add(time.Minute),
	}, r)
	r, err = s.Get(ctx, hash(4))
	require.NoError(t, err)
	require.Nil(t, r.RussianAudio)
}

func TestUnclassified(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for i := byte(1); i <= 3; i++ {
		_, err := s.Insert(ctx, torrent(i, "t", "a.mkv", "b.mkv", "c.mkv"))
		require.NoError(t, err)
	}
	require.NoError(t, s.Classify(ctx, 1, Classification{Kind: "movie"}, t0))
	require.NoError(t, s.ClassifyFailed(ctx, 3, "422", t0))

	p, err := s.Unclassified(ctx, 0, 10, 2)
	require.NoError(t, err)
	require.Equal(t, []Pending{{ID: 2, Name: "t", Size: 1 << 30, Files: []rules.File{{Path: "a.mkv", Size: 1 << 30}, {Path: "b.mkv", Size: 1 << 30}}}}, p)
	p, err = s.Unclassified(ctx, 2, 10, 2)
	require.NoError(t, err)
	require.Empty(t, p)
}

func TestTokens(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	n, err := s.TokensUsed(ctx, "2026-10-01")
	require.NoError(t, err)
	require.Zero(t, n)
	require.NoError(t, s.AddTokens(ctx, "2026-10-01", 300))
	require.NoError(t, s.AddTokens(ctx, "2026-10-01", 200))
	n, err = s.TokensUsed(ctx, "2026-10-01")
	require.NoError(t, err)
	require.EqualValues(t, 500, n)
}
