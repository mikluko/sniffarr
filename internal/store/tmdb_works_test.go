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

func TestTMDB(t *testing.T) {
	ctx := context.Background()
	s := open(t)
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

	alien := Title{Year: 1979, Confidence: 1, Layer: LayerTMDB, IDs: []ExternalID{{ProviderTMDB, "348"}, {ProviderIMDb, "tt0078748"}}}
	require.NoError(t, s.RecordTitle(ctx, "y:alien 1979", alien, t0))
	require.NoError(t, s.RecordTitle(ctx, "s:severance", Title{}, t0))
	keys, err = s.UnmatchedTitles(ctx, nil, t0, 10)
	require.NoError(t, err)
	require.Empty(t, keys, "a failed match is not retried before its time")
	keys, err = s.UnmatchedTitles(ctx, nil, t0.Add(time.Second), 10)
	require.NoError(t, err)
	require.Equal(t, []string{"s:severance"}, titleKeys(keys), "then it is, and a match never is")

	r, err := s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.Equal(t, Title{}, r.Title, "without Options.TMDB")
	s.opts.TMDB = true
	r, err = s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.Equal(t, "y:alien 1979", r.TitleKey)
	require.Equal(t, Title{Year: 1979, Confidence: 1, Layer: LayerTMDB, IDs: []ExternalID{{ProviderIMDb, "tt0078748"}, {ProviderTMDB, "348"}}, MatchedAt: t0}, r.Title)
	r, err = s.Get(ctx, hash(3))
	require.NoError(t, err)
	require.Equal(t, Title{MatchedAt: t0}, r.Title, "looked up, nothing found")

	rs, err := s.Search(ctx, Query{ID: &ExternalID{ProviderIMDb, "tt0078748"}, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rs, 2, "every release of the key")
	for _, r := range rs {
		require.Equal(t, "y:alien 1979", r.TitleKey)
		require.Len(t, r.Title.IDs, 2)
	}
	rs, err = s.Search(ctx, Query{ID: &ExternalID{ProviderTMDB, "tt0078748"}, Limit: 10})
	require.NoError(t, err)
	require.Empty(t, rs, "the ID of another provider")

	require.NoError(t, s.RecordTitle(ctx, "y:alien 1979", Title{Year: 1979, Confidence: 0.6, Layer: LayerJev, IDs: []ExternalID{{ProviderTMDB, "8077"}}}, t0))
	r, err = s.Get(ctx, hash(2))
	require.NoError(t, err)
	require.Equal(t, []ExternalID{{ProviderTMDB, "8077"}}, r.Title.IDs, "a match replaces the one before")

	_, err = s.w.ExecContext(ctx, "DELETE FROM tmdb_works WHERE title_key = 'y:alien 1979'")
	require.NoError(t, err)
	var ids int
	require.NoError(t, s.r.QueryRowContext(ctx, "SELECT count(*) FROM tmdb_ids").Scan(&ids))
	require.Zero(t, ids, "IDs go with their key")
	var ddl string
	require.NoError(t, s.r.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE name = 'tmdb_ids'").Scan(&ddl))
	require.Contains(t, ddl, `REFERENCES "tmdb_works"`, "the foreign key follows the renamed table")
}

// alienDetails are the details of Alien in both languages.
var alienDetails = Details{
	Type: "movie", OriginalTitle: "Alien", PosterPath: "/vfrQk5IPloGg1v9Rzbh2Eg3VGyM.jpg", Rating: 8.176, Votes: 16995, Runtime: 117,
	Countries: []string{"US", "GB"}, Language: "en",
	Texts: map[string]Text{
		"en": {Title: "Alien", Overview: "During its return to the earth...", Genres: []string{"Horror", "Science Fiction"}},
		"ru": {Title: "Чужой", Overview: "В далеком будущем...", Genres: []string{"ужасы", "фантастика"}},
	},
}

func TestTMDBDetails(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	s.opts.TMDB = true
	for _, tr := range []struct {
		b         byte
		name, key string
		kind      string
	}{
		{1, "Alien.1979.1080p.BluRay", "y:alien 1979", "movie"},
		{2, "Severance.S02E01", "s:severance", "series"},
		{3, "Heat.1995.1080p", "y:heat 1995", "movie"},
		{4, "Ronin.1998.1080p", "y:ronin 1998", "movie"},
	} {
		x := torrent(tr.b, tr.name)
		x.TitleKey, x.Kind, x.KindLayer, x.WorkKey = tr.key, tr.kind, LayerRules, tr.key
		x.SeenAt = t0.Add(time.Duration(tr.b) * time.Hour)
		_, _, err := s.Insert(ctx, x)
		require.NoError(t, err)
	}
	ids := func(id string) []ExternalID { return []ExternalID{{ProviderTMDB, id}} }
	require.NoError(t, s.RecordTitle(ctx, "s:severance", Title{Year: 2022, Confidence: 0.9, Layer: LayerTMDB, IDs: ids("95396")}, t0))
	require.NoError(t, s.RecordTitle(ctx, "y:heat 1995", Title{Year: 1995, Confidence: 1, Layer: LayerTMDB, IDs: ids("949")}, t0.Add(time.Hour)))
	require.NoError(t, s.RecordTitle(ctx, "y:ronin 1998", Title{}, t0))
	require.NoError(t, s.RecordTitle(ctx, "y:alien 1979", Title{Year: 1979, Confidence: 1, Layer: LayerTMDB, IDs: ids("348"), Details: &alienDetails}, t0.Add(2*time.Hour)))

	due, err := s.WithoutDetails(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, []Matched{{Key: "s:severance", Kind: "series", ID: 95396}, {Key: "y:heat 1995", Kind: "movie", ID: 949}}, due,
		"matched without details, oldest match first; a failed match and a match with details are not due")
	due, err = s.WithoutDetails(ctx, 1)
	require.NoError(t, err)
	require.Len(t, due, 1)

	r, err := s.Get(ctx, hash(1))
	require.NoError(t, err)
	want := alienDetails
	require.Equal(t, Title{Year: 1979, Confidence: 1, Layer: LayerTMDB, IDs: ids("348"), MatchedAt: t0.Add(2 * time.Hour), Details: &want}, r.Title)
	require.Equal(t, Text{Title: "Чужой", Overview: "В далеком будущем...", Genres: []string{"ужасы", "фантастика"}}, r.Title.Details.Text("ru"))
	require.Equal(t, alienDetails.Texts["en"], r.Title.Details.Text("en"))
	r, err = s.Get(ctx, hash(3))
	require.NoError(t, err)
	require.Nil(t, r.Title.Details, "not fetched yet")

	heat := Details{Type: "movie", OriginalTitle: "Heat", Runtime: 170, Texts: map[string]Text{
		"en": {Title: "Heat", Overview: "Obsessive master thief Neil McCauley...", Genres: []string{"Action", "Crime"}},
		"ru": {Title: "Схватка"},
	}}
	require.NoError(t, s.RecordDetails(ctx, "y:heat 1995", heat, []ExternalID{{ProviderIMDb, "tt0113277"}, {ProviderTMDB, "949"}}, t0.Add(3*time.Hour)))
	r, err = s.Get(ctx, hash(3))
	require.NoError(t, err)
	require.Equal(t, []ExternalID{{ProviderIMDb, "tt0113277"}, {ProviderTMDB, "949"}}, r.Title.IDs, "the IDs come with the details")
	require.Equal(t, &heat, r.Title.Details)
	require.Equal(t, Text{Title: "Схватка", Overview: "Obsessive master thief Neil McCauley...", Genres: []string{"Action", "Crime"}}, r.Title.Details.Text("ru"),
		"a field missing in the language comes from the other")
	require.Equal(t, Text{Title: "Heat", Overview: "Obsessive master thief Neil McCauley...", Genres: []string{"Action", "Crime"}}, r.Title.Details.Text("de"),
		"an unknown language takes what there is")
	require.Equal(t, Text{Title: "Nameless"}, (&Details{OriginalTitle: "Nameless"}).Text("ru"), "the original title is the last resort")
	require.Equal(t, Text{}, (*Details)(nil).Text("ru"))
	due, err = s.WithoutDetails(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, []Matched{{Key: "s:severance", Kind: "series", ID: 95396}}, due)

	require.NoError(t, s.RecordDetails(ctx, "s:severance", Details{}, nil, t0))
	r, err = s.Get(ctx, hash(2))
	require.NoError(t, err)
	require.Equal(t, &Details{}, r.Title.Details, "empty details are details all the same")
	due, err = s.WithoutDetails(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, due)

	require.NoError(t, s.RecordTitle(ctx, "y:alien 1979", Title{Year: 1979, Confidence: 0.7, Layer: LayerJev, IDs: ids("8077")}, t0.Add(4*time.Hour)))
	r, err = s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.Nil(t, r.Title.Details, "a new match drops the details of the one before")
	var texts int
	require.NoError(t, s.r.QueryRowContext(ctx, "SELECT count(*) FROM tmdb_texts WHERE title_key = 'y:alien 1979'").Scan(&texts))
	require.Zero(t, texts)
	due, err = s.WithoutDetails(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, []Matched{{Key: "y:alien 1979", Kind: "movie", ID: 8077}}, due, "and puts the key back in line")

	works, err := s.works(ctx, "movie", []string{"y:heat 1995", "y:ronin 1998"}, VoiceAny)
	require.NoError(t, err)
	require.Equal(t, &heat, works["y:heat 1995"].TMDB.Details, "a work carries the match of its title key")
	require.Equal(t, []ExternalID{{ProviderIMDb, "tt0113277"}, {ProviderTMDB, "949"}}, works["y:heat 1995"].TMDB.IDs)
	require.Equal(t, Title{MatchedAt: t0}, works["y:ronin 1998"].TMDB)
	s.opts.TMDB = false
	works, err = s.works(ctx, "movie", []string{"y:heat 1995"}, VoiceAny)
	require.NoError(t, err)
	require.Equal(t, Title{}, works["y:heat 1995"].TMDB, "without Options.TMDB")

	_, err = s.w.ExecContext(ctx, "DELETE FROM tmdb_works WHERE title_key = 'y:heat 1995'")
	require.NoError(t, err)
	require.NoError(t, s.r.QueryRowContext(ctx, "SELECT count(*) FROM tmdb_texts WHERE title_key = 'y:heat 1995'").Scan(&texts))
	require.Zero(t, texts, "texts go with their key")
}

func TestMigrateTMDBDetails(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	defer db.Close()
	old := fstest.MapFS{}
	for _, name := range []string{"001_init.sql", "002_layers.sql", "003_audio.sql", "004_titles.sql", "005_peers.sql", "006_tmdb.sql", "007_modules.sql"} {
		b, err := migrations.ReadFile("migrations/" + name)
		require.NoError(t, err)
		old[name] = &fstest.MapFile{Data: b}
	}
	require.NoError(t, db.Migrate(ctx, "store", old))
	for _, stmt := range []string{
		`INSERT INTO torrent_releases (id, infohash, name, size, file_count, discovery, first_seen, last_seen, kind, kind_layer, title_key, work_key)
			VALUES (1, x'0100000000000000000000000000000000000000', 'Alien.1979.1080p.BluRay', 1073741824, 1, 'sample', 100, 100, 'movie', 'rules', 'y:alien 1979', 'y:alien 1979')`,
		"INSERT INTO torrent_releases_fts (rowid, name, paths) VALUES (1, 'Alien.1979.1080p.BluRay', '')",
		"INSERT INTO tmdb_works (title_key, year, confidence, layer, matched_at) VALUES ('y:alien 1979', 1979, 1, 'tmdb', 100)",
		"INSERT INTO tmdb_ids (title_key, provider, id) VALUES ('y:alien 1979', 'tmdb', '348')",
		"INSERT INTO tmdb_works (title_key, matched_at) VALUES ('y:heat 1995', 100)",
	} {
		_, err := db.W.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	s, err := New(ctx, db, Options{TMDB: true})
	require.NoError(t, err)
	var columns []string
	require.NoError(t, s.each(ctx, func(rows *sql.Rows) error {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		columns = append(columns, name)
		return nil
	}, "SELECT name FROM pragma_table_info('tmdb_works') ORDER BY cid"))
	require.Equal(t, []string{"title_key", "year", "confidence", "layer", "matched_at", "error", "type", "original_title", "poster_path", "rating", "votes",
		"runtime", "status", "seasons", "countries", "language", "details_at"}, columns)
	var ddl string
	require.NoError(t, db.R.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE name = 'tmdb_texts'").Scan(&ddl))
	require.Contains(t, ddl, "REFERENCES tmdb_works (title_key) ON DELETE CASCADE")
	require.Contains(t, ddl, "PRIMARY KEY (title_key, lang)")
	require.Contains(t, ddl, "WITHOUT ROWID")

	due, err := s.WithoutDetails(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, []Matched{{Key: "y:alien 1979", Kind: "movie", ID: 348}}, due, "every match from before is due its details")
	r, err := s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.Equal(t, Title{Year: 1979, Confidence: 1, Layer: LayerTMDB, IDs: []ExternalID{{ProviderTMDB, "348"}}, MatchedAt: time.Unix(100, 0).UTC()}, r.Title)

	require.NoError(t, s.RecordDetails(ctx, "y:alien 1979", alienDetails, nil, time.Unix(200, 0)))
	var texts int
	require.NoError(t, db.R.QueryRowContext(ctx, "SELECT count(*) FROM tmdb_texts").Scan(&texts))
	require.Equal(t, 2, texts)
	_, err = db.W.ExecContext(ctx, "DELETE FROM tmdb_works")
	require.NoError(t, err)
	require.NoError(t, db.R.QueryRowContext(ctx, "SELECT count(*) FROM tmdb_texts").Scan(&texts))
	require.Zero(t, texts, "texts cascade from tmdb_works")
}

func titleKeys(ks []TitleKey) []string {
	var out []string
	for _, k := range ks {
		out = append(out, k.Key)
	}
	return out
}
