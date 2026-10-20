package store

import (
	"context"
	"database/sql"
)

// Audio is what is known of a torrent's audio track in one language.
type Audio struct {
	Language string
	// Marker reports a word or studio of the language in the torrent's
	// names.
	Marker bool
	// ScriptHint reports letters of the language's script in the torrent's
	// names.
	ScriptHint bool
	// Probability is nil while unknown; Layer decided it.
	Probability *float64
	Layer       string
}

// audio fills in the Audio of the releases.
func (s *Store) audio(ctx context.Context, rs []Release) error {
	if len(rs) == 0 || len(s.opts.Languages) == 0 {
		return nil
	}
	index := make(map[int64]int, len(rs))
	args := make([]any, 0, len(rs)+len(s.opts.Languages))
	for i, r := range rs {
		index[r.ID] = i
		args = append(args, r.ID)
	}
	for _, l := range s.opts.Languages {
		args = append(args, l)
	}
	rows, err := s.r.QueryContext(ctx, "SELECT torrent_id, language, marker, script_hint, probability, coalesce(layer, '') FROM torrent_audio WHERE torrent_id IN ("+
		placeholders(len(rs))+") AND language IN ("+placeholders(len(s.opts.Languages))+") ORDER BY torrent_id, language", args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var a Audio
		var p sql.NullFloat64
		if err := rows.Scan(&id, &a.Language, &a.Marker, &a.ScriptHint, &p, &a.Layer); err != nil {
			return err
		}
		if p.Valid {
			a.Probability = &p.Float64
		}
		i := index[id]
		rs[i].Audio = append(rs[i].Audio, a)
	}
	return rows.Err()
}
