package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/mikluko/sniffarr/internal/database"
)

// ErrNotQueued is returned for a hash that is not in the queue.
var ErrNotQueued = errors.New("not queued")

// Kinds name what a task asks for.
const (
	// TaskFetch asks for a sighted hash's metadata.
	TaskFetch = "fetch"
	// TaskPeers asks for a stored release's peers on the DHT.
	TaskPeers = "peers"
	// TaskProbe asks for the tracks of a stored release's main video file.
	TaskProbe = "probe"
)

// Discoveries name how a hash was sighted, in the order the queue serves
// them.
const (
	Arr      = "arr"
	Feed     = "feed"
	Announce = "announce"
	Sample   = "sample"
)

// Priorities order the queue, P0 first, whatever the kind. A fetch takes
// its discovery's; a peer lookup or a probe asked for in the web UI takes
// PriorityRequest, a probe of a release a search returned PriorityArr, and
// any other probe PrioritySample.
const (
	PriorityRequest  = 0
	PriorityArr      = 1
	PriorityFeed     = 2
	PriorityAnnounce = 3
	PrioritySample   = 4
)

// Task is one leased task.
type Task struct {
	Kind string
	Hash Hash
}

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

// TaskOptions bound the fetch tasks of a Tasks. Zero fields take the
// default named in their doc.
type TaskOptions struct {
	// MaxSize is the most fetch tasks Trim leaves queued; DefaultMaxSize.
	MaxSize int
	// MaxAge is how long a fetch task may wait before Trim evicts it;
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

// Tasks is the queue of tasks of every kind, one per kind and hash, served
// by priority and then oldest first; probes are served newest first. It is
// safe for concurrent use.
type Tasks struct {
	db        *database.DB
	opts      TaskOptions
	evictions metric.Int64Counter
	pace      pace
}

// NewTasks returns the Tasks in db, applying any pending migrations.
func NewTasks(ctx context.Context, db *database.DB, opts TaskOptions) (*Tasks, error) {
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
	if err := migrate(ctx, db); err != nil {
		return nil, err
	}
	q := &Tasks{db: db, opts: opts}
	if err := q.observe(opts.Meter); err != nil {
		return nil, err
	}
	return q, nil
}

func priority(discovery string) int {
	switch discovery {
	case Arr:
		return PriorityArr
	case Feed:
		return PriorityFeed
	case Announce:
		return PriorityAnnounce
	}
	return PrioritySample
}

func discovery(priority int) string {
	switch {
	case priority <= PriorityArr:
		return Arr
	case priority == PriorityFeed:
		return Feed
	case priority == PriorityAnnounce:
		return Announce
	}
	return Sample
}

// Enqueue queues a fetch of every sighted hash not yet queued, and returns
// how many it queued and how many it knew. A dead hash is queued only when
// sighted by announce, arr or feed, which revives it. A queued hash sighted
// by a discovery served before its own moves ahead to that discovery's
// priority. The queue may outgrow MaxSize until the next Trim.
func (q *Tasks) Enqueue(ctx context.Context, sightings []Sighting, now time.Time) (queued, known int, err error) {
	err = q.db.Tx(ctx, func(tx *sql.Tx) error {
		for _, g := range sightings {
			added, err := enqueueFetch(ctx, tx, g, now)
			if err != nil {
				return err
			}
			if added {
				queued++
			} else {
				known++
			}
		}
		return nil
	})
	return queued, known, err
}

// enqueueFetch queues the fetch of the sighting as Enqueue does, and reports
// whether it queued it.
func enqueueFetch(ctx context.Context, tx *sql.Tx, g Sighting, now time.Time) (bool, error) {
	var dead bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM task_dead WHERE key = ? AND until > ?)", deadKey(g.Hash[:]), now.UnixMilli()).Scan(&dead); err != nil {
		return false, err
	}
	if dead {
		if g.Discovery != Announce && g.Discovery != Arr && g.Discovery != Feed {
			return false, nil
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM task_dead WHERE key = ?", deadKey(g.Hash[:])); err != nil {
			return false, err
		}
	}
	res, err := tx.ExecContext(ctx, "INSERT INTO task_queue (kind, infohash, priority, queued_at) VALUES ('fetch', ?1, ?2, ?3) ON CONFLICT (kind, infohash) DO NOTHING",
		g.Hash[:], priority(g.Discovery), now.UnixMilli())
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return n > 0, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE task_queue SET priority = min(priority, ?) WHERE kind = 'fetch' AND infohash = ?", priority(g.Discovery), g.Hash[:])
	return false, err
}

// Trimmed is what one Trim removed.
type Trimmed struct {
	// Aged and Oversize are the hashes evicted for waiting longer than MaxAge
	// and for queueing beyond MaxSize.
	Aged, Oversize int
	// Expired is how many dead hashes were forgotten.
	Expired int
}

