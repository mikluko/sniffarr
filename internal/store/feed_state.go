package store

import (
	"context"
	"time"
)

// FeedDone reports whether the source marked the key done.
func (s *Store) FeedDone(ctx context.Context, source, key string) (bool, error) {
	var done bool
	err := s.r.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM feed_state WHERE source = ? AND key = ?)", source, key).Scan(&done)
	return done, err
}

// MarkFeedDone records the key as done by the source; a key marked before
// keeps its time.
func (s *Store) MarkFeedDone(ctx context.Context, source, key string, now time.Time) error {
	_, err := s.w.ExecContext(ctx, "INSERT OR IGNORE INTO feed_state (source, key, done_at) VALUES (?, ?, ?)", source, key, now.Unix())
	return err
}
