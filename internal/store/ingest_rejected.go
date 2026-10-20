package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/mikluko/sniffarr/internal/rules"
)

// Reject remembers a hash as rejected for reason. It reports false when the
// hash is already stored or rejected.
func (s *Store) Reject(ctx context.Context, h Hash, reason rules.Reason, at time.Time) (bool, error) {
	var rejected bool
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var stored bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM torrent_releases WHERE infohash = ?)", h[:]).Scan(&stored); err != nil || stored {
			return err
		}
		res, err := tx.ExecContext(ctx, "INSERT INTO ingest_rejected (infohash, reason, rejected_at) VALUES (?, ?, ?) ON CONFLICT DO NOTHING", h[:], string(reason), at.Unix())
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		rejected = n > 0
		return err
	})
	return rejected, err
}