// Trim evicts free fetch tasks queued for longer than MaxAge, then the free
// fetch tasks beyond MaxSize, samples before announced ones and the oldest
// first, and keeps every evicted hash dead for DeadFor. It drops the peer
// lookups of releases no longer stored and the probes of releases no longer
// stored or that a probe cannot read, and forgets the dead hashes whose time
// is up.
func (q *Tasks) Trim(ctx context.Context, now time.Time) (Trimmed, error) {
	var t Trimmed
	until := now.Add(q.opts.DeadFor).UnixMilli()
	err := q.db.Tx(ctx, func(tx *sql.Tx) error {
		n, err := evict(ctx, tx, agedIDs, until, now.Add(-q.opts.MaxAge).UnixMilli())
		if err != nil {
			return err
		}
		t.Aged = n
		var size int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM task_queue WHERE kind = 'fetch'").Scan(&size); err != nil {
			return err
		}
		if over := size - q.opts.MaxSize; over > 0 {
			n, err := evict(ctx, tx, oversizeIDs, until, over)
			if err != nil {
				return err
			}
			t.Oversize = n
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM task_queue WHERE kind = 'peers'
			AND NOT EXISTS (SELECT 1 FROM torrent_releases t WHERE t.infohash = task_queue.infohash)`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM task_queue WHERE kind = 'probe'
			AND NOT EXISTS (SELECT 1 FROM torrent_releases t WHERE t.infohash = task_queue.infohash AND `+probeReadable+`)`, probeArgs()...); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "DELETE FROM task_dead WHERE until <= ?", now.UnixMilli())
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

// Lease frees the expired leases of fetches and peer lookups, then leases up
// to n of them that are free to owner until now+d. It returns the leased
// tasks in the queue's order, and how many leases expired. A task is free
// when it has no lease and its retry backoff has passed.
func (q *Tasks) Lease(ctx context.Context, owner int64, n int, now time.Time, d time.Duration) (tasks []Task, expired int, err error) {
	type leased struct {
		task         Task
		priority, id int64
	}
	var out []leased
	err = q.db.Tx(ctx, func(tx *sql.Tx) error {
		if expired, err = freeExpired(ctx, tx, "kind IN ('fetch', 'peers')", now); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "UPDATE task_queue SET lease_owner = ?, lease_until = ?, leased_at = ? WHERE id IN ("+leaseIDs+") RETURNING kind, infohash, priority, id",
			owner, now.Add(d).UnixMilli(), now.UnixMilli(), now.UnixMilli(), n)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l leased
			var b []byte
			if err := rows.Scan(&l.task.Kind, &b, &l.priority, &l.id); err != nil {
				return err
			}
			l.task.Hash = Hash(b)
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
	tasks = make([]Task, len(out))
	for i, l := range out {
		tasks[i] = l.task
	}
	return tasks, expired, nil
}

// The queries below name their index: SQLite, without statistics, takes the
// (kind, infohash) key for a condition on kind and walks every fetch queued.
const (
	// leaseIDs selects, given now and n, the first n free fetches and peer
	// lookups in the queue's order.
	leaseIDs = "SELECT id FROM task_queue INDEXED BY task_queue_order WHERE kind IN ('fetch', 'peers') AND lease_owner IS NULL AND not_before <= ? ORDER BY priority, id LIMIT ?"
	// agedIDs selects the free fetches queued before the given time.
	agedIDs = "SELECT id FROM task_queue INDEXED BY task_queue_queued_at WHERE kind = 'fetch' AND lease_owner IS NULL AND queued_at < ?"
	// oversizeIDs selects, given n, the n free fetches Trim evicts first.
	oversizeIDs = "SELECT id FROM task_queue INDEXED BY task_queue_evict WHERE kind = 'fetch' AND lease_owner IS NULL ORDER BY priority DESC, id LIMIT ?"
)

// freeExpiredQuery frees, given now, the expired leases of the tasks kinds
// selects, an SQL condition.
func freeExpiredQuery(kinds string) string {
	return "UPDATE task_queue INDEXED BY task_queue_lease_until SET lease_owner = NULL, lease_until = NULL WHERE " + kinds + " AND lease_until <= ?"
}

// freeExpired frees the leases expired by now of the tasks kinds selects,
// an SQL condition, and returns how many it freed.
func freeExpired(ctx context.Context, tx *sql.Tx, kinds string, now time.Time) (int, error) {
	res, err := tx.ExecContext(ctx, freeExpiredQuery(kinds), now.UnixMilli())
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// Release frees every lease owner holds.
func (q *Tasks) Release(ctx context.Context, owner int64) error {
	_, err := q.db.W.ExecContext(ctx, "UPDATE task_queue SET lease_owner = NULL, lease_until = NULL WHERE lease_owner = ?", owner)
	return err
}

// Reset frees every lease.
func (q *Tasks) Reset(ctx context.Context) error {
	_, err := q.db.W.ExecContext(ctx, "UPDATE task_queue SET lease_owner = NULL, lease_until = NULL WHERE lease_owner IS NOT NULL")
	return err
}

// Discovery returns how the hash queued for a fetch was sighted, or
// ErrNotQueued.
func (q *Tasks) Discovery(ctx context.Context, h Hash) (string, error) {
	var p int
	err := q.db.R.QueryRowContext(ctx, "SELECT priority FROM task_queue WHERE kind = 'fetch' AND infohash = ?", h[:]).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotQueued
	}
	return discovery(p), err
}

// Complete drops the task of the kind for the hash from the queue.
func (q *Tasks) Complete(ctx context.Context, kind string, h Hash) error {
	_, err := q.db.W.ExecContext(ctx, "DELETE FROM task_queue WHERE kind = ? AND infohash = ?", kind, h[:])
	return err
}

// Fail records a failed attempt at the fetch of the hash leased to owner.
// The hash is freed for another attempt after backoff, doubled for each
// attempt before, or dropped from the queue and kept dead for DeadFor once
// it has failed maxAttempts times; Fail reports whether it was dropped. A
// hash not leased to owner is left as is.
func (q *Tasks) Fail(ctx context.Context, h Hash, owner int64, now time.Time, maxAttempts int, backoff time.Duration) (bool, error) {
	var dropped bool
	err := q.db.Tx(ctx, func(tx *sql.Tx) error {
		var attempts int
		err := tx.QueryRowContext(ctx, "SELECT attempts FROM task_queue WHERE kind = 'fetch' AND infohash = ? AND lease_owner = ?", h[:], owner).Scan(&attempts)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		attempts++
		if attempts >= maxAttempts {
			dropped = true
			_, err := evict(ctx, tx, "SELECT id FROM task_queue WHERE kind = 'fetch' AND infohash = ?", now.Add(q.opts.DeadFor).UnixMilli(), h[:])
			return err
		}
		retry := now.Add(backoff << (attempts - 1))
		_, err = tx.ExecContext(ctx, "UPDATE task_queue SET attempts = ?, not_before = ?, lease_owner = NULL, lease_until = NULL WHERE kind = 'fetch' AND infohash = ?",
			attempts, retry.UnixMilli(), h[:])
		return err
	})
	return dropped, err
}

// NoPeers drops the fetch of the hash leased to owner, which nobody was
// found sharing, from the queue at once and keeps it dead for NoPeersFor. A
// hash not leased to owner is left as is.
func (q *Tasks) NoPeers(ctx context.Context, h Hash, owner int64, now time.Time) error {
	var n int
	err := q.db.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = evict(ctx, tx, "SELECT id FROM task_queue WHERE kind = 'fetch' AND infohash = ? AND lease_owner = ?", now.Add(q.opts.NoPeersFor).UnixMilli(), h[:], owner)
		return err
	})
	if err != nil {
		return err
	}
	q.evictions.Add(ctx, int64(n), evictionAttr("no_peers"))
	return nil
}

