package database

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func TestMigrate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	a := fstest.MapFS{"001.sql": {Data: []byte("CREATE TABLE a (x INTEGER)")}}
	b := fstest.MapFS{"001.sql": {Data: []byte("CREATE TABLE b (x INTEGER)")}}
	require.NoError(t, db.Migrate(ctx, "a", a))
	require.NoError(t, db.Migrate(ctx, "b", b), "components count versions apart")
	require.NoError(t, db.Close())

	db, err = Open(ctx, path, Options{})
	require.NoError(t, err)
	defer db.Close()
	a["002.sql"] = &fstest.MapFile{Data: []byte("INSERT INTO a VALUES (1)")}
	require.NoError(t, db.Migrate(ctx, "a", a), "001 is not applied again")
	require.NoError(t, db.Migrate(ctx, "a", a))
	var n int
	require.NoError(t, db.R.QueryRowContext(ctx, "SELECT count(*) FROM a").Scan(&n))
	require.Equal(t, 1, n)
}

func TestMigrateLogs(t *testing.T) {
	ctx := context.Background()
	var buf bytes.Buffer
	db, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"), Options{Log: slog.New(slog.NewJSONHandler(&buf, nil))})
	require.NoError(t, err)
	defer db.Close()
	m := fstest.MapFS{
		"001.sql": {Data: []byte("CREATE TABLE a (x INTEGER)")},
		"002.sql": {Data: []byte("INSERT INTO a VALUES (1)")},
	}
	require.NoError(t, db.Migrate(ctx, "a", m))
	require.NoError(t, db.Migrate(ctx, "a", m))

	type record struct {
		Msg, Component, Migration string
		Took                      *int64
	}
	var got []record
	dec := json.NewDecoder(&buf)
	for dec.More() {
		var r record
		require.NoError(t, dec.Decode(&r))
		got = append(got, r)
	}
	require.Len(t, got, 4, "a migration already applied is not logged")
	for i, name := range []string{"001.sql", "002.sql"} {
		require.Equal(t, record{Msg: "migrating", Component: "a", Migration: name}, got[2*i])
		require.Equal(t, "migrated", got[2*i+1].Msg)
		require.Equal(t, name, got[2*i+1].Migration)
		require.NotNil(t, got[2*i+1].Took)
	}
}
