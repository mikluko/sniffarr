package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
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
	_, _, err := s.Known(ctx, []Hash{hash(1)}, t0.Add(10*time.Hour))
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
	require.NoError(t, s.r.QueryRowContext(ctx, "SELECT count(*) FROM torrent_files").Scan(&files))
	require.Equal(t, 2, files, "and their files go with them")
	var audio int
	require.NoError(t, s.r.QueryRowContext(ctx, "SELECT count(*) FROM torrent_audio").Scan(&audio))
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
	require.NoError(t, db.R.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('torrent_releases') WHERE name LIKE 'russian%'").Scan(&dropped))
	require.Zero(t, dropped, "the old columns are gone")
	p, err := s.Unclassified(ctx, 0, 10, 1)
	require.NoError(t, err)
	require.Len(t, p, 1, "3, unexamined with a hint")
	require.EqualValues(t, 3, p[0].ID)
}

// queueMigrations are the migrations of the queue component as the databases
// before 007 applied them.
var queueMigrations = fstest.MapFS{
	"001_queue.sql": {Data: []byte(`CREATE TABLE queue (
    id          INTEGER PRIMARY KEY,
    infohash    BLOB    NOT NULL UNIQUE,
    priority    INTEGER NOT NULL,
    attempts    INTEGER NOT NULL DEFAULT 0,
    not_before  INTEGER NOT NULL DEFAULT 0,
    lease_owner INTEGER,
    lease_until INTEGER
);
CREATE INDEX queue_order ON queue (priority, id) WHERE lease_owner IS NULL;
CREATE INDEX queue_lease_owner ON queue (lease_owner) WHERE lease_owner IS NOT NULL;
CREATE INDEX queue_lease_until ON queue (lease_until) WHERE lease_until IS NOT NULL;`)},
	"002_bounds.sql": {Data: []byte(`ALTER TABLE queue ADD COLUMN queued_at INTEGER NOT NULL DEFAULT 0;
UPDATE queue SET queued_at = CAST(unixepoch('subsec') * 1000 AS INTEGER);
CREATE INDEX queue_queued_at ON queue (queued_at) WHERE lease_owner IS NULL;
CREATE INDEX queue_evict ON queue (priority DESC, id) WHERE lease_owner IS NULL;
CREATE TABLE dead (
    infohash BLOB    PRIMARY KEY,
    until    INTEGER NOT NULL
) WITHOUT ROWID;
CREATE INDEX dead_until ON dead (until);`)},
}

