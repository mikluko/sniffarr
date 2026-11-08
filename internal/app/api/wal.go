package api

import (
	"context"
	"errors"
	"log/slog"

	"sarak.sh/mikluko/libarr/database"
)

// walLimit is the WAL size, in bytes, at which truncateWAL truncates it.
const walLimit = 1 << 30

// truncateWAL truncates the WAL once it is limit or over, and logs the
// reader that stops it when one does.
func truncateWAL(ctx context.Context, db *database.DB, limit int64, log *slog.Logger) {
	size, err := db.WALSize()
	if err != nil {
		log.ErrorContext(ctx, "wal size", "err", err)
		return
	}
	if size < limit {
		return
	}
	c, err := db.Checkpoint(ctx, database.Truncate)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			log.ErrorContext(ctx, "truncate wal", "size", size, "err", err)
		}
		return
	}
	if c.Busy {
		log.WarnContext(ctx, "wal held by a reader", "size", size, "frames", c.Frames, "backfilled", c.Backfilled)
		return
	}
	log.InfoContext(ctx, "truncated wal", "size", size)
}
