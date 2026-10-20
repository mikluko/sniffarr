package store

import (
	"context"

	"github.com/mikluko/sniffarr/internal/rules"
)

// files returns the first limit stored files of the torrent with the ID, in
// stored order.
func (s *Store) files(ctx context.Context, id int64, limit int) ([]rules.File, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT path, size FROM torrent_files WHERE torrent_id = ? ORDER BY idx LIMIT ?", id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rules.File
	for rows.Next() {
		var f rules.File
		if err := rows.Scan(&f.Path, &f.Size); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
