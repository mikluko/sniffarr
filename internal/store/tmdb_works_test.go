package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/database"
)

// alienHit is Alien as an English search answer gives it.
var alienHit = Hit{Type: TypeMovie, TMDBID: 348, Lang: "en", Title: "Alien", OriginalTitle: "Alien", Year: 1979, PosterPath: "/s.jpg", Popularity: 12}

func TestTMDB(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	s.opts.TMDB = true
	for _, tr := range []struct {
		b         byte
		name, key string
		kind      string
	}{
		{1, "Alien.1979.1080p.BluRay", "y:alien 1979", "movie"},
		{2, "Alien.1979.Directors.Cut.2160p", "y:alien 1979", "movie"},
		{3, "Severance.S02E01", "s:severance", "series"},
		{4, "Moon.Safari.1998.FLAC", "y:moon safari 1998", "music"},
		{5, "Heat.1995.Rus", "y:heat 1995", ""},
	} {
		x := torrent(tr.b, tr.name)
		x.TitleKey, x.Kind = tr.key, tr.kind
		if tr.kind != "" {
			x.KindLayer = LayerRules
		}
		_, _, err := s.Insert(ctx, x)
		require.NoError(t, err)
	}

	keys, err := s.UnmatchedTitles(ctx, nil, t0, 10)
	require.NoError(t, err)
	require.Equal(t, []TitleKey{
		{Key: "s:severance", Kind: "series", Name: "Severance.S02E01"},
		{Key: "y:alien 1979", Kind: "movie", Name: "Alien.1979.Directors.Cut.2160p"},
	}, keys, "movies and series only, newest first, with the newest name")
	keys, err = s.UnmatchedTitles(ctx, []string{"y:alien 1979", "y:moon safari 1998"}, t0, 10)
	require.NoError(t, err)
	require.Equal(t, []TitleKey{{Key: "y:alien 1979", Kind: "movie", Name: "Alien.1979.Directors.Cut.2160p"}}, keys)
	keys, err = s.UnmatchedTitles(ctx, []string{}, t0, 10)
	require.NoError(t, err)
	require.Empty(t, keys)

	alien, detailed, err := s.RecordMatch(ctx, "y:alien 1979", Found{Hit: &alienHit, Confidence: 1, Layer: LayerTMDB}, t0)
	require.NoError(t, err)
	require.NotZero(t, alien)
	require.False(t, detailed)
	none, _, err := s.RecordMatch(ctx, "s:severance", Found{}, t0)
	require.NoError(t, err)
	require.Zero(t, none, "nothing found is no work")
	keys, err = s.UnmatchedTitles(ctx, nil, t0, 10)
	require.NoError(t, err)
	require.Empty(t, keys, "a failed match is not retried before its time")
	keys, err = s.UnmatchedTitles(ctx, nil, t0.Add(time.Second), 10)
	require.NoError(t, err)
	require.Equal(t, []string{"s:severance"}, titleKeys(keys), "then it is, and a match never is")

	r, err := s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.Equal(t, alien, r.Work, "every release of the key points at the work")
	w := Work{ID: alien, TMDBID: 348, Popularity: 12, Details: Details{Type: TypeMovie, OriginalTitle: "Alien", Year: 1979, PosterPath: "/s.jpg",
		Texts: map[string]Text{"en": {Title: "Alien", PosterPath: "/s.jpg"}}}}
	require.Equal(t, Match{Work: &w, Confidence: 1, Layer: LayerTMDB, MatchedAt: t0}, r.Match, "the minimal work of the search answer")
	require.Equal(t, []ExternalID{{ProviderTMDB, "348"}}, r.Match.Work.ExternalIDs())
	r, err = s.Get(ctx, hash(3))
	require.NoError(t, err)
	require.Zero(t, r.Work)
	require.Equal(t, Match{MatchedAt: t0}, r.Match, "looked up, nothing found")
	r, err = s.Get(ctx, hash(5))
	require.NoError(t, err)
	require.Equal(t, Match{}, r.Match, "never looked up")
	s.opts.TMDB = false
	r, err = s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.Equal(t, Match{}, r.Match, "without Options.TMDB")
	require.Equal(t, alien, r.Work, "the work all the same")
	s.opts.TMDB = true

	x := torrent(6, "Alien.1979.720p")
	x.TitleKey, x.Kind, x.KindLayer = "y:alien 1979", "movie", LayerRules
	_, _, err = s.Insert(ctx, x)
	require.NoError(t, err)
	r, err = s.Get(ctx, hash(6))
	require.NoError(t, err)
	require.Equal(t, alien, r.Work, "a release of a matched key gets its work on insert")

	rs, err := s.Search(ctx, Query{ID: &ExternalID{ProviderTMDB, "348"}, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rs, 3, "every release of the work")
	require.NoError(t, s.RecordDetails(ctx, alien, Details{Type: TypeMovie, OriginalTitle: "Alien"}, []ExternalID{{ProviderIMDb, "tt0078748"}, {ProviderTMDB, "348"}}, t0))
	rs, err = s.Search(ctx, Query{ID: &ExternalID{ProviderIMDb, "tt0078748"}, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rs, 3)
	require.Equal(t, []ExternalID{{ProviderIMDb, "tt0078748"}, {ProviderTMDB, "348"}}, rs[0].Match.Work.ExternalIDs())
	rs, err = s.Search(ctx, Query{ID: &ExternalID{ProviderTMDB, "tt0078748"}, Limit: 10})
	require.NoError(t, err)
	require.Empty(t, rs, "the ID of another provider")

	other, _, err := s.RecordMatch(ctx, "y:alien 1979", Found{Hit: &Hit{Type: TypeMovie, TMDBID: 8077}, Confidence: 0.6, Layer: LayerJev}, t0)
	require.NoError(t, err)
	require.NotEqual(t, alien, other)
	r, err = s.Get(ctx, hash(2))
	require.NoError(t, err)
	require.Equal(t, other, r.Work, "a new match moves the key's releases")
	require.Equal(t, 0.6, r.Match.Confidence)
	again, detailed, err := s.RecordMatch(ctx, "y:alien 1979", Found{Hit: &Hit{Type: TypeMovie, TMDBID: 348, Lang: "en", Title: "Renamed"}, Confidence: 1, Layer: LayerTMDB}, t0)
	require.NoError(t, err)
	require.Equal(t, alien, again, "the work stays one row")
	require.True(t, detailed)
	ws, err := s.Works(ctx, []int64{alien})
	require.NoError(t, err)
	require.Empty(t, ws[alien].Texts, "a search answer leaves fetched details alone")

	_, err = s.w.ExecContext(ctx, "DELETE FROM tmdb_works WHERE id = ?", alien)
	require.NoError(t, err)
	var ids, matched int
	require.NoError(t, s.r.QueryRowContext(ctx, "SELECT count(*) FROM tmdb_ids").Scan(&ids))
	require.Zero(t, ids, "IDs go with their work")
	require.NoError(t, s.r.QueryRowContext(ctx, "SELECT count(*) FROM torrent_releases WHERE work IS NOT NULL").Scan(&matched))
	require.Zero(t, matched, "releases let go of a deleted work")
}

// alienDetails are the details of Alien in both languages.
var alienDetails = Details{
	Type: "movie", OriginalTitle: "Alien", Year: 1979, PosterPath: "/vfrQk5IPloGg1v9Rzbh2Eg3VGyM.jpg", Rating: 8.176, Votes: 16995, Runtime: 117,
	Countries: []string{"US", "GB"}, Language: "en",
	Texts: map[string]Text{
		"en": {Title: "Alien", Overview: "During its return to the earth...", Genres: []string{"Horror", "Science Fiction"}, PosterPath: "/vfrQk5IPloGg1v9Rzbh2Eg3VGyM.jpg"},
		"ru": {Title: "Чужой", Overview: "В далеком будущем...", Genres: []string{"ужасы", "фантастика"}, PosterPath: "/zg4fcO8IuYRllK9wWVNqNlYpueL.jpg"},
	},
}

func TestTMDBDetails(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	x := torrent(1, "Heat.1995.1080p")
	x.TitleKey, x.Kind, x.KindLayer = "y:heat 1995", "movie", LayerRules
	_, _, err := s.Insert(ctx, x)
	require.NoError(t, err)
	ids, err := s.RecordSearch(ctx, TypeMovie, "alien", []Hit{alienHit}, t0)
	require.NoError(t, err)
	alien := ids[0]
	heat, _, err := s.RecordMatch(ctx, "y:heat 1995", Found{Hit: &Hit{Type: TypeMovie, TMDBID: 949}, Confidence: 1, Layer: LayerTMDB}, t0.Add(time.Hour))
	require.NoError(t, err)

	due, err := s.WithoutDetails(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, []Due{{Work: heat, Type: TypeMovie, TMDBID: 949}, {Work: alien, Type: TypeMovie, TMDBID: 348}}, due,
		"a work with releases first, though it is newer")
	due, err = s.WithoutDetails(ctx, 1)
	require.NoError(t, err)
	require.Len(t, due, 1)

	require.NoError(t, s.RecordDetails(ctx, alien, alienDetails, []ExternalID{{ProviderIMDb, "tt0078748"}, {ProviderTMDB, "348"}}, t0.Add(2*time.Hour)))
	ws, err := s.Works(ctx, []int64{alien})
	require.NoError(t, err)
	require.Equal(t, Work{ID: alien, TMDBID: 348, Popularity: 12, Details: alienDetails, IDs: []ExternalID{{ProviderIMDb, "tt0078748"}},
		DetailsAt: t0.Add(2 * time.Hour)}, ws[alien], "the details replace the search answer's texts; the TMDB ID is the work's own")
	w := ws[alien]
	require.Equal(t, Text{Title: "Чужой", Overview: "В далеком будущем...", Genres: []string{"ужасы", "фантастика"}, PosterPath: "/zg4fcO8IuYRllK9wWVNqNlYpueL.jpg"},
		w.Text("ru"), "the poster of the language")
	found, err := s.FindWorks(ctx, "чужой", "")
	require.NoError(t, err)
	require.Equal(t, []int64{alien}, found, "the titles of the details are searched")

	partial := Details{Type: "movie", OriginalTitle: "Heat", Runtime: 170, PosterPath: "/w.jpg", Texts: map[string]Text{
		"en": {Title: "Heat", Overview: "Obsessive master thief Neil McCauley...", Genres: []string{"Action", "Crime"}, PosterPath: "/h.jpg"},
		"ru": {Title: "Схватка"},
	}}
	require.Equal(t, Text{Title: "Схватка", Overview: "Obsessive master thief Neil McCauley...", Genres: []string{"Action", "Crime"}, PosterPath: "/h.jpg"}, partial.Text("ru"),
		"a field missing in the language comes from the other, the poster too")
	require.Equal(t, Text{Title: "Heat", Overview: "Obsessive master thief Neil McCauley...", Genres: []string{"Action", "Crime"}, PosterPath: "/h.jpg"}, partial.Text("de"),
		"an unknown language takes what there is")
	require.Equal(t, Text{Title: "Nameless", PosterPath: "/n.jpg"}, (&Details{OriginalTitle: "Nameless", PosterPath: "/n.jpg", Texts: map[string]Text{"en": {}}}).Text("ru"),
		"the original title and the work's poster are the last resort")
	require.Equal(t, Text{}, (*Details)(nil).Text("ru"))

	require.NoError(t, s.RecordDetails(ctx, heat, Details{}, nil, t0.Add(3*time.Hour)))
	due, err = s.WithoutDetails(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, due, "refused details mark the work fetched")
	ws, err = s.Works(ctx, []int64{heat})
	require.NoError(t, err)
	require.Equal(t, t0.Add(3*time.Hour), ws[heat].DetailsAt)

	_, err = s.w.ExecContext(ctx, "DELETE FROM tmdb_works WHERE id = ?", alien)
	require.NoError(t, err)
	var texts int
	require.NoError(t, s.r.QueryRowContext(ctx, "SELECT count(*) FROM tmdb_texts").Scan(&texts))
	require.Zero(t, texts, "texts go with their work")
}

func TestSearchCache(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	_, ok, err := s.CachedSearch(ctx, TypeMovie, "alien", t0.Add(-SearchTTL))
	require.NoError(t, err)
	require.False(t, ok, "nothing recorded")

	ids, err := s.RecordSearch(ctx, TypeMovie, "alien", []Hit{alienHit, {Type: TypeMovie, TMDBID: 679, Lang: "en", Title: "Aliens", Year: 1986, Popularity: 9}}, t0)
	require.NoError(t, err)
	require.Len(t, ids, 2)
	got, ok, err := s.CachedSearch(ctx, TypeMovie, "alien", t0.Add(time.Hour-SearchTTL))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ids, got, "in hit order")
	_, ok, err = s.CachedSearch(ctx, TypeMovie, "alien", t0)
	require.NoError(t, err)
	require.False(t, ok, "stale once older than the bound")
	_, ok, err = s.CachedSearch(ctx, TypeTV, "alien", t0.Add(-SearchTTL))
	require.NoError(t, err)
	require.False(t, ok, "per type")

	ids2, err := s.RecordSearch(ctx, TypeMovie, "alien", []Hit{{Type: TypeMovie, TMDBID: 348, Lang: "ru", Title: "Чужой", Popularity: 30}}, t0.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, ids[:1], ids2, "the same work")
	ws, err := s.Works(ctx, ids)
	require.NoError(t, err)
	require.Equal(t, 30.0, ws[ids[0]].Popularity, "the latest popularity")
	require.Equal(t, "Alien", ws[ids[0]].Texts["en"].Title)
	require.Equal(t, "Чужой", ws[ids[0]].Texts["ru"].Title, "a text per language answered in")
	require.Equal(t, "/s.jpg", ws[ids[0]].PosterPath, "a later answer without a poster keeps the one before")
	require.True(t, ws[ids[1]].DetailsAt.IsZero())
}

func TestFillFeaturesAndMarks(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	s, db := openAt(t, path)
	tr := torrent(1, "Severance.S02E05.1080p.WEB-DL.Rus", "Severance.S02E05.mkv")
	tr.Audio = []Audio{{Language: "ru", Marker: true}}
	_, _, err := s.Insert(ctx, tr)
	require.NoError(t, err)
	silo := torrent(2, "Silo.S03E10.1080p.rus.LostFilm.TV.mkv", "Silo.S03E10.1080p.rus.LostFilm.TV.mkv")
	silo.Audio = []Audio{{Language: "ru", Marker: true, Mark: "rus"}}
	_, _, err = s.Insert(ctx, silo)
	require.NoError(t, err)
	r, err := s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.Zero(t, r.Episode, "Insert takes the features it is given")
	require.Empty(t, r.Audio[0].Mark)
	for _, k := range []string{featuresFilled, marksFilled + "ru"} {
		_, err := db.W.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", k)
		require.NoError(t, err)
	}
	require.NoError(t, db.Close())

	s, db = openAt(t, path)
	defer db.Close()
	r, err = s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.Equal(t, 2, r.Season)
	require.Equal(t, 5, r.Episode)
	require.Equal(t, "1080p", r.Quality)
	require.Equal(t, "Rus", r.Audio[0].Mark, "the mark of a stored marker")
	r, err = s.Get(ctx, hash(2))
	require.NoError(t, err)
	require.Equal(t, "LostFilm", r.Audio[0].Mark, "a stored mark is recomputed: the studio over the word")

	_, err = db.W.ExecContext(ctx, "UPDATE torrent_releases SET quality = NULL, episode = NULL")
	require.NoError(t, err)
	_, err = New(ctx, db, Options{Languages: []string{"ru"}})
	require.NoError(t, err)
	r, err = s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.Zero(t, r.Episode, "the backfill runs once")
}

func TestMigrateWorks(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	defer db.Close()
	old := fstest.MapFS{}
	for _, name := range []string{"001_init.sql", "002_layers.sql", "003_audio.sql", "004_titles.sql", "005_peers.sql", "006_tmdb.sql", "007_modules.sql",
		"008_tmdb_details.sql", "009_tmdb_posters.sql"} {
		b, err := migrations.ReadFile("migrations/" + name)
		require.NoError(t, err)
		old[name] = &fstest.MapFile{Data: b}
	}
	require.NoError(t, db.Migrate(ctx, "store", old))
	release := func(id int, name, key string) []string {
		return []string{
			`INSERT INTO torrent_releases (id, infohash, name, size, file_count, discovery, first_seen, last_seen, kind, kind_layer, title_key, work_key)
				VALUES (` + itoa(id) + `, x'0` + itoa(id) + `00000000000000000000000000000000000000', '` + name + `', 1073741824, 1, 'sample', 100, 100, 'movie', 'rules', '` + key + `', '` + key + `')`,
			"INSERT INTO torrent_releases_fts (rowid, name, paths) VALUES (" + itoa(id) + ", '" + name + "', '')",
		}
	}
	var stmts []string
	stmts = append(stmts, release(1, "Dune.Part.Two.2024.1080p.Rus", "y:dune part two 2024")...)
	stmts = append(stmts, release(2, "Дюна: Часть вторая (2024) WEB-DL 720p", "y:дюна часть вторая 2024")...)
	stmts = append(stmts, release(3, "Heat.1995.1080p", "y:heat 1995")...)
	stmts = append(stmts, release(4, "Ronin.1998.1080p", "y:ronin 1998")...)
	stmts = append(stmts,
		"INSERT INTO torrent_files (torrent_id, idx, path, size) VALUES (1, 0, 'Dune.mkv', 1073741824)",
		"INSERT INTO torrent_audio (torrent_id, language, marker, script_hint, probability, layer) VALUES (1, 'ru', 1, 0, 1.0, 'rules')",
		`INSERT INTO tmdb_works (title_key, year, confidence, layer, matched_at, type, original_title, poster_path, rating, votes, runtime, countries, language, details_at)
			VALUES ('y:dune part two 2024', 2024, 1, 'tmdb', 100, 'movie', 'Dune: Part Two', '/d.jpg', 8.1, 7000, 167, '["US"]', 'en', 200)`,
		"INSERT INTO tmdb_ids (title_key, provider, id) VALUES ('y:dune part two 2024', 'tmdb', '693134'), ('y:dune part two 2024', 'imdb', 'tt15239678')",
		`INSERT INTO tmdb_texts (title_key, lang, title, overview, genres, poster_path) VALUES
			('y:dune part two 2024', 'en', 'Dune: Part Two', 'Paul.', '["Science Fiction"]', '/d.jpg'),
			('y:dune part two 2024', 'ru', 'Дюна: Часть вторая', 'Пол.', '["фантастика"]', '/r.jpg')`,
		"INSERT INTO tmdb_works (title_key, year, confidence, layer, matched_at) VALUES ('y:дюна часть вторая 2024', 2024, 0.7, 'jev', 150)",
		"INSERT INTO tmdb_ids (title_key, provider, id) VALUES ('y:дюна часть вторая 2024', 'tmdb', '693134')",
		"INSERT INTO tmdb_works (title_key, matched_at, error) VALUES ('y:heat 1995', 120, 'tmdb /3/search/movie: 404 Not Found')",
		"INSERT INTO stat_top (kind, window, voice, rank, work_key, peers, releases, computed_at) VALUES ('movie', '7d', 'any', 1, 'y:dune part two 2024', 5, 1, 100)",
	)
	for _, stmt := range stmts {
		_, err := db.W.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	s, err := New(ctx, db, Options{Languages: []string{"ru"}, TMDB: true})
	require.NoError(t, err)
	var works int
	require.NoError(t, db.R.QueryRowContext(ctx, "SELECT count(*) FROM tmdb_works").Scan(&works))
	require.Equal(t, 1, works, "two title keys matched to one TMDB ID are one work")
	dune, err := s.WorkByTMDB(ctx, TypeMovie, 693134)
	require.NoError(t, err)
	ws, err := s.Works(ctx, []int64{dune})
	require.NoError(t, err)
	require.Equal(t, Work{ID: dune, TMDBID: 693134, IDs: []ExternalID{{ProviderIMDb, "tt15239678"}}, DetailsAt: time.Unix(200, 0).UTC(),
		Details: Details{Type: TypeMovie, OriginalTitle: "Dune: Part Two", Year: 2024, PosterPath: "/d.jpg", Rating: 8.1, Votes: 7000, Runtime: 167,
			Countries: []string{"US"}, Language: "en", Texts: map[string]Text{
				"en": {Title: "Dune: Part Two", Overview: "Paul.", Genres: []string{"Science Fiction"}, PosterPath: "/d.jpg"},
				"ru": {Title: "Дюна: Часть вторая", Overview: "Пол.", Genres: []string{"фантастика"}, PosterPath: "/r.jpg"},
			}}}, ws[dune], "the details, texts and IDs of the row that had them")

	for b, want := range map[byte]int64{1: dune, 2: dune, 3: 0, 4: 0} {
		r, err := s.Get(ctx, hash(b))
		require.NoError(t, err)
		require.Equal(t, want, r.Work, "release %d", b)
	}
	r, err := s.Get(ctx, hash(2))
	require.NoError(t, err)
	require.Equal(t, 0.7, r.Match.Confidence, "each key keeps its own match")
	require.Equal(t, LayerJev, r.Match.Layer)
	require.Equal(t, time.Unix(150, 0).UTC(), r.Match.MatchedAt)
	require.Equal(t, "720p", r.Quality, "the features are derived")
	r, err = s.Get(ctx, hash(3))
	require.NoError(t, err)
	require.Equal(t, Match{Error: "tmdb /3/search/movie: 404 Not Found", MatchedAt: time.Unix(120, 0).UTC()}, r.Match, "an unmatched key keeps its row")
	r, err = s.Get(ctx, hash(4))
	require.NoError(t, err)
	require.Equal(t, Match{}, r.Match, "a key never looked up has none")
	r, err = s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.Equal(t, "Rus", r.Audio[0].Mark, "the marker's mark is derived")
	require.Equal(t, "1080p", r.Quality)

	var top int
	require.NoError(t, db.R.QueryRowContext(ctx, "SELECT count(*) FROM stat_top").Scan(&top))
	require.Zero(t, top, "the tops are recomputed")
	found, err := s.FindWorks(ctx, "дюна", "")
	require.NoError(t, err)
	require.Equal(t, []int64{dune}, found, "titles are searchable")
	keys, err := s.UnmatchedTitles(ctx, nil, time.Unix(121, 0), 10)
	require.NoError(t, err)
	require.Equal(t, []string{"y:ronin 1998", "y:heat 1995"}, titleKeys(keys), "the unmatched key is due again in time")

	var columns []string
	require.NoError(t, s.each(ctx, func(rows *sql.Rows) error {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		columns = append(columns, name)
		return nil
	}, "SELECT name FROM pragma_table_info('torrent_releases') WHERE name IN ('work_key', 'work', 'episode', 'quality') ORDER BY cid"))
	require.Equal(t, []string{"work", "episode", "quality"}, columns)
	rows, err := db.R.QueryContext(ctx, "PRAGMA foreign_key_check")
	require.NoError(t, err)
	require.False(t, rows.Next(), "no foreign key is broken")
	require.NoError(t, rows.Close())

	fresh, err := database.Open(ctx, filepath.Join(t.TempDir(), "fresh.db"))
	require.NoError(t, err)
	defer fresh.Close()
	_, err = New(ctx, fresh, Options{})
	require.NoError(t, err)
	require.Equal(t, schema(t, fresh), schema(t, db), "a fresh database has the same schema as a migrated one")
}

// itoa is the decimal digit of n, 0 to 9.
func itoa(n int) string {
	return string(rune('0' + n))
}

func titleKeys(ks []TitleKey) []string {
	var out []string
	for _, k := range ks {
		out = append(out, k.Key)
	}
	return out
}
