package store

import (
	"context"
	"database/sql"
	"time"

	"sarak.sh/mikluko/libarr/database"
)

// Kinds name what a task asks for.
const (
	// TaskFetch asks for a sighted hash's metadata.
	TaskFetch = "fetch"
	// TaskPeers asks for a stored release's peers on the DHT.
	TaskPeers = "peers"
	// TaskProbe asks for the tracks of a stored release's main video file.
	TaskProbe = "probe"
)

// Discoveries name how a hash was sighted, in the order its fetch is served.
const (
	Arr      = "arr"
	Announce = "announce"
	Sample   = "sample"
)

// Priorities order the tasks of each kind, P0 first. A fetch takes its
// discovery's; a peer lookup or a probe asked for in the web UI takes
// PriorityRequest, a probe of a release a search returned PriorityArr, and
// any other probe PrioritySample.
const (
	PriorityRequest  = 0
	PriorityArr      = 1
	PriorityAnnounce = 2
	PrioritySample   = 3
)

// Sighting is one report of a hash seen on the DHT.
type Sighting struct {
	Hash      Hash
	Discovery string
}

// Dead-set durations.
const (
	DefaultDeadFor    = 7 * 24 * time.Hour
	DefaultNoPeersFor = 24 * time.Hour
)

// TaskOptions tune the probe tasks of a Tasks.
type TaskOptions struct {
	// NoProbes has Trim drop every probe queued.
	NoProbes bool
}

// Tasks is the queue of probe tasks, one per hash, served by priority and
// then newest first. It is safe for concurrent use.
type Tasks struct {
	db      *database.DB
	opts    TaskOptions
	pace    pace
	probers probers
}

// NewTasks returns the Tasks in db, applying any pending migrations.
func NewTasks(ctx context.Context, db *database.DB, opts TaskOptions) (*Tasks, error) {
	if err := migrate(ctx, db); err != nil {
		return nil, err
	}
	return &Tasks{db: db, opts: opts}, nil
}

// PriorityOf returns the priority of a fetch of a hash sighted by the
// discovery.
func PriorityOf(discovery string) int {
	switch discovery {
	case Arr:
		return PriorityArr
	case Announce:
		return PriorityAnnounce
	}
	return PrioritySample
}

// DiscoveryOf returns the discovery a fetch at the priority serves.
func DiscoveryOf(priority int) string {
	switch {
	case priority <= PriorityArr:
		return Arr
	case priority == PriorityAnnounce:
		return Announce
	}
	return Sample
}

// Trim drops the probes of releases no longer stored or that a probe cannot
// read, or every probe with NoProbes.
func (q *Tasks) Trim(ctx context.Context) error {
	return q.db.Tx(ctx, func(tx *sql.Tx) error { return trimProbes(ctx, tx, q.opts.NoProbes) })
}

// trimProbes drops the probes of releases no longer stored or that a probe
// cannot read, or every probe with all.
func trimProbes(ctx context.Context, tx *sql.Tx, all bool) error {
	if all {
		_, err := tx.ExecContext(ctx, "DELETE FROM task_queue WHERE kind = 'probe'")
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM task_queue WHERE kind = 'probe'
		AND NOT EXISTS (SELECT 1 FROM torrent_releases t WHERE t.infohash = task_queue.infohash AND `+probeReadable+`)`, probeArgs()...)
	return err
}

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

// Reset frees every lease.
func (q *Tasks) Reset(ctx context.Context) error {
	_, err := q.db.W.ExecContext(ctx, "UPDATE task_queue SET lease_owner = NULL, lease_until = NULL WHERE lease_owner IS NOT NULL")
	return err
}
