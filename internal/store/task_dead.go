package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
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

func evictionAttr(why string) metric.AddOption {
	return metric.WithAttributes(attribute.String("sniffarr.queue.eviction", why))
}

// evicted is a queued hash that evict moved into the dead set, with the
// priority and queued_at of its queue row.
type evicted struct {
	hash     Hash
	priority int
	queuedAt int64
}

// evict moves the queued hashes whose ids selectIDs returns into the dead set
// until until, and returns them.
func evict(ctx context.Context, tx *sql.Tx, selectIDs string, until int64, args ...any) ([]evicted, error) {
	if _, err := tx.ExecContext(ctx, "INSERT INTO task_dead (key, until) SELECT task_dead_key(infohash), ? FROM task_queue WHERE id IN ("+selectIDs+") ON CONFLICT (key) DO UPDATE SET until = excluded.until",
		append([]any{until}, args...)...); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "DELETE FROM task_queue WHERE id IN ("+selectIDs+") RETURNING infohash, priority, queued_at", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []evicted
	for rows.Next() {
		var e evicted
		var b []byte
		if err := rows.Scan(&b, &e.priority, &e.queuedAt); err != nil {
			return nil, err
		}
		e.hash = Hash(b)
		out = append(out, e)
	}
	return out, rows.Err()
}

// journaled reports whether the queue journals a hash of the priority:
// those sighted by a discovery served before samples.
func journaled(priority int) bool {
	return priority < PrioritySample
}

// journalEvicted writes the entry, with the detail of each, for every
// evicted hash the queue journals.
func journalEvicted(ctx context.Context, tx *sql.Tx, rows []evicted, at time.Time, kind, outcome string, detail func(evicted) map[string]any) error {
	for _, e := range rows {
		if !journaled(e.priority) {
			continue
		}
		if err := journal(ctx, tx, e.hash, at, kind, outcome, detail(e)); err != nil {
			return err
		}
	}
	return nil
}
