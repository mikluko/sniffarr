package store

import (
	"context"
	"database/sql"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

func evictionAttr(why string) metric.AddOption {
	return metric.WithAttributes(attribute.String("sniffarr.queue.eviction", why))
}

// evict moves the queued hashes whose ids selectIDs returns into the dead set
// until until, and returns how many it moved.
func evict(ctx context.Context, tx *sql.Tx, selectIDs string, until int64, args ...any) (int, error) {
	if _, err := tx.ExecContext(ctx, "INSERT INTO ingest_dead (infohash, until) SELECT infohash, ? FROM ingest_queue WHERE id IN ("+selectIDs+") ON CONFLICT (infohash) DO UPDATE SET until = excluded.until",
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
