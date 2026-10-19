// Package queue is the work queue of infohashes awaiting their metadata:
// sightings enqueue them, fetchers lease them, and a result completes or
// fails them. Its tables live in the shared database.
package queue

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"time"

	"github.com/mikluko/sniffarr/internal/database"
)

// ErrNotQueued is returned for a hash that is not in the queue.
var ErrNotQueued = errors.New("not queued")

// Hash is a BitTorrent v1 infohash.
type Hash [20]byte

// Discoveries name how a hash was sighted, in the order the queue serves
// them.
const (
	Announce = "announce"
	Sample   = "sample"
)

// Sighting is one report of a hash seen on the DHT.
type Sighting struct {
	Hash      Hash
	Discovery string
}

// Queue is the work queue. It is safe for concurrent use.
type Queue struct {
	db *database.DB
}

//go:embed migrations/*.sql
var migrations embed.FS

// New returns the Queue in db, applying any pending migrations.
func New(ctx context.Context, db *database.DB) (*Queue, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(ctx, "queue", sub); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Queue{db: db}, nil
}

func priority(discovery string) int {
	if discovery == Announce {
		return 0
	}
	return 1
}

func discovery(priority int) string {
	if priority == 0 {
		return Announce
	}
	return Sample
}

// Enqueue queues every sighted hash not queued yet, and returns how many it
// queued and how many were queued already. A queued sample sighted by
// announce moves ahead to the announce queue.
func (q *Queue) Enqueue(ctx context.Context, sightings []Sighting) (queued, known int, err error) {
	err = q.db.Tx(ctx, func(tx *sql.Tx) error {
		for _, g := range sightings {
			res, err := tx.ExecContext(ctx, "UPDATE queue SET priority = min(priority, ?) WHERE infohash = ?", priority(g.Discovery), g.Hash[:])
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n > 0 {
				known++
				continue
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO queue (infohash, priority) VALUES (?, ?)", g.Hash[:], priority(g.Discovery)); err != nil {
				return err
			}
			queued++
		}
		return nil
	})
	return queued, known, err
}

// Lease frees the leases expired by now, then leases up to n free hashes to
// owner until now+d. It returns the leased hashes, announced ones first,
// then oldest first, and how many leases expired. A hash is free when it has
// no lease and its retry backoff has passed.
func (q *Queue) Lease(ctx context.Context, owner int64, n int, now time.Time, d time.Duration) (hashes []Hash, expired int, err error) {
	type leased struct {
		hash         Hash
		priority, id int64
	}
	var out []leased
	err = q.db.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE queue SET lease_owner = NULL, lease_until = NULL WHERE lease_until <= ?", now.UnixMilli())
		if err != nil {
			return err
		}
		e, err := res.RowsAffected()
		if err != nil {
			return err
		}
		expired = int(e)
		rows, err := tx.QueryContext(ctx, `UPDATE queue SET lease_owner = ?, lease_until = ? WHERE id IN (
				SELECT id FROM queue WHERE lease_owner IS NULL AND not_before <= ? ORDER BY priority, id LIMIT ?)
			RETURNING infohash, priority, id`,
			owner, now.Add(d).UnixMilli(), now.UnixMilli(), n)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l leased
			var b []byte
			if err := rows.Scan(&b, &l.priority, &l.id); err != nil {
				return err
			}
			l.hash = Hash(b)
			out = append(out, l)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, err
	}
	slices.SortFunc(out, func(a, b leased) int {
		if a.priority != b.priority {
			return int(a.priority - b.priority)
		}
		return int(a.id - b.id)
	})
	hashes = make([]Hash, len(out))
	for i, l := range out {
		hashes[i] = l.hash
	}
	return hashes, expired, nil
}

// Release frees every lease owner holds.
func (q *Queue) Release(ctx context.Context, owner int64) error {
	_, err := q.db.W.ExecContext(ctx, "UPDATE queue SET lease_owner = NULL, lease_until = NULL WHERE lease_owner = ?", owner)
	return err
}

// Reset frees every lease.
func (q *Queue) Reset(ctx context.Context) error {
	_, err := q.db.W.ExecContext(ctx, "UPDATE queue SET lease_owner = NULL, lease_until = NULL WHERE lease_owner IS NOT NULL")
	return err
}

// Discovery returns how the queued hash was sighted, or ErrNotQueued.
func (q *Queue) Discovery(ctx context.Context, h Hash) (string, error) {
	var p int
	err := q.db.R.QueryRowContext(ctx, "SELECT priority FROM queue WHERE infohash = ?", h[:]).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotQueued
	}
	return discovery(p), err
}

// Complete drops the hash from the queue.
func (q *Queue) Complete(ctx context.Context, h Hash) error {
	_, err := q.db.W.ExecContext(ctx, "DELETE FROM queue WHERE infohash = ?", h[:])
	return err
}

// Fail records a failed attempt at the hash leased to owner. The hash is
// freed for another attempt after backoff, doubled for each attempt before,
// or dropped from the queue once it has failed maxAttempts times; Fail
// reports whether it was dropped. A hash not leased to owner is left as is.
func (q *Queue) Fail(ctx context.Context, h Hash, owner int64, now time.Time, maxAttempts int, backoff time.Duration) (bool, error) {
	var dropped bool
	err := q.db.Tx(ctx, func(tx *sql.Tx) error {
		var attempts int
		err := tx.QueryRowContext(ctx, "SELECT attempts FROM queue WHERE infohash = ? AND lease_owner = ?", h[:], owner).Scan(&attempts)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		attempts++
		if attempts >= maxAttempts {
			dropped = true
			_, err := tx.ExecContext(ctx, "DELETE FROM queue WHERE infohash = ?", h[:])
			return err
		}
		retry := now.Add(backoff << (attempts - 1))
		_, err = tx.ExecContext(ctx, "UPDATE queue SET attempts = ?, not_before = ?, lease_owner = NULL, lease_until = NULL WHERE infohash = ?",
			attempts, retry.UnixMilli(), h[:])
		return err
	})
	return dropped, err
}

// Len returns how many hashes are queued.
func (q *Queue) Len(ctx context.Context) (int, error) {
	var n int
	err := q.db.R.QueryRowContext(ctx, "SELECT count(*) FROM queue").Scan(&n)
	return n, err
}
