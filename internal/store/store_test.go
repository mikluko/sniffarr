package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/database"
	"github.com/mikluko/sniffarr/internal/rules"
)

func openAt(t *testing.T, path string) (*Store, *database.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, path)
	require.NoError(t, err)
	s, err := New(ctx, db, Options{Languages: []string{"ru"}})
	require.NoError(t, err)
	return s, db
}

func open(t *testing.T) *Store {
	t.Helper()
	s, db := openAt(t, filepath.Join(t.TempDir(), "test.db"))
	t.Cleanup(func() { require.NoError(t, db.Close()) })
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
	s, db := openAt(t, path)
	_, _, err := s.Insert(ctx, torrent(1, "Alien.1979.mkv"))
	require.NoError(t, err)
	require.NoError(t, db.Close())

	s, db = openAt(t, path)
	defer db.Close()
	_, err = s.Get(ctx, hash(1))
	require.NoError(t, err)
}

func TestKnown(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	_, _, err := s.Insert(ctx, torrent(1, "Alien.1979.mkv"))
	require.NoError(t, err)
	_, err = s.Reject(ctx, hash(2), rules.Book, t0)
	require.NoError(t, err)

	known, err := s.Known(ctx, []Hash{hash(1), hash(2), hash(3)}, t0.Add(2*time.Hour))
	require.NoError(t, err)
	require.Equal(t, map[Hash]bool{hash(1): true, hash(2): true}, known)
	_, err = s.Known(ctx, []Hash{hash(1)}, t0.Add(time.Hour))
	require.NoError(t, err)

	r, err := s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.Equal(t, t0, r.FirstSeen)
	require.Equal(t, t0.Add(2*time.Hour), r.LastSeen, "an older sighting never moves last_seen back")
}

func TestInsertReject(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	ok, _, err := s.Insert(ctx, torrent(1, "Alien.1979.1080p.BluRay", "Alien.1979.1080p.BluRay.mkv"))
	require.NoError(t, err)
	require.True(t, ok)
	ok, _, err = s.Insert(ctx, torrent(1, "Alien.1979.1080p.BluRay"))
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
	ok, _, err = s.Insert(ctx, torrent(2, "x"))
	require.NoError(t, err)
	require.False(t, ok, "insert of a rejected hash")

	_, err = s.Get(ctx, hash(2))
	require.ErrorIs(t, err, ErrNotFound)
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
	require.Empty(t, names(Query{Text: `"); DROP TABLE torrents; --`}))
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

func TestMaxFiles(t *testing.T) {
	ctx := context.Background()
	s, db := openAt(t, filepath.Join(t.TempDir(), "test.db"))
	defer db.Close()
	s.opts.MaxFiles = 2
	tr := torrent(1, "Collection")
	tr.Audio = []Audio{{Language: "ru", ScriptHint: true}}
	for i, size := range []int64{10, 30, 20, 5} {
		tr.Files = append(tr.Files, rules.File{Path: fmt.Sprintf("f%d.mkv", i), Size: size})
	}
	_, _, err := s.Insert(ctx, tr)
	require.NoError(t, err)

	r, err := s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.EqualValues(t, 4, r.FileCount, "the count stays true")
	p, err := s.Unclassified(ctx, 0, 1, 10)
	require.NoError(t, err)
	require.Equal(t, []rules.File{{Path: "f1.mkv", Size: 30}, {Path: "f2.mkv", Size: 20}}, p[0].Files, "the largest, largest first")
	got, err := s.Search(ctx, Query{Text: "f0", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, got, "a file not stored is not searched")
}

func TestSweep(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for i := byte(1); i <= 5; i++ {
		tr := torrent(i, fmt.Sprintf("Old.Movie.%d.1999", i), "old.mkv")
		tr.Audio = []Audio{{Language: "ru"}}
		tr.SeenAt = t0.Add(time.Duration(i) * time.Hour)
		_, _, err := s.Insert(ctx, tr)
		require.NoError(t, err)
	}
	_, err := s.Known(ctx, []Hash{hash(1)}, t0.Add(10*time.Hour))
	require.NoError(t, err)

	n, err := s.Sweep(ctx, t0.Add(4*time.Hour+time.Minute), 2)
	require.NoError(t, err)
	require.Equal(t, 3, n, "2, 3 and 4, in two batches; 1 was seen again")
	for b, stored := range map[byte]bool{1: true, 2: false, 3: false, 4: false, 5: true} {
		_, err := s.Get(ctx, hash(b))
		if stored {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, ErrNotFound)
		}
	}
	got, err := s.Search(ctx, Query{Text: "old movie", Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 2, "swept torrents leave the search index")
	var files int
	require.NoError(t, s.r.QueryRowContext(ctx, "SELECT count(*) FROM files").Scan(&files))
	require.Equal(t, 2, files, "and their files go with them")
	var audio int
	require.NoError(t, s.r.QueryRowContext(ctx, "SELECT count(*) FROM audio").Scan(&audio))
	require.Equal(t, 2, audio, "and their audio")
}

func TestMigrateAudio(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	defer db.Close()
	old := fstest.MapFS{}
	for _, name := range []string{"001_init.sql", "002_layers.sql"} {
		b, err := migrations.ReadFile("migrations/" + name)
		require.NoError(t, err)
		old[name] = &fstest.MapFile{Data: b}
	}
	require.NoError(t, db.Migrate(ctx, "store", old))
	for _, row := range []struct {
		id                  int64
		marker, hint        bool
		audio, classifiedAt any
	}{
		{1, true, true, 0.3, 100},
		{2, false, true, 0.8, 100},
		{3, false, true, nil, nil},
		{4, false, false, nil, nil},
	} {
		_, err := db.W.ExecContext(ctx, `INSERT INTO torrents (id, infohash, name, size, file_count, discovery, first_seen, last_seen,
			russian_marker, russian_hint, russian_audio, classified_at) VALUES (?, ?, 't', 1, 0, 'sample', 0, 0, ?, ?, ?, ?)`,
			row.id, []byte{byte(row.id)}, row.marker, row.hint, row.audio, row.classifiedAt)
		require.NoError(t, err)
	}

	s, err := New(ctx, db, Options{Languages: []string{"ru"}})
	require.NoError(t, err)
	got := map[int64]Audio{}
	for id := int64(1); id <= 4; id++ {
		rs := []Release{{ID: id}}
		require.NoError(t, s.audio(ctx, rs))
		require.Len(t, rs[0].Audio, 1)
		got[id] = rs[0].Audio[0]
	}
	require.Equal(t, map[int64]Audio{
		1: {Language: "ru", Marker: true, ScriptHint: true, Probability: new(1.0), Layer: LayerRules},
		2: {Language: "ru", ScriptHint: true, Probability: new(0.8), Layer: LayerJev},
		3: {Language: "ru", ScriptHint: true},
		4: {Language: "ru"},
	}, got)
	var dropped int
	require.NoError(t, db.R.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('torrents') WHERE name LIKE 'russian%'").Scan(&dropped))
	require.Zero(t, dropped, "the old columns are gone")
	p, err := s.Unclassified(ctx, 0, 10, 1)
	require.NoError(t, err)
	require.Len(t, p, 1, "3, unexamined with a hint")
	require.EqualValues(t, 3, p[0].ID)
}
