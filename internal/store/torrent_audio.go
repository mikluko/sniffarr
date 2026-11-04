package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Audio is what is known of a torrent's audio track in one language.
type Audio struct {
	Language string
	// Marker reports a word or studio of the language in the torrent's
	// names; Mark is it as written there, "" when the marker is only in a
	// file path not stored.
	Marker bool
	Mark   string
	// Studios are the display names of the language's studios in the
	// torrent's name or stored file paths, as audio.Language.StudiosIn gives
	// them, and in the names of the probed tracks in the language; nil
	// without a studio.
	Studios []string
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
	ids := make([]int64, len(rs))
	for i, r := range rs {
		index[r.ID] = i
		ids[i] = r.ID
	}
	langs := make([]any, len(s.opts.Languages))
	for i, l := range s.opts.Languages {
		langs[i] = l
	}
	return s.each(ctx, func(rows *sql.Rows) error {
		var id int64
		var a Audio
		var p sql.NullFloat64
		var studios sql.NullString
		if err := rows.Scan(&id, &a.Language, &a.Marker, &a.Mark, &studios, &a.ScriptHint, &p, &a.Layer); err != nil {
			return err
		}
		if studios.Valid {
			if err := json.Unmarshal([]byte(studios.String), &a.Studios); err != nil {
				return fmt.Errorf("torrent %d audio %s studios: %w", id, a.Language, err)
			}
		}
		if p.Valid {
			a.Probability = &p.Float64
		}
		i := index[id]
		rs[i].Audio = append(rs[i].Audio, a)
		return nil
	}, "SELECT torrent_id, language, marker, coalesce(mark, ''), studios, script_hint, probability, coalesce(layer, '') FROM torrent_audio WHERE torrent_id IN ("+
		placeholders(len(ids))+") AND language IN ("+placeholders(len(langs))+") ORDER BY torrent_id, language", append(int64Args(ids), langs...)...)
}

// studiosColumn is the torrent_audio.studios value of studios: a JSON array,
// NULL for none.
func studiosColumn(studios []string) (sql.NullString, error) {
	if len(studios) == 0 {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(studios)
	return sql.NullString{String: string(b), Valid: true}, err
}

// flag reports whether the settings key is set.
func (s *Store) flag(ctx context.Context, key string) (bool, error) {
	var one int
	err := s.r.QueryRowContext(ctx, "SELECT 1 FROM settings WHERE key = ?", key).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// setFlag sets the settings key.
func (s *Store) setFlag(ctx context.Context, key string) error {
	_, err := s.w.ExecContext(ctx, "INSERT INTO settings (key, value) VALUES (?, x'01') ON CONFLICT (key) DO NOTHING", key)
	return err
}
