// Package db provides the SQLite database the API owns, with the store over
// it.
package db

import (
	"context"
	"fmt"
	"log/slog"

	"go.uber.org/fx"

	"sarak.sh/mikluko/libarr/database"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

// Config is what Module needs.
type Config struct {
	// Path is the SQLite database file, created when missing.
	Path  string
	Store store.Options
}

// Module provides *database.DB and *store.Store from Config; the database
// closes at stop. Migrations run as the store is provided, outside the start
// and its deadline.
var Module = fx.Module("db", fx.Provide(open, newStore))

func open(lc fx.Lifecycle, cfg Config, log *slog.Logger) (*database.DB, error) {
	db, err := database.Open(context.Background(), cfg.Path, database.Options{Service: "sniffarr", Meter: cfg.Store.Meter, Log: log})
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
