package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/mikluko/sniffarr/internal/audio"
	"github.com/mikluko/sniffarr/internal/names"
	"github.com/mikluko/sniffarr/internal/rules"
)

// backfillBatch is the default of Store.backfillBatch.
const backfillBatch = 200

// featuresFilled is the settings key that records that the features
// backfill has run. A new key makes every database recompute the features
// of releases without a year or a quality once more, which a change to
// names.Parse needs.
const featuresFilled = "release_features_letter_o"

// marksFilled is the prefix of the settings key that records, per language,
// that the marks backfill has run. A new prefix makes every database
// recompute its marks and studios once more, which a change to
// audio.Language.Mark or audio.Language.StudiosIn needs.
const marksFilled = "audio_marks_no_subtitles:"

// subtitleMarksFilled is the prefix of the settings key that records, per
// language, that the markers stored from a word in a subtitle file's path
// alone were cleared.
const subtitleMarksFilled = "audio_subtitle_marks:"

// codesFilled is the prefix of the settings key that records, per language,
// that the torrents stored before audio.Language.Codes were read got their
// markers from the codes in their names.
const codesFilled = "audio_codes:"

// probeStudiosFilled is the prefix of the settings key that records, per
// language, that the studios named in probed tracks were added to the
// studios of releases probed before they were read.
const probeStudiosFilled = "probe_studios:"

// Backfilled is the progress of one backfill within one Backfill call.
type Backfilled struct {
	Name string
	// Rows is how many rows the call has gone through.
	Rows int
	// Done reports that the backfill finished and will not run again.
	Done bool
}

// Backfill runs, in transactions of a few hundred rows, the one-time
// backfills of stored torrents not yet done: the year, season, episode and
// quality of those without a year or a quality, from their names,
// and, per language of Options.Languages, a marker for every torrent whose
// name has the language's audio.Language.Codes, no marker of the rules layer
// that only a subtitle file's path gave, the mark and studios of every
// stored marker, as audio.Language.Mark and audio.Language.StudiosIn give
// them from the torrent's name and stored files. Each transaction saves how
// far its backfill got, so a call after a cancelled one resumes there.
// progress, when not nil, is called after every transaction.
func (s *Store) Backfill(ctx context.Context, progress func(Backfilled)) error {
	if progress == nil {
		progress = func(Backfilled) {}
	}
	if err := s.backfill(ctx, "release features", featuresFilled, progress, s.featuresBatch); err != nil {
		return fmt.Errorf("release features: %w", err)
	}
	for _, tag := range s.opts.Languages {
		l, ok := audio.Lookup(tag)
		if !ok {
			continue
		}
		name := "audio codes " + l.Tag
		if err := s.backfill(ctx, name, codesFilled+l.Tag, progress, func(ctx context.Context, after int64) (int, int64, func(*sql.Tx) error, error) {
			return s.codesBatch(ctx, l, after)
		}); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		name = "subtitle marks " + l.Tag
		if err := s.backfill(ctx, name, subtitleMarksFilled+l.Tag, progress, func(ctx context.Context, after int64) (int, int64, func(*sql.Tx) error, error) {
			return s.subtitleMarksBatch(ctx, l, after)
		}); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		name = "audio marks " + l.Tag
		if err := s.backfill(ctx, name, marksFilled+l.Tag, progress, func(ctx context.Context, after int64) (int, int64, func(*sql.Tx) error, error) {
			return s.marksBatch(ctx, l, after)
		}); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		name = "probe studios " + l.Tag
		if err := s.backfill(ctx, name, probeStudiosFilled+l.Tag, progress, func(ctx context.Context, after int64) (int, int64, func(*sql.Tx) error, error) {
			return s.probeStudiosBatch(ctx, l, after)
		}); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// batchFunc reads the rows of a backfill after the row ID after and returns
// how many it read, the ID of the last, and the writes they need; none read
// ends the backfill.
type batchFunc func(ctx context.Context, after int64) (n int, last int64, write func(*sql.Tx) error, err error)

// backfill runs next from the position saved for flag until it reads no
// rows, then sets flag; it does nothing once flag is set.
func (s *Store) backfill(ctx context.Context, name, flag string, progress func(Backfilled), next batchFunc) error {
	done, err := s.flag(ctx, flag)
	if err != nil || done {
		return err
	}
	after, err := s.position(ctx, flag)
	if err != nil {
		return err
	}
	rows := 0
	for {
		n, last, write, err := next(ctx, after)
		if err != nil {
			return err
		}
		if n == 0 {
			if err := s.tx(ctx, func(tx *sql.Tx) error {
				if _, err := tx.ExecContext(ctx, "INSERT INTO settings (key, value) VALUES (?, x'01') ON CONFLICT (key) DO NOTHING", flag); err != nil {
					return err
				}
				_, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", positionKey(flag))
				return err
			}); err != nil {
				return err
			}
			progress(Backfilled{Name: name, Rows: rows, Done: true})
			return nil
		}
		if err := s.tx(ctx, func(tx *sql.Tx) error {
			if err := write(tx); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, "INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value",
				positionKey(flag), strconv.AppendInt(nil, last, 10))
			return err
		}); err != nil {
			return err
		}
		rows += n
		after = last
		progress(Backfilled{Name: name, Rows: rows})
	}
}

// positionKey is the settings key of the ID of the last row the backfill
// recorded by flag went through.
func positionKey(flag string) string {
	return flag + ":after"
}

// position returns the ID saved under the position key of flag, 0 when none
// is.
func (s *Store) position(ctx context.Context, flag string) (int64, error) {
	var raw []byte
	err := s.r.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", positionKey(flag)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("stored position %q: %w", raw, err)
	}
	return id, nil
}

