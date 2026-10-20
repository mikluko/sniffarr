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

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

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

const (
	DefaultMaxSize    = 1000000
	DefaultMaxAge     = 24 * time.Hour
	DefaultDeadFor    = 7 * 24 * time.Hour
	DefaultNoPeersFor = 24 * time.Hour
)

// Options bound a Queue. Zero fields take the default named in their doc.
type Options struct {
	// MaxSize is the most hashes Trim leaves queued; DefaultMaxSize.
	MaxSize int
	// MaxAge is how long a hash may wait in the queue before Trim evicts it;
	// DefaultMaxAge.
	MaxAge time.Duration
	// DeadFor is how long a hash Trim evicts, or one dropped after its last
	// failed attempt, is kept out of the queue; DefaultDeadFor.
	DeadFor time.Duration
	// NoPeersFor is how long a hash dropped by NoPeers is kept out of the
	// queue; DefaultNoPeersFor.
	NoPeersFor time.Duration
	// Meter records the queue's metrics; the global MeterProvider when nil.
	Meter metric.MeterProvider
}

// Queue is the work queue. It is safe for concurrent use.
type Queue struct {
	db        *database.DB
	opts      Options
	evictions metric.Int64Counter
}

//go:embed migrations/*.sql
var migrations embed.FS

// New returns the Queue in db, applying any pending migrations.
func New(ctx context.Context, db *database.DB, opts Options) (*Queue, error) {
	if opts.MaxSize <= 0 {
		opts.MaxSize = DefaultMaxSize
	}
	if opts.MaxAge <= 0 {
		opts.MaxAge = DefaultMaxAge
	}
	if opts.DeadFor <= 0 {
		opts.DeadFor = DefaultDeadFor
	}
	if opts.NoPeersFor <= 0 {
		opts.NoPeersFor = DefaultNoPeersFor
	}
	if opts.Meter == nil {
		opts.Meter = otel.GetMeterProvider()
	}
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(ctx, "queue", sub); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	q := &Queue{db: db, opts: opts}
	if err := q.observe(opts.Meter); err != nil {
		return nil, err
	}
	return q, nil
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

// Enqueue queues every sighted hash not yet queued, and returns how many it
// queued and how many it knew. A dead hash is queued only when sighted by
// announce, which revives it. A queued sample sighted by announce moves ahead
// to the announce queue. The queue may outgrow MaxSize until the next Trim.
func (q *Queue) Enqueue(ctx context.Context, sightings []Sighting, now time.Time) (queued, known int, err error) {
	err = q.db.Tx(ctx, func(tx *sql.Tx) error {
		for _, g := range sightings {
			var dead bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM dead WHERE infohash = ? AND until > ?)", g.Hash[:], now.UnixMilli()).Scan(&dead); err != nil {
				return err
			}
			if dead {
				if g.Discovery != Announce {
					known++
					continue
				}
				if _, err := tx.ExecContext(ctx, "DELETE FROM dead WHERE infohash = ?", g.Hash[:]); err != nil {
					return err
				}
			}
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
			if _, err := tx.ExecContext(ctx, "INSERT INTO queue (infohash, priority, queued_at) VALUES (?, ?, ?)", g.Hash[:], priority(g.Discovery), now.UnixMilli()); err != nil {
				return err
			}
			queued++
		}
		return nil
	})
	return queued, known, err
}

// Trimmed is what one Trim removed.
type Trimmed struct {
	// Aged and Oversize are the hashes evicted for waiting longer than MaxAge
	// and for queueing beyond MaxSize.
	Aged, Oversize int
	// Expired is how many dead hashes were forgotten.
	Expired int
}

// Trim evicts free hashes queued for longer than MaxAge, then the free hashes
// beyond MaxSize, samples before announced ones and the oldest first, and
// keeps every evicted hash dead for DeadFor. It forgets the dead hashes whose
// time is up.
func (q *Queue) Trim(ctx context.Context, now time.Time) (Trimmed, error) {
	var t Trimmed
	until := now.Add(q.opts.DeadFor).UnixMilli()
	err := q.db.Tx(ctx, func(tx *sql.Tx) error {
		n, err := evict(ctx, tx, "SELECT id FROM queue WHERE lease_owner IS NULL AND queued_at < ?", until, now.Add(-q.opts.MaxAge).UnixMilli())
		if err != nil {
			return err
		}
		t.Aged = n
		var size int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM queue").Scan(&size); err != nil {
			return err
		}
		if over := size - q.opts.MaxSize; over > 0 {
			n, err := evict(ctx, tx, "SELECT id FROM queue WHERE lease_owner IS NULL ORDER BY priority DESC, id LIMIT ?", until, over)
			if err != nil {
				return err
			}
			t.Oversize = n
		}
		res, err := tx.ExecContext(ctx, "DELETE FROM dead WHERE until <= ?", now.UnixMilli())
		if err != nil {
			return err
		}
		e, err := res.RowsAffected()
		t.Expired = int(e)
		return err
	})
	if err != nil {
		return Trimmed{}, err
	}
	q.evictions.Add(ctx, int64(t.Aged), evictionAttr("age"))
	q.evictions.Add(ctx, int64(t.Oversize), evictionAttr("size"))
	return t, nil
}

