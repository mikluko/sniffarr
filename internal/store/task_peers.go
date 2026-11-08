package store

import (
	"context"
	"database/sql"
	"time"
)

// WantPeers journals a peer lookup requested, at the time, on each stored
// release of the hashes, and returns their hashes.
func (s *Store) WantPeers(ctx context.Context, hashes []Hash, at time.Time) ([]Hash, error) {
	if len(hashes) == 0 {
		return nil, nil
	}
	args := make([]any, len(hashes))
	for i, h := range hashes {
		args[i] = h[:]
	}
	var stored []Hash
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		if stored, err = hashesOf(tx.QueryContext(ctx, "SELECT infohash FROM torrent_releases WHERE infohash IN ("+placeholders(len(hashes))+")", args...)); err != nil {
			return err
		}
		return journalEach(ctx, tx, stored, len(stored), at, JournalPeersWanted, "request", nil)
	})
	return stored, err
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
