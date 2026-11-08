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
	"log/slog"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/mikluko/batcher"

	"sarak.sh/mikluko/sniffarr/internal/database"
	"sarak.sh/mikluko/sniffarr/internal/sketch"
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
	// ProbeBatch is the most LeaseProbe calls one search and one write
	// serve; DefaultProbeBatch. A batch fills only from calls waiting at
	// once, so more than the probers that may ask at once fills none.
	ProbeBatch int
	// Log records the LeaseProbe batches that fail; nothing is logged when
	// nil.
	Log *slog.Logger
}

// Store keeps its tables in a shared database. It is safe for concurrent
// use.
type Store struct {
	db             *database.DB
	w, r           *sql.DB
	opts           Options
	deleted        metric.Int64Counter
	journalDeleted metric.Int64Counter
	// backfillBatch is the most rows one Backfill transaction updates.
	backfillBatch int
	probes        *batcher.Batcher[*probeLease]
	log           *slog.Logger

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

// New returns the Store in db, applying any pending migrations; Backfill
// does the one-time backfills of stored torrents. Close stops it.
func New(ctx context.Context, db *database.DB, opts Options) (*Store, error) {
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = DefaultMaxFiles
	}
	if opts.ProbeBatch <= 0 {
		opts.ProbeBatch = DefaultProbeBatch
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Meter == nil {
		opts.Meter = otel.GetMeterProvider()
	}
	if err := migrate(ctx, db); err != nil {
		return nil, err
	}
	m := opts.Meter.Meter("sarak.sh/mikluko/sniffarr/internal/store")
	deleted, err := m.Int64Counter("sniffarr.store.retention.deletions",
		metric.WithUnit("{torrent}"), metric.WithDescription("Stored torrents deleted for not being seen within the retention period, by which period."))
	if err != nil {
		return nil, err
	}
	journalDeleted, err := m.Int64Counter("sniffarr.store.journal.deletions",
		metric.WithUnit("{entry}"), metric.WithDescription("Journal entries deleted for being older than the journal retention period."))
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, w: db.W, r: db.R, opts: opts, deleted: deleted, journalDeleted: journalDeleted, backfillBatch: backfillBatch, log: opts.Log}
	s.probes = s.newProbes(opts.ProbeBatch)
	return s, nil
}

// Swept is what one Sweep deleted.
type Swept struct {
	// Idle is how many torrents with no TMDB work were deleted for not being
	// seen since idleBefore.
	Idle int
	// Expired is how many torrents were deleted for not being seen since
	// before.
	Expired int
}

// idleIDs and expiredIDs select, given a time and a limit, the oldest
// torrents last seen before the time, those with no TMDB work and any.
const (
	idleIDs    = "SELECT id FROM torrent_releases WHERE work IS NULL AND last_seen < ? ORDER BY last_seen LIMIT ?"
	expiredIDs = "SELECT id FROM torrent_releases WHERE last_seen < ? ORDER BY last_seen LIMIT ?"
)

// Sweep deletes, with their files and search entries, the torrents with no
// TMDB work last seen before idleBefore, then every torrent last seen before
// before, in transactions of at most batch torrents each, and journals each
// as deleted at now. A torrent is never first seen after it is last seen,
// so none is deleted as idle before it has been stored for as long as
// idleBefore leaves it.
func (s *Store) Sweep(ctx context.Context, before, idleBefore, now time.Time, batch int) (Swept, error) {
	var sw Swept
	var err error
	sw.Idle, err = s.sweep(ctx, idleIDs, idleBefore, now, batch, "idle")
	if err != nil {
		return sw, err
	}
	sw.Expired, err = s.sweep(ctx, expiredIDs, before, now, batch, "expired")
	return sw, err
}

// sweep deletes the torrents ids selects, given before and batch, until it
// selects fewer than batch, and returns how many it deleted.
func (s *Store) sweep(ctx context.Context, ids string, before, now time.Time, batch int, class string) (int, error) {
	attr := metric.WithAttributes(attribute.String("sniffarr.store.retention.class", class))
	total := 0
	for {
		var n int
		err := s.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "DELETE FROM torrent_releases_fts WHERE rowid IN ("+ids+")", before.Unix(), batch); err != nil {
				return err
			}
			rows, err := tx.QueryContext(ctx, "DELETE FROM torrent_releases WHERE id IN ("+ids+") RETURNING infohash, name, last_seen", before.Unix(), batch)
			if err != nil {
				return err
			}
			defer rows.Close()
			type gone struct {
				hash     Hash
				name     string
				lastSeen int64
			}
			var deleted []gone
			for rows.Next() {
				var g gone
				var b []byte
				if err := rows.Scan(&b, &g.name, &g.lastSeen); err != nil {
					return err
				}
				g.hash = Hash(b)
				deleted = append(deleted, g)
			}
			if err := errors.Join(rows.Err(), rows.Close()); err != nil {
				return err
			}
			n = len(deleted)
			for _, g := range deleted {
				if err := journal(ctx, tx, g.hash, now, JournalDeleted, class, Detail("name", g.name, "last_seen", time.Unix(g.lastSeen, 0))); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return total, err
		}
		total += n
		s.deleted.Add(ctx, int64(n), attr)
		if n < batch {
			return total, nil
		}
	}
}

func (s *Store) tx(ctx context.Context, f func(*sql.Tx) error) error {
	return s.db.Tx(ctx, f)
}

// querier runs queries: a *sql.DB, a *sql.Conn or a *sql.Tx.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// each calls f on every row of the query.
func (s *Store) each(ctx context.Context, f func(*sql.Rows) error, query string, args ...any) error {
	return eachOn(ctx, s.r, f, query, args...)
}

// eachOn calls f on every row of the query on q.
func eachOn(ctx context.Context, q querier, f func(*sql.Rows) error, query string, args ...any) error {
	rows, err := q.QueryContext(ctx, query, args...)
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