func evictionAttr(why string) metric.AddOption {
	return metric.WithAttributes(attribute.String("sniffarr.queue.eviction", why))
}

// evict moves the queued hashes whose ids selectIDs returns into the dead set
// until until, and returns how many it moved.
func evict(ctx context.Context, tx *sql.Tx, selectIDs string, until int64, args ...any) (int, error) {
	if _, err := tx.ExecContext(ctx, "INSERT INTO dead (infohash, until) SELECT infohash, ? FROM queue WHERE id IN ("+selectIDs+") ON CONFLICT (infohash) DO UPDATE SET until = excluded.until",
		append([]any{until}, args...)...); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, "DELETE FROM queue WHERE id IN ("+selectIDs+")", args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
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
// or dropped from the queue and kept dead for DeadFor once it has failed
// maxAttempts times; Fail reports whether it was dropped. A hash not leased
// to owner is left as is.
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
			_, err := evict(ctx, tx, "SELECT id FROM queue WHERE infohash = ?", now.Add(q.opts.DeadFor).UnixMilli(), h[:])
			return err
		}
		retry := now.Add(backoff << (attempts - 1))
		_, err = tx.ExecContext(ctx, "UPDATE queue SET attempts = ?, not_before = ?, lease_owner = NULL, lease_until = NULL WHERE infohash = ?",
			attempts, retry.UnixMilli(), h[:])
		return err
	})
	return dropped, err
}

// NoPeers drops the hash leased to owner, which nobody was found sharing,
// from the queue at once and keeps it dead for NoPeersFor. A hash not leased
// to owner is left as is.
func (q *Queue) NoPeers(ctx context.Context, h Hash, owner int64, now time.Time) error {
	var n int
	err := q.db.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = evict(ctx, tx, "SELECT id FROM queue WHERE infohash = ? AND lease_owner = ?", now.Add(q.opts.NoPeersFor).UnixMilli(), h[:], owner)
		return err
	})
	if err != nil {
		return err
	}
	q.evictions.Add(ctx, int64(n), evictionAttr("no_peers"))
	return nil
}

func (q *Queue) observe(mp metric.MeterProvider) error {
	m := mp.Meter("github.com/mikluko/sniffarr/internal/queue")
	var err error
	q.evictions, err = m.Int64Counter("sniffarr.queue.evictions", metric.WithUnit("{infohash}"),
		metric.WithDescription("Infohashes evicted from the queue, by why."))
	if err != nil {
		return err
	}
	depth, err := m.Int64ObservableGauge("sniffarr.queue.depth", metric.WithUnit("{infohash}"),
		metric.WithDescription("Infohashes queued, by how they were sighted."))
	if err != nil {
		return err
	}
	leases, err := m.Int64ObservableGauge("sniffarr.queue.leases", metric.WithUnit("{lease}"),
		metric.WithDescription("Tasks leased to fetchers."))
	if err != nil {
		return err
	}
	dead, err := m.Int64ObservableGauge("sniffarr.queue.dead", metric.WithUnit("{infohash}"),
		metric.WithDescription("Infohashes kept out of the queue after eviction or their last failed attempt."))
	if err != nil {
		return err
	}
	_, err = m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		var n int64
		if err := q.db.R.QueryRowContext(ctx, "SELECT count(*) FROM dead").Scan(&n); err != nil {
			return err
		}
		o.ObserveInt64(dead, n)
		return nil
	}, dead)
	if err != nil {
		return err
	}
	_, err = m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		rows, err := q.db.R.QueryContext(ctx, "SELECT priority, count(*), count(lease_owner) FROM queue GROUP BY priority")
		if err != nil {
			return err
		}
		defer rows.Close()
		counts := map[string]int64{Announce: 0, Sample: 0}
		var leased int64
		for rows.Next() {
			var p int
			var n, l int64
			if err := rows.Scan(&p, &n, &l); err != nil {
				return err
			}
			counts[discovery(p)] += n
			leased += l
		}
		for d, n := range counts {
			o.ObserveInt64(depth, n, metric.WithAttributes(attribute.String("sniffarr.discovery", d)))
		}
		o.ObserveInt64(leases, leased)
		return rows.Err()
	}, depth, leases)
	return err
}

// Len returns how many hashes are queued.
func (q *Queue) Len(ctx context.Context) (int, error) {
	var n int
	err := q.db.R.QueryRowContext(ctx, "SELECT count(*) FROM queue").Scan(&n)
	return n, err
}
