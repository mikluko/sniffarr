package store

import (
	"context"
	"time"
)

// WantPeers queues a peer lookup at PriorityRequest for each stored release
// of the hashes, at the time, and returns how many of them are stored. A
// release already queued for one keeps its place.
func (s *Store) WantPeers(ctx context.Context, hashes []Hash, at time.Time) (int, error) {
	if len(hashes) == 0 {
		return 0, nil
	}
	args := make([]any, 0, len(hashes)+2)
	args = append(args, PriorityRequest, at.UnixMilli())
	for _, h := range hashes {
		args = append(args, h[:])
	}
	res, err := s.w.ExecContext(ctx, `INSERT INTO task_queue (kind, infohash, priority, queued_at)
		SELECT 'peers', infohash, ?1, ?2 FROM torrent_releases WHERE infohash IN (`+placeholders(len(hashes))+`)
		ON CONFLICT (kind, infohash) DO UPDATE SET priority = task_queue.priority`, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
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
