package database

import (
	"context"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func TestMigrate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(ctx, path)
	require.NoError(t, err)
	a := fstest.MapFS{"001.sql": {Data: []byte("CREATE TABLE a (x INTEGER)")}}
	b := fstest.MapFS{"001.sql": {Data: []byte("CREATE TABLE b (x INTEGER)")}}
	require.NoError(t, db.Migrate(ctx, "a", a))
	require.NoError(t, db.Migrate(ctx, "b", b), "components count versions apart")
	require.NoError(t, db.Close())

	db, err = Open(ctx, path)
	require.NoError(t, err)
	defer db.Close()
	a["002.sql"] = &fstest.MapFile{Data: []byte("INSERT INTO a VALUES (1)")}
	require.NoError(t, db.Migrate(ctx, "a", a), "001 is not applied again")
	require.NoError(t, db.Migrate(ctx, "a", a))
	var n int
	require.NoError(t, db.R.QueryRowContext(ctx, "SELECT count(*) FROM a").Scan(&n))
	require.Equal(t, 1, n)
}
