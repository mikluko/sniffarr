package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// countsKey is the settings key of the counts SaveCounts keeps.
const countsKey = "counts"

// savedCounts is the stored form of SaveCounts' counts.
type savedCounts struct {
	At    time.Time
	Stats Stats
}

// SaveCounts keeps st, computed at at, for LoadCounts, replacing what it
// kept before.
func (s *Store) SaveCounts(ctx context.Context, at time.Time, st Stats) error {
	raw, err := json.Marshal(savedCounts{At: at.UTC(), Stats: st})
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value", countsKey, raw)
		return err
	})
}

// LoadCounts returns the counts SaveCounts kept last and when they were
// computed; false when it kept none.
func (s *Store) LoadCounts(ctx context.Context) (Stats, time.Time, bool, error) {
	var raw []byte
	err := s.r.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", countsKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Stats{}, time.Time{}, false, nil
	}
	if err != nil {
		return Stats{}, time.Time{}, false, err
	}
	var c savedCounts
	if err := json.Unmarshal(raw, &c); err != nil {
		return Stats{}, time.Time{}, false, fmt.Errorf("stored counts: %w", err)
	}
	return c.Stats, c.At, true, nil
}
