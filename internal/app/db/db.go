// Package db provides the SQLite database the API owns, with the store and
// the work queue over it.
package db

import (
	"context"
	"fmt"
	"log/slog"

	"go.uber.org/fx"

	"sarak.sh/mikluko/sniffarr/internal/database"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

// Config is what Module needs.
type Config struct {
	// Path is the SQLite database file, created when missing.
	Path  string
	Store store.Options
	Tasks store.TaskOptions
}

// Module provides *database.DB, *store.Store and *store.Tasks from Config;
// the store, then the database, close at stop. Migrations and the freeing of the queue's
// leases run as they are provided, outside the start and its deadline.
var Module = fx.Module("db", fx.Provide(open, newStore, newQueue))

func open(lc fx.Lifecycle, cfg Config, log *slog.Logger) (*database.DB, error) {
	db, err := database.Open(context.Background(), cfg.Path, database.Options{Meter: cfg.Store.Meter, Log: log})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", cfg.Path, err)
	}
	lc.Append(fx.StopHook(db.Close))
	return db, nil
}

func newStore(lc fx.Lifecycle, db *database.DB, cfg Config, log *slog.Logger) (*store.Store, error) {
	opts := cfg.Store
	if opts.Log == nil {
		opts.Log = log
	}
	s, err := store.New(context.Background(), db, opts)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	lc.Append(fx.StopHook(s.Close))
	return s, nil
}

func newQueue(db *database.DB, cfg Config) (*store.Tasks, error) {
	ctx := context.Background()
	q, err := store.NewTasks(ctx, db, cfg.Tasks)
	if err != nil {
		return nil, fmt.Errorf("queue: %w", err)
	}
	if err := q.Reset(ctx); err != nil {
		return nil, fmt.Errorf("reset leases: %w", err)
	}
	return q, nil
}