// schema returns the type and name of every object in db's sqlite_master.
func schema(t *testing.T, db *database.DB) []string {
	t.Helper()
	rows, err := db.R.QueryContext(context.Background(), "SELECT type || ' ' || name FROM sqlite_master ORDER BY type, name")
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestMigrateModules(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	defer db.Close()
	old := fstest.MapFS{}
	for _, name := range []string{"001_init.sql", "002_layers.sql", "003_audio.sql", "004_titles.sql", "005_peers.sql", "006_tmdb.sql"} {
		b, err := migrations.ReadFile("migrations/" + name)
		require.NoError(t, err)
		old[name] = &fstest.MapFile{Data: b}
	}
	require.NoError(t, db.Migrate(ctx, "store", old))
	require.NoError(t, db.Migrate(ctx, "queue", queueMigrations))
	for _, stmt := range []string{
		`INSERT INTO torrents (id, infohash, name, size, file_count, discovery, first_seen, last_seen, kind, kind_layer, title_key)
			VALUES (1, x'0100000000000000000000000000000000000000', 'Alien.1979.1080p.BluRay', 1073741824, 1, 'sample', 100, 100, 'movie', 'rules', 'y:alien 1979')`,
		"INSERT INTO torrents_fts (rowid, name, paths) VALUES (1, 'Alien.1979.1080p.BluRay', 'Alien.1979.mkv')",
		"INSERT INTO files (torrent_id, idx, path, size) VALUES (1, 0, 'Alien.1979.mkv', 1073741824)",
		"INSERT INTO audio (torrent_id, language, marker, script_hint, probability, layer) VALUES (1, 'ru', 1, 0, 1.0, 'rules')",
		"INSERT INTO tmdb_works (title_key, year, confidence, layer, matched_at) VALUES ('y:alien 1979', 1979, 0.9, 'tmdb', 100)",
		"INSERT INTO tmdb_ids (title_key, provider, id) VALUES ('y:alien 1979', 'tmdb', '348')",
		"INSERT INTO peers_hour (torrent_id, hour, sketch) VALUES (1, 3600, x'0102')",
		"INSERT INTO peers_day (torrent_id, day, sketch) VALUES (1, 0, x'0102')",
		"INSERT INTO top (kind, window, voice, rank, work_key, peers, releases, computed_at) VALUES ('movie', '7d', 'any', 1, 'y:alien 1979', 5, 1, 100)",
		"INSERT INTO rejected (infohash, reason, rejected_at) VALUES (x'02', 'small', 100)",
		"INSERT INTO token_usage (day, tokens) VALUES ('2026-10-01', 500)",
		"INSERT INTO settings (key, value) VALUES ('peer_hash_key', x'00')",
		"INSERT INTO queue (infohash, priority, queued_at) VALUES (x'03', 1, 100000)",
		"INSERT INTO dead (infohash, until) VALUES (x'04', 200000)",
	} {
		_, err := db.W.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	s, err := New(ctx, db, Options{Languages: []string{"ru"}})
	require.NoError(t, err)
	q, err := NewQueue(ctx, db, QueueOptions{})
	require.NoError(t, err)

	names := schema(t, db)
	for _, name := range []string{"torrent_releases", "torrent_releases_fts", "torrent_files", "torrent_audio", "ingest_queue", "ingest_dead",
		"ingest_rejected", "ingest_token_usage", "stat_peers_by_hour", "stat_peers_by_day", "stat_top", "tmdb_works", "tmdb_ids", "settings",
		"torrent_releases_fts_data", "torrent_releases_fts_idx", "torrent_releases_fts_docsize", "torrent_releases_fts_config"} {
		require.Contains(t, names, "table "+name)
	}
	for _, name := range names {
		for _, gone := range []string{"torrents", "files", "audio", "queue", "dead", "rejected", "token_usage", "peers_hour", "peers_day", "top"} {
			require.NotEqual(t, "table "+gone, name)
			require.NotEqual(t, "index "+gone, name)
		}
		require.False(t, strings.HasPrefix(name, "table torrents_fts"), name)
	}
	wantIndexes := []string{
		"torrent_releases_unclassified", "torrent_releases_last_seen", "torrent_releases_title_key", "torrent_releases_background",
		"torrent_releases_titled", "torrent_releases_work_key", "torrent_releases_peers_week", "torrent_releases_size",
		"ingest_queue_order", "ingest_queue_lease_owner", "ingest_queue_lease_until", "ingest_queue_queued_at", "ingest_queue_evict",
		"ingest_dead_until", "stat_peers_by_hour_hour", "stat_peers_by_day_day", "stat_top_work", "tmdb_ids_id",
	}
	var gotIndexes []string
	require.NoError(t, s.each(ctx, func(rows *sql.Rows) error {
		var name, table string
		if err := rows.Scan(&name, &table); err != nil {
			return err
		}
		require.True(t, strings.HasPrefix(name, table+"_"), "%s is an index of %s", name, table)
		gotIndexes = append(gotIndexes, name)
		return nil
	}, "SELECT name, tbl_name FROM sqlite_master WHERE type = 'index' AND sql IS NOT NULL"))
	require.ElementsMatch(t, wantIndexes, gotIndexes)
	for _, table := range []string{"torrent_files", "torrent_audio", "stat_peers_by_hour", "stat_peers_by_day"} {
		var ddl string
		require.NoError(t, db.R.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE name = ?", table).Scan(&ddl))
		require.Contains(t, ddl, `REFERENCES "torrent_releases"`, table)
	}
	var version int
	require.NoError(t, db.R.QueryRowContext(ctx, "SELECT version FROM schema_migrations WHERE component = 'store'").Scan(&version))
	require.Equal(t, 7, version)
	var queueRows int
	require.NoError(t, db.R.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations WHERE component = 'queue'").Scan(&queueRows))
	require.Zero(t, queueRows, "the queue component is gone")
	rows, err := db.R.QueryContext(ctx, "PRAGMA foreign_key_check")
	require.NoError(t, err)
	require.False(t, rows.Next(), "no foreign key is broken")
	require.NoError(t, rows.Close())

	r, err := s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.Equal(t, "Alien.1979.1080p.BluRay", r.Name)
	require.Equal(t, []Audio{{Language: "ru", Marker: true, Probability: new(1.0), Layer: LayerRules}}, r.Audio)
	got, err := s.Search(ctx, Query{Text: "alien", Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1, "the search index survived")
	files, err := s.files(ctx, 1, 10)
	require.NoError(t, err)
	require.Equal(t, []rules.File{{Path: "Alien.1979.mkv", Size: 1 << 30}}, files)
	tokens, err := s.TokensUsed(ctx, "2026-10-01")
	require.NoError(t, err)
	require.EqualValues(t, 500, tokens)
	n, err := q.Len(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	for table, want := range map[string]int{"ingest_dead": 1, "ingest_rejected": 1, "stat_peers_by_hour": 1, "stat_peers_by_day": 1,
		"stat_top": 1, "tmdb_works": 1, "tmdb_ids": 1, "settings": 1} {
		var n int
		require.NoError(t, db.R.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n))
		require.Equal(t, want, n, table)
	}

	_, err = db.W.ExecContext(ctx, "DELETE FROM torrent_releases")
	require.NoError(t, err)
	for _, table := range []string{"torrent_files", "torrent_audio", "stat_peers_by_hour", "stat_peers_by_day"} {
		var n int
		require.NoError(t, db.R.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n))
		require.Zero(t, n, "%s cascades from torrent_releases", table)
	}

	fresh, err := database.Open(ctx, filepath.Join(t.TempDir(), "fresh.db"))
	require.NoError(t, err)
	defer fresh.Close()
	_, err = New(ctx, fresh, Options{})
	require.NoError(t, err)
	require.Equal(t, schema(t, fresh), names, "a fresh database has the same schema as a migrated one")
}