func (s *Store) featuresBatch(ctx context.Context, after int64) (int, int64, func(*sql.Tx) error, error) {
	type row struct {
		id int64
		p  names.Parsed
	}
	var batch []row
	if err := s.each(ctx, func(rows *sql.Rows) error {
		var r row
		var name string
		if err := rows.Scan(&r.id, &name); err != nil {
			return err
		}
		r.p = names.Parse(name)
		batch = append(batch, r)
		return nil
	}, "SELECT id, name FROM torrent_releases WHERE id > ? AND (quality IS NULL OR year IS NULL) ORDER BY id LIMIT ?", after, s.backfillBatch); err != nil {
		return 0, 0, nil, err
	}
	if len(batch) == 0 {
		return 0, 0, nil, nil
	}
	return len(batch), batch[len(batch)-1].id, func(tx *sql.Tx) error {
		for _, r := range batch {
			if _, err := tx.ExecContext(ctx, "UPDATE torrent_releases SET year = ?, season = ?, episode = ?, quality = ? WHERE id = ?",
				nullInt(r.p.Year), nullInt(r.p.Season), nullInt(r.p.Episode), null(r.p.Quality), r.id); err != nil {
				return err
			}
		}
		return nil
	}, nil
}

// codesBatch reads the torrents after the row ID after and writes a marker in
// l, as a marker found at ingest is, for each whose name has l's codes. A
// probe that read a track in l keeps its layer.
func (s *Store) codesBatch(ctx context.Context, l audio.Language, after int64) (int, int64, func(*sql.Tx) error, error) {
	type row struct {
		id         int64
		name       string
		mark       string
		studios    sql.NullString
		scriptHint bool
	}
	var batch, coded []row
	if err := s.each(ctx, func(rows *sql.Rows) error {
		var r row
		if err := rows.Scan(&r.id, &r.name); err != nil {
			return err
		}
		batch = append(batch, r)
		return nil
	}, "SELECT id, name FROM torrent_releases WHERE id > ? ORDER BY id LIMIT ?", after, s.backfillBatch); err != nil {
		return 0, 0, nil, err
	}
	if len(batch) == 0 {
		return 0, 0, nil, nil
	}
	for _, r := range batch {
		if l.CodesIn(r.name) == nil {
			continue
		}
		files, err := s.files(ctx, r.id, s.opts.MaxFiles)
		if err != nil {
			return 0, 0, nil, err
		}
		r.mark, r.scriptHint = l.Mark(r.name, files), l.ScriptHint(r.name, files)
		if r.studios, err = studiosColumn(l.StudiosIn(r.name, files)); err != nil {
			return 0, 0, nil, err
		}
		coded = append(coded, r)
	}
	return len(batch), batch[len(batch)-1].id, func(tx *sql.Tx) error {
		for _, r := range coded {
			if _, err := tx.ExecContext(ctx, `INSERT INTO torrent_audio (torrent_id, language, marker, mark, studios, script_hint, probability, layer)
				VALUES (?1, ?2, 1, ?3, ?4, ?5, 1, ?6) ON CONFLICT (torrent_id, language) DO UPDATE SET marker = 1, mark = ?3,
				studios = coalesce(torrent_audio.studios, ?4), probability = 1,
				layer = CASE WHEN torrent_audio.layer = ?7 AND torrent_audio.probability = 1 THEN ?7 ELSE ?6 END`,
				r.id, l.Tag, r.mark, r.studios, r.scriptHint, LayerRules, LayerProbe); err != nil {
				return err
			}
		}
		return nil
	}, nil
}

