package store

import (
	"context"
	"database/sql"
	"errors"
)

// TokensUsed returns the classifier tokens recorded for the day.
func (s *Store) TokensUsed(ctx context.Context, day string) (int64, error) {
	var n int64
	err := s.r.QueryRowContext(ctx, "SELECT tokens FROM ingest_token_usage WHERE day = ?", day).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

// AddTokens adds n to the classifier tokens recorded for the day.
func (s *Store) AddTokens(ctx context.Context, day string, n int64) error {
	_, err := s.w.ExecContext(ctx, "INSERT INTO ingest_token_usage (day, tokens) VALUES (?, ?) ON CONFLICT (day) DO UPDATE SET tokens = tokens + excluded.tokens", day, n)
	return err
}
