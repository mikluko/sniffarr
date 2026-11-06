package store

import (
	"context"
	"database/sql"
	"time"
)

// WantPeers records that the peers of the stored releases of the hashes
// were asked to be looked up at the time, and returns how many of them are
// stored. A release already waiting keeps its place.
func (s *Store) WantPeers(ctx context.Context, hashes []Hash, at time.Time) (int, error) {
	if len(hashes) == 0 {
		return 0, nil
	}
	args := make([]any, 0, len(hashes)+1)
	args = append(args, at.Unix())
	for _, h := range hashes {
		args = append(args, h[:])
	}
	res, err := s.w.ExecContext(ctx, `INSERT INTO peer_lookups (infohash, wanted_at)
		SELECT infohash, ?1 FROM torrent_releases WHERE infohash IN (`+placeholders(len(hashes))+`)
		ON CONFLICT (infohash) DO UPDATE SET wanted_at = peer_lookups.wanted_at`, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// TakePeerLookups removes up to n of the lookups WantPeers recorded, the
// longest waiting first, and returns their hashes.
func (s *Store) TakePeerLookups(ctx context.Context, n int) ([]Hash, error) {
	var out []Hash
	err := s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT infohash FROM peer_lookups ORDER BY wanted_at, infohash LIMIT ?", n)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b []byte
			if err := rows.Scan(&b); err != nil {
				return err
			}
			out = append(out, Hash(b))
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, h := range out {
			if _, err := tx.ExecContext(ctx, "DELETE FROM peer_lookups WHERE infohash = ?", h[:]); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// WorkHashes returns the hashes of the stored releases of the work.
func (s *Store) WorkHashes(ctx context.Context, work int64) ([]Hash, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT infohash FROM torrent_releases WHERE work = ? ORDER BY id", work)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hash
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, Hash(b))
	}
	return out, rows.Err()
}
