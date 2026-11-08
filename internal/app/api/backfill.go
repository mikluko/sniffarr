package api

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"sarak.sh/mikluko/sniffarr/internal/store"
)

// backfillLog is how often the progress of a running backfill is logged.
const backfillLog = 10 * time.Second

// backfill runs the store's one-time backfills, logging their progress; an
// error is logged and leaves the rest to the next start.
func backfill(ctx context.Context, s *store.Store, log *slog.Logger) {
	start := time.Now()
	logged := map[string]time.Time{}
	err := s.Backfill(ctx, func(b store.Backfilled) {
		switch {
		case b.Done:
			log.Info("backfilled", "backfill", b.Name, "rows", b.Rows, "took", time.Since(start))
		case time.Since(logged[b.Name]) >= backfillLog:
			logged[b.Name] = time.Now()
			log.Info("backfilling", "backfill", b.Name, "rows", b.Rows)
		}
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		log.ErrorContext(ctx, "backfill", "err", err)
	}
}
