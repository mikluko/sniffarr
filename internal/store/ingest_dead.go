package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"modernc.org/sqlite"
)

// The SQL function ingest_dead_key(infohash), available on every
// connection, returns deadKey of the infohash blob.
func init() {
	if err := sqlite.RegisterFunction("ingest_dead_key", &sqlite.FunctionImpl{NArgs: 1, Deterministic: true, Scalar: sqlDeadKey}); err != nil {
		panic(fmt.Errorf("register ingest_dead_key: %w", err))
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
		return nil, fmt.Errorf("ingest_dead_key: want a blob, got %T", v)
	}
}

func evictionAttr(why string) metric.AddOption {
	return metric.WithAttributes(attribute.String("sniffarr.queue.eviction", why))
}

// evict moves the queued hashes whose ids selectIDs returns into the dead set
// until until, and returns how many it moved.
func evict(ctx context.Context, tx *sql.Tx, selectIDs string, until int64, args ...any) (int, error) {
	if _, err := tx.ExecContext(ctx, "INSERT INTO ingest_dead (key, until) SELECT ingest_dead_key(infohash), ? FROM ingest_queue WHERE id IN ("+selectIDs+") ON CONFLICT (key) DO UPDATE SET until = excluded.until",
		append([]any{until}, args...)...); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, "DELETE FROM ingest_queue WHERE id IN ("+selectIDs+")", args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
