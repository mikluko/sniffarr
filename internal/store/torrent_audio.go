package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mikluko/sniffarr/internal/audio"
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
	// them; nil without a marker or without a studio.
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

// marksFilled is the prefix of the settings key that records, per language,
// that fillMarks has run. A new prefix makes every database recompute its
// marks and studios once more, which a change to audio.Language.Mark or
// audio.Language.StudiosIn needs.
const marksFilled = "audio_marks_studios_all:"

// fillMarks records, once per language of Options.Languages, the mark and
// studios of every stored marker as audio.Language.Mark and
// audio.Language.StudiosIn give them from the torrent's name and stored
// files.
func (s *Store) fillMarks(ctx context.Context) error {
	for _, tag := range s.opts.Languages {
		l, ok := audio.Lookup(tag)
		if !ok {
			continue
		}
		done, err := s.flag(ctx, marksFilled+l.Tag)
		if err != nil {
			return err
		}
		if done {
			continue
		}
		var after int64
		for {
			type row struct {
				id   int64
				name string
			}
			var batch []row
			if err := s.each(ctx, func(rows *sql.Rows) error {
				var r row
				if err := rows.Scan(&r.id, &r.name); err != nil {
					return err
				}
				batch = append(batch, r)
				return nil
			}, `SELECT t.id, t.name FROM torrent_audio a JOIN torrent_releases t ON t.id = a.torrent_id
				WHERE a.language = ? AND a.marker AND a.torrent_id > ? ORDER BY a.torrent_id LIMIT 1000`, l.Tag, after); err != nil {
				return err
			}
			if len(batch) == 0 {
				break
			}
			marks := make([]string, len(batch))
			studios := make([]sql.NullString, len(batch))
			for i, r := range batch {
				files, err := s.files(ctx, r.id, s.opts.MaxFiles)
				if err != nil {
					return err
				}
				marks[i] = l.Mark(r.name, files)
				if studios[i], err = studiosColumn(l.StudiosIn(r.name, files)); err != nil {
					return err
				}
			}
			if err := s.tx(ctx, func(tx *sql.Tx) error {
				for i, r := range batch {
					if marks[i] == "" {
						continue
					}
					if _, err := tx.ExecContext(ctx, `UPDATE torrent_audio SET mark = ?1, studios = ?2 WHERE torrent_id = ?3 AND language = ?4
						AND (mark IS NOT ?1 OR studios IS NOT ?2)`, marks[i], studios[i], r.id, l.Tag); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				return err
			}
			after = batch[len(batch)-1].id
		}
		if err := s.setFlag(ctx, marksFilled+l.Tag); err != nil {
			return err
		}
	}
	return nil
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