func (q *Tasks) observe(mp metric.MeterProvider) error {
	m := mp.Meter("github.com/mikluko/sniffarr/internal/store")
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
		metric.WithDescription("Tasks leased to fetchers, by kind."))
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
		if err := q.db.R.QueryRowContext(ctx, "SELECT count(*) FROM task_dead").Scan(&n); err != nil {
			return err
		}
		o.ObserveInt64(dead, n)
		return nil
	}, dead)
	if err != nil {
		return err
	}
	_, err = m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		depths, leased, err := q.counts(ctx)
		if err != nil {
			return err
		}
		for d, n := range depths {
			o.ObserveInt64(depth, n, metric.WithAttributes(attribute.String("sniffarr.discovery", d)))
		}
		for k, n := range leased {
			o.ObserveInt64(leases, n, metric.WithAttributes(attribute.String("sniffarr.task.kind", k)))
		}
		return nil
	}, depth, leases)
	return err
}

// counts returns the fetches queued by discovery and the leases by kind.
func (q *Tasks) counts(ctx context.Context) (depths, leased map[string]int64, err error) {
	rows, err := q.db.R.QueryContext(ctx, "SELECT kind, priority, count(*), count(lease_owner) FROM task_queue GROUP BY kind, priority")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	depths = map[string]int64{Arr: 0, Announce: 0, Sample: 0}
	leased = map[string]int64{TaskFetch: 0, TaskPeers: 0, TaskProbe: 0}
	for rows.Next() {
		var kind string
		var p int
		var n, l int64
		if err := rows.Scan(&kind, &p, &n, &l); err != nil {
			return nil, nil, err
		}
		if kind == TaskFetch {
			depths[discovery(p)] += n
		}
		leased[kind] += l
	}
	return depths, leased, rows.Err()
}

// Len returns how many fetches are queued.
func (q *Tasks) Len(ctx context.Context) (int, error) {
	var n int
	err := q.db.R.QueryRowContext(ctx, "SELECT count(*) FROM task_queue WHERE kind = 'fetch'").Scan(&n)
	return n, err
}
