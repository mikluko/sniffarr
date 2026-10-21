// Package store keeps torrents, rejected infohashes, classifications and
// peer sketches, with full-text search over names and file paths; the
// movies and series of TMDB that title keys match, with or without
// releases, and the tops of them by peers; and the work queue of
// infohashes awaiting their metadata: sightings enqueue them, fetchers
// lease them, and a result completes or fails them.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/mikluko/sniffarr/internal/database"
	"github.com/mikluko/sniffarr/internal/sketch"
)

// ErrNotFound is returned for an infohash that is not stored.
var ErrNotFound = errors.New("not found")

// Hash is a BitTorrent v1 infohash.
type Hash [20]byte

// DefaultMaxFiles is the default of Options.MaxFiles.
const DefaultMaxFiles = 20

// Options tune a Store. Zero fields take the default named in their doc.
type Options struct {
	// MaxFiles is how many files of a torrent are stored, the largest ones;
	// DefaultMaxFiles. The file count stays true.
	MaxFiles int
	// Languages are the audio languages Release.Audio holds and Unclassified
	// selects by; none leaves both without audio.
	Languages []string
	// TMDB has Release.Title read.
	TMDB bool
	// Meter records the store's metrics; the global MeterProvider when nil.
	Meter metric.MeterProvider
}

// Store keeps its tables in a shared database. It is safe for concurrent
// use.
type Store struct {
	db      *database.DB
	w, r    *sql.DB
	opts    Options
	deleted metric.Int64Counter

	keyMu     sync.Mutex
	key       sketch.Key
	keySet    bool
	nodeID    [20]byte
	nodeIDSet bool
}

//go:embed migrations/*.sql
var migrations embed.FS

// migrate applies the migrations db has not applied yet.
func migrate(ctx context.Context, db *database.DB) error {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	if err := db.Migrate(ctx, "store", sub); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// New returns the Store in db, applying any pending migrations, and, once
// per database, deriving the episode and quality of stored torrents from
// their names and the marks of their audio markers.
func New(ctx context.Context, db *database.DB, opts Options) (*Store, error) {
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = DefaultMaxFiles
	}
	if opts.Meter == nil {
		opts.Meter = otel.GetMeterProvider()
	}
	if err := migrate(ctx, db); err != nil {
		return nil, err
	}
	deleted, err := opts.Meter.Meter("github.com/mikluko/sniffarr/internal/store").Int64Counter("sniffarr.store.retention.deletions",
		metric.WithUnit("{torrent}"), metric.WithDescription("Stored torrents deleted for not being seen within the retention period."))
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, w: db.W, r: db.R, opts: opts, deleted: deleted}
	if err := s.fillFeatures(ctx); err != nil {
		return nil, fmt.Errorf("release features: %w", err)
	}
	if err := s.fillMarks(ctx); err != nil {
		return nil, fmt.Errorf("audio marks: %w", err)
	}
	return s, nil
}

// Sweep deletes, with their files and search entries, the torrents last seen
// before before, in transactions of at most batch torrents each, and returns
// how many it deleted.
func (s *Store) Sweep(ctx context.Context, before time.Time, batch int) (int, error) {
	total := 0
	for {
		var n int
		err := s.tx(ctx, func(tx *sql.Tx) error {
			const ids = "SELECT id FROM torrent_releases WHERE last_seen < ? ORDER BY last_seen LIMIT ?"
			if _, err := tx.ExecContext(ctx, "DELETE FROM torrent_releases_fts WHERE rowid IN ("+ids+")", before.Unix(), batch); err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, "DELETE FROM torrent_releases WHERE id IN ("+ids+")", before.Unix(), batch)
			if err != nil {
				return err
			}
			d, err := res.RowsAffected()
			n = int(d)
			return err
		})
		if err != nil {
			return total, err
		}
		total += n
		s.deleted.Add(ctx, int64(n))
		if n < batch {
			return total, nil
		}
	}
}

func (s *Store) tx(ctx context.Context, f func(*sql.Tx) error) error {
	return s.db.Tx(ctx, f)
}

// each calls f on every row of the query.
func (s *Store) each(ctx context.Context, f func(*sql.Rows) error, query string, args ...any) error {
	rows, err := s.r.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := f(rows); err != nil {
			return err
		}
	}
	return errors.Join(rows.Err(), rows.Close())
}

// and returns " AND cond" or "".
func and(cond string) string {
	if cond == "" {
		return ""
	}
	return " AND " + cond
}

func null(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func nullFloat(valid bool, f float64) any {
	if !valid {
		return nil
	}
	return f
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
