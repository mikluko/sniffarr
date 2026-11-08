package store

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"time"
)

// Screened is what the store holds of a sighted hash.
type Screened struct {
	Stored, Rejected bool
	// Dead is a dead-set entry under the hash's key that has not expired.
	Dead bool
}

// screenChunk is the most hashes one Screen statement takes.
const screenChunk = 500

// Screen returns what the store holds of each of hashes at now, reading
// only, in one statement per screenChunk hashes; a hash it holds nothing of
// is absent.
func (s *Store) Screen(ctx context.Context, hashes []Hash, now time.Time) (map[Hash]Screened, error) {
	out := map[Hash]Screened{}
	for c := range slices.Chunk(hashes, screenChunk) {
		args := make([]any, 0, len(c)+1)
		for _, h := range c {
			args = append(args, h[:])
		}
		args = append(args, now.UnixMilli())
		values := strings.TrimSuffix(strings.Repeat("(?),", len(c)), ",")
		err := s.each(ctx, func(rows *sql.Rows) error {
			var b []byte
			var g Screened
			if err := rows.Scan(&b, &g.Stored, &g.Rejected, &g.Dead); err != nil {
				return err
			}
			if g != (Screened{}) {
				out[Hash(b)] = g
			}
			return nil
		}, `WITH s(h) AS (VALUES `+values+`)
SELECT h, EXISTS (SELECT 1 FROM torrent_releases WHERE infohash = s.h),
	EXISTS (SELECT 1 FROM ingest_rejected WHERE infohash = s.h),
	EXISTS (SELECT 1 FROM task_dead WHERE key = task_dead_key(s.h) AND until > ?)
FROM s`, args...)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Seen moves the last sighting of the stored torrents among hashes to at.
func (s *Store) Seen(ctx context.Context, hashes []Hash, at time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for c := range slices.Chunk(hashes, screenChunk) {
			args := make([]any, len(c))
			for i, h := range c {
				args[i] = h[:]
			}
			if err := touch(ctx, tx, args, at); err != nil {
				return err
			}
		}
		return nil
	})
}

// touch moves the last sighting of the stored torrents whose infohash blobs
// are hashes to at.
func touch(ctx context.Context, tx *sql.Tx, hashes []any, at time.Time) error {
	_, err := tx.ExecContext(ctx, "UPDATE torrent_releases SET last_seen = max(last_seen, ?) WHERE infohash IN ("+placeholders(len(hashes))+")",
		append([]any{at.Unix()}, hashes...)...)
	return err
}
