// Package database opens the SQLite database that the store and the queue
// share, and applies each one's migrations.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"slices"

	_ "modernc.org/sqlite"
)

// DB is a SQLite database in WAL mode. W is its only writing connection; R
// is a pool for reads. It is safe for concurrent use.
type DB struct {
	W, R *sql.DB
}

// Open opens or creates the database at path.
func Open(ctx context.Context, path string) (*DB, error) {
	q := url.Values{"_pragma": {"journal_mode(WAL)", "busy_timeout(10000)", "foreign_keys(1)", "synchronous(NORMAL)"}}
	w, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode()+"&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	r, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		w.Close()
		return nil, err
	}
	db := &DB{W: w, R: r}
	_, err = w.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (component TEXT PRIMARY KEY, version INTEGER NOT NULL) WITHOUT ROWID")
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Close closes the database.
func (db *DB) Close() error {
	return errors.Join(db.R.Close(), db.W.Close())
}

// Migrate applies the *.sql files of migrations that component has not
// applied yet, in name order, each in a transaction of its own.
func (db *DB) Migrate(ctx context.Context, component string, migrations fs.FS) error {
	names, err := fs.Glob(migrations, "*.sql")
	if err != nil {
		return err
	}
	slices.Sort(names)
	var version int
	err = db.R.QueryRowContext(ctx, "SELECT version FROM schema_migrations WHERE component = ?", component).Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	for i := version; i < len(names); i++ {
		body, err := fs.ReadFile(migrations, names[i])
		if err != nil {
			return err
		}
		err = db.Tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, string(body)); err != nil {
				return fmt.Errorf("%s %s: %w", component, names[i], err)
			}
			_, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (component, version) VALUES (?, ?) ON CONFLICT (component) DO UPDATE SET version = excluded.version", component, i+1)
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Tx runs f in a write transaction, committing when f returns nil.
func (db *DB) Tx(ctx context.Context, f func(*sql.Tx) error) error {
	tx, err := db.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := f(tx); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}
