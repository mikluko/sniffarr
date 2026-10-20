// Package db provides the SQLite database the API owns, with the store and
// the work queue over it.
package db

import (
	"context"
	"fmt"

	"go.uber.org/fx"

	"github.com/mikluko/sniffarr/internal/database"
	"github.com/mikluko/sniffarr/internal/queue"
	"github.com/mikluko/sniffarr/internal/store"
)

// Config is what Module needs.
type Config struct {
	// Path is the SQLite database file, created when missing.
	Path  string
	Store store.Options
	Queue queue.Options
}

// Module provides *database.DB, *store.Store and *queue.Queue from Config;
// the database closes at stop, and the queue's leases are freed at start.
var Module = fx.Module("db", fx.Provide(open, newStore, newQueue))

func open(lc fx.Lifecycle, cfg Config) (*database.DB, error) {
	db, err := database.Open(context.Background(), cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", cfg.Path, err)
	}
	lc.Append(fx.StopHook(db.Close))
	return db, nil
}

func newStore(db *database.DB, cfg Config) (*store.Store, error) {
	s, err := store.New(context.Background(), db, cfg.Store)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	return s, nil
}

func newQueue(lc fx.Lifecycle, db *database.DB, cfg Config) (*queue.Queue, error) {
	q, err := queue.New(context.Background(), db, cfg.Queue)
	if err != nil {
		return nil, fmt.Errorf("queue: %w", err)
	}
	lc.Append(fx.StartHook(func(ctx context.Context) error {
		if err := q.Reset(ctx); err != nil {
			return fmt.Errorf("reset leases: %w", err)
		}
		return nil
	}))
	return q, nil
}
