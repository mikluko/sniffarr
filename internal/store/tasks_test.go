package store

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"sarak.sh/mikluko/libarr/database"
)

// migrateBefore applies to db the store's migrations whose paths sort
// before path, such as "migrations/031".
func migrateBefore(t *testing.T, db *database.DB, path string) {
	t.Helper()
	old := fstest.MapFS{}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	require.NoError(t, err)
	for _, name := range names {
		if name >= path {
			break
		}
		b, err := migrations.ReadFile(name)
		require.NoError(t, err)
		old[strings.TrimPrefix(name, "migrations/")] = &fstest.MapFile{Data: b}
	}
	require.NoError(t, db.Migrate(context.Background(), "store", old))
}

func TestPriorityOf(t *testing.T) {
	for d, p := range map[string]int{Arr: PriorityArr, Announce: PriorityAnnounce, Sample: PrioritySample} {
		require.Equal(t, p, PriorityOf(d))
		require.Equal(t, d, DiscoveryOf(p))
	}
	require.Equal(t, Arr, DiscoveryOf(PriorityRequest), "a fetch asked for at P0 serves Radarr and Sonarr")
}

func TestMigrateFeedPriority(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{Service: "sniffarr"})
	require.NoError(t, err)
	defer db.Close()
	migrateBefore(t, db, "migrations/030")
	for p := range 5 {
		_, err := db.W.ExecContext(ctx, "INSERT INTO task_queue (kind, infohash, priority, queued_at) VALUES ('fetch', ?, ?, 0)", []byte{byte(p)}, p)
		require.NoError(t, err)
	}
	for _, stmt := range []string{
		"INSERT INTO feed_items (source, infohash, title, url, category, size, published_at, recorded_at) VALUES ('x', x'01', 'Alien', '', '', 1, 0, 0)",
		"INSERT INTO crawl_state (source, url, created_at, updated_at, status, etag, last_modified, error) VALUES ('x', '', 0, 0, 200, '', '', '')",
		"INSERT INTO torrent_journal (infohash, at, kind, outcome) VALUES (x'01', 0, 'feed', 'queued'), (x'01', 0, 'sighted', 'queued')",
	} {
		_, err := db.W.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	migrateBefore(t, db, "migrations/031")
	got := map[byte]int{}
	rows, err := db.R.QueryContext(ctx, "SELECT infohash, priority FROM task_queue")
	require.NoError(t, err)
	for rows.Next() {
		var h []byte
		var p int
		require.NoError(t, rows.Scan(&h, &p))
		got[h[0]] = p
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Equal(t, map[byte]int{0: PriorityRequest, 1: PriorityArr, 2: PriorityAnnounce, 3: PriorityAnnounce, 4: PrioritySample}, got,
		"the feed priority folds into announce, the ones behind it move up")
	var kinds []string
	rows, err = db.R.QueryContext(ctx, "SELECT kind FROM torrent_journal")
	require.NoError(t, err)
	for rows.Next() {
		var k string
		require.NoError(t, rows.Scan(&k))
		kinds = append(kinds, k)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Equal(t, []string{JournalSighted}, kinds)
	tables := schema(t, db)
	require.NotContains(t, tables, "table feed_items")
	require.NotContains(t, tables, "table crawl_state")
}
