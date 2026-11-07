package store

import (
	"context"
	"database/sql"
	"slices"
	"time"
)

// WantPeers queues a peer lookup at PriorityRequest for each stored release
// of the hashes, at the time, and returns how many of them are stored. A
// release already queued for one, or being looked up, keeps its place; any
// other is journaled as requested.
func (s *Store) WantPeers(ctx context.Context, hashes []Hash, at time.Time) (int, error) {
	if len(hashes) == 0 {
		return 0, nil
	}
	args := make([]any, 0, len(hashes)+2)
	args = append(args, PriorityRequest, at.UnixMilli())
	for _, h := range hashes {
		args = append(args, h[:])
	}
	var n int
	err := s.tx(ctx, func(tx *sql.Tx) error {
		pending, err := hashesOf(tx.QueryContext(ctx, "SELECT infohash FROM task_queue WHERE kind = 'peers' AND infohash IN ("+placeholders(len(hashes))+")", args[2:]...))
		if err != nil {
			return err
		}
		hs, err := hashesOf(tx.QueryContext(ctx, `INSERT INTO task_queue (kind, infohash, priority, queued_at)
			SELECT 'peers', infohash, ?1, ?2 FROM torrent_releases WHERE infohash IN (`+placeholders(len(hashes))+`)
			ON CONFLICT (kind, infohash) DO UPDATE SET priority = task_queue.priority
			RETURNING infohash`, args...))
		if err != nil {
			return err
		}
		n = len(hs)
		return journalEach(ctx, tx, slices.DeleteFunc(hs, func(h Hash) bool { return slices.Contains(pending, h) }), n, at, JournalPeersWanted, "request", nil)
	})
	return n, err
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