// subtitleMarksBatch reads the markers in l of the rules layer after the
// row ID after, and clears the marker, mark, probability and layer of each
// that only a word in a subtitle file's path gave.
func (s *Store) subtitleMarksBatch(ctx context.Context, l audio.Language, after int64) (int, int64, func(*sql.Tx) error, error) {
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
		WHERE a.language = ? AND a.marker AND a.layer = ? AND a.torrent_id > ? ORDER BY a.torrent_id LIMIT ?`, l.Tag, LayerRules, after, s.backfillBatch); err != nil {
		return 0, 0, nil, err
	}
	if len(batch) == 0 {
		return 0, 0, nil, nil
	}
	var unmarked []int64
	for _, r := range batch {
		files, err := s.files(ctx, r.id, s.opts.MaxFiles)
		if err != nil {
			return 0, 0, nil, err
		}
		if !l.Marker(r.name, files) && slices.ContainsFunc(files, func(f rules.File) bool { return audio.Subtitle(f.Path) && l.Marker(f.Path, nil) }) {
			unmarked = append(unmarked, r.id)
		}
	}
	return len(batch), batch[len(batch)-1].id, func(tx *sql.Tx) error {
		for _, id := range unmarked {
			if _, err := tx.ExecContext(ctx, `UPDATE torrent_audio SET marker = 0, mark = NULL, probability = NULL, layer = NULL
				WHERE torrent_id = ? AND language = ? AND layer = ?`, id, l.Tag, LayerRules); err != nil {
				return err
			}
		}
		return nil
	}, nil
}

func (s *Store) marksBatch(ctx context.Context, l audio.Language, after int64) (int, int64, func(*sql.Tx) error, error) {
	type row struct {
		id      int64
		name    string
		mark    string
		studios sql.NullString
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
		WHERE a.language = ? AND a.marker AND a.torrent_id > ? ORDER BY a.torrent_id LIMIT ?`, l.Tag, after, s.backfillBatch); err != nil {
		return 0, 0, nil, err
	}
	if len(batch) == 0 {
		return 0, 0, nil, nil
	}
	for i := range batch {
		r := &batch[i]
		files, err := s.files(ctx, r.id, s.opts.MaxFiles)
		if err != nil {
			return 0, 0, nil, err
		}
		r.mark = l.Mark(r.name, files)
		if r.studios, err = studiosColumn(l.StudiosIn(r.name, files)); err != nil {
			return 0, 0, nil, err
		}
	}
	return len(batch), batch[len(batch)-1].id, func(tx *sql.Tx) error {
		for _, r := range batch {
			if r.mark == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE torrent_audio SET mark = ?1, studios = ?2 WHERE torrent_id = ?3 AND language = ?4
				AND (mark IS NOT ?1 OR studios IS NOT ?2)`, r.mark, r.studios, r.id, l.Tag); err != nil {
				return err
			}
		}
		return nil
	}, nil
}

// probeStudiosBatch reads the releases with probed audio tracks after the
// row ID after, and writes the studios of l named in the tracks in l to
// each release's audio in l, after those stored.
func (s *Store) probeStudiosBatch(ctx context.Context, l audio.Language, after int64) (int, int64, func(*sql.Tx) error, error) {
	var ids []int64
	if err := s.each(ctx, func(rows *sql.Rows) error {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
		return nil
	}, "SELECT DISTINCT torrent_id FROM probe_tracks WHERE kind = 'audio' AND torrent_id > ? ORDER BY torrent_id LIMIT ?", after, s.backfillBatch); err != nil {
		return 0, 0, nil, err
	}
	if len(ids) == 0 {
		return 0, 0, nil, nil
	}
	names := map[int64][]string{}
	if err := s.each(ctx, func(rows *sql.Rows) error {
		var id int64
		var t Track
		if err := rows.Scan(&id, &t.Language, &t.LanguageBCP47, &t.Name); err != nil {
			return err
		}
		if t.In(l.Tag) {
			names[id] = append(names[id], t.Name)
		}
		return nil
	}, "SELECT torrent_id, language, language_bcp47, name FROM probe_tracks WHERE kind = 'audio' AND torrent_id IN ("+placeholders(len(ids))+")", int64Args(ids)...); err != nil {
		return 0, 0, nil, err
	}
	return len(ids), ids[len(ids)-1], func(tx *sql.Tx) error {
		for _, id := range ids {
			if len(names[id]) == 0 {
				continue
			}
			studios, err := mergedStudios(ctx, tx, id, l.Tag, names[id])
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE torrent_audio SET studios = ?1 WHERE torrent_id = ?2 AND language = ?3 AND studios IS NOT ?1", studios, id, l.Tag); err != nil {
				return err
			}
		}
		return nil
	}, nil
}
