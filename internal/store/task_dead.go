package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"slices"
	"time"

	"modernc.org/sqlite"
)

// The SQL function task_dead_key(infohash), available on every connection,
// returns deadKey of the infohash blob; ingest_dead_key is the same function
// under the name migration 012 calls.
func init() {
	for _, name := range []string{"task_dead_key", "ingest_dead_key"} {
		if err := sqlite.RegisterFunction(name, &sqlite.FunctionImpl{NArgs: 1, Deterministic: true, Scalar: sqlDeadKey}); err != nil {
			panic(fmt.Errorf("register %s: %w", name, err))
		}
	}
}

// deadKey is the key of the hash in the dead set: its first 8 bytes, big
// endian, as a signed integer, zero-padded when shorter. Two hashes sharing
// their first 8 bytes share the key, so the dead one keeps the other out of
// the queue too, until it expires or an announce revives it.
func deadKey(h []byte) int64 {
	var b [8]byte
	copy(b[:], h)
	return int64(binary.BigEndian.Uint64(b[:]))
}

func sqlDeadKey(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	switch v := args[0].(type) {
	case []byte:
		return deadKey(v), nil
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("task_dead_key: want a blob, got %T", v)
	}
}

// journaled reports whether the fetches of a hash queued at the priority are
// journaled: those sighted by a discovery served before samples.
func journaled(priority int) bool {
	return priority < PrioritySample
}

// Queued is a fetch queued for a sighting.
type Queued struct {
	Hash      Hash
	Discovery string
	// Revived is a hash in the dead set that the sighting queued anyway.
	Revived bool
}

// RecordQueued takes the revived hashes of qs out of the dead set and
// journals each fetch queued for a discovery served before samples, at the
// time, as queued or revived.
func (s *Store) RecordQueued(ctx context.Context, qs []Queued, at time.Time) error {
	if !slices.ContainsFunc(qs, func(q Queued) bool { return q.Revived || journaled(PriorityOf(q.Discovery)) }) {
		return nil
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, q := range qs {
			outcome := "queued"
			if q.Revived {
				outcome = "revived"
				if _, err := tx.ExecContext(ctx, "DELETE FROM task_dead WHERE key = ?", deadKey(q.Hash[:])); err != nil {
					return err
				}
			}
			if !journaled(PriorityOf(q.Discovery)) {
				continue
			}
			if err := journal(ctx, tx, q.Hash, at, JournalSighted, outcome, Detail("discovery", q.Discovery)); err != nil {
				return err
			}
		}
		return nil
	})
}

// Lease is a fetch handed to a fetcher.
type Lease struct {
	Hash     Hash
	Priority int
	// Attempt counts this attempt, the first being 1.
	Attempt int
	Until   time.Time
}

// RecordLeases journals, at the time, each lease of a fetch queued for a
// discovery served before samples.
func (s *Store) RecordLeases(ctx context.Context, ls []Lease, at time.Time) error {
	if !slices.ContainsFunc(ls, func(l Lease) bool { return journaled(l.Priority) }) {
		return nil
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, l := range ls {
			if !journaled(l.Priority) {
				continue
			}
			if err := journal(ctx, tx, l.Hash, at, JournalLeased, "fetch", Detail("attempt", l.Attempt, "lease_until", l.Until)); err != nil {
				return err
			}
		}
		return nil
	})
}

// FetchFailure is a fetch that brought no metadata.
type FetchFailure struct {
	Hash     Hash
	Priority int
	// Outcome is "no_peers", "timeout" or "dropped".
	Outcome string
	// Attempt counts the failed attempt, the first being 1.
	Attempt int
	// RetryAt is when the fetch is tried again, and DeadUntil until when the
	// hash is kept out of the queue; either may be zero.
	RetryAt, DeadUntil time.Time
	// Leases is how many times the attempt was handed out.
	Leases int
}

// RecordFetchFailure keeps f's hash in the dead set until f.DeadUntil,
// unless it is zero, and journals f, at the time, when its fetch was queued
// for a discovery served before samples.
func (s *Store) RecordFetchFailure(ctx context.Context, f FetchFailure, at time.Time) error {
	if f.DeadUntil.IsZero() && !journaled(f.Priority) {
		return nil
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if !f.DeadUntil.IsZero() {
			if _, err := tx.ExecContext(ctx, "INSERT INTO task_dead (key, until) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET until = excluded.until",
				deadKey(f.Hash[:]), f.DeadUntil.UnixMilli()); err != nil {
				return err
			}
		}
		if !journaled(f.Priority) {
			return nil
		}
		return journal(ctx, tx, f.Hash, at, JournalFetchFailed, f.Outcome,
			Detail("attempt", f.Attempt, "retry_at", f.RetryAt, "dead_until", f.DeadUntil, "leases", manyLeases(f.Leases)))
	})
}

// ExpireDead forgets the dead hashes whose time is up at now, and returns
// how many it forgot.
func (s *Store) ExpireDead(ctx context.Context, now time.Time) (int, error) {
	res, err := s.w.ExecContext(ctx, "DELETE FROM task_dead WHERE until <= ?", now.UnixMilli())
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// Dead returns how many hashes the dead set holds.
func (s *Store) Dead(ctx context.Context) (int64, error) {
	var n int64
	err := s.r.QueryRowContext(ctx, "SELECT count(*) FROM task_dead").Scan(&n)
	return n, err
}
