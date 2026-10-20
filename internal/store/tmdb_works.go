package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"
)

// LayerTMDB names a title match TMDB's search decided alone.
const LayerTMDB = "tmdb"

// Providers of ExternalID.
const (
	ProviderTMDB     = "tmdb"
	ProviderIMDb     = "imdb"
	ProviderTVDB     = "tvdb"
	ProviderWikidata = "wikidata"
)

// ExternalID is a work's ID with one provider.
type ExternalID struct {
	Provider string
	ID       string
}

// Title is the match of a title key to a work. An empty Layer is no match:
// the key was looked up and nothing fit, or Error says why it failed.
type Title struct {
	Year       int
	Confidence float64
	Layer      string
	// IDs are in provider order.
	IDs   []ExternalID
	Error string
	// MatchedAt is when the key was looked up; zero when it never was.
	MatchedAt time.Time
	// Details are the work's details, nil until they are fetched.
	Details *Details
}

// Details is what TMDB tells of a matched work besides its IDs.
type Details struct {
	// Type is "movie" or "tv".
	Type          string
	OriginalTitle string
	PosterPath    string
	Rating        float64
	Votes         int
	// Runtime is in minutes: a movie's, or a typical episode's for a series.
	Runtime int
	// Status and Seasons are a series'.
	Status  string
	Seasons int
	// Countries are ISO 3166-1 alpha-2 codes; Language is ISO 639-1.
	Countries []string
	Language  string
	// Texts are by language, "ru" and "en".
	Texts map[string]Text
}

// Text is what TMDB tells of a work in one language.
type Text struct {
	Title    string
	Overview string
	Genres   []string
}

// Text returns the work's text in lang; each field it lacks there is taken
// from the other languages in order, and the title from OriginalTitle last.
func (d *Details) Text(lang string) Text {
	if d == nil {
		return Text{}
	}
	out := d.Texts[lang]
	for _, l := range slices.Sorted(maps.Keys(d.Texts)) {
		if l == lang {
			continue
		}
		t := d.Texts[l]
		if out.Title == "" {
			out.Title = t.Title
		}
		if out.Overview == "" {
			out.Overview = t.Overview
		}
		if len(out.Genres) == 0 {
			out.Genres = t.Genres
		}
	}
	if out.Title == "" {
		out.Title = d.OriginalTitle
	}
	return out
}

// TitleKey is a title key awaiting a match, with the kind and name of its
// newest torrent.
type TitleKey struct {
	Key  string
	Kind string
	Name string
}

// Matched is a title key matched to a TMDB work whose details are not
// fetched yet.
type Matched struct {
	Key string
	// Kind is movie or series, as the key's newest torrent says.
	Kind string
	// ID is the work's TMDB ID.
	ID int
}

// UnmatchedTitles returns, newest torrent first, up to limit title keys of
// movies and series that have no match recorded, or only a failed one older
// than before. A non-nil keys narrows them to those keys.
func (s *Store) UnmatchedTitles(ctx context.Context, keys []string, before time.Time, limit int) ([]TitleKey, error) {
	if keys != nil && len(keys) == 0 {
		return nil, nil
	}
	where := "t.title_key IS NOT NULL AND t.kind IN ('movie', 'series') AND (ti.title_key IS NULL OR ti.layer IS NULL AND ti.matched_at < ?)"
	args := []any{before.Unix()}
	if keys != nil {
		where += " AND t.title_key IN (" + placeholders(len(keys)) + ")"
		for _, k := range keys {
			args = append(args, k)
		}
	}
	rows, err := s.r.QueryContext(ctx, `SELECT t.title_key, t.kind, t.name, max(t.id) FROM torrent_releases t
		LEFT JOIN tmdb_works ti ON ti.title_key = t.title_key WHERE `+where+`
		GROUP BY t.title_key ORDER BY max(t.id) DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TitleKey
	for rows.Next() {
		var k TitleKey
		var id int64
		if err := rows.Scan(&k.Key, &k.Kind, &k.Name, &id); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// WithoutDetails returns, oldest match first, up to limit matched title keys
// whose details are not fetched yet and that a stored movie or series has.
func (s *Store) WithoutDetails(ctx context.Context, limit int) ([]Matched, error) {
	var out []Matched
	err := s.each(ctx, func(rows *sql.Rows) error {
		var m Matched
		var id string
		if err := rows.Scan(&m.Key, &m.Kind, &id); err != nil {
			return err
		}
		if _, err := fmt.Sscan(id, &m.ID); err != nil {
			return fmt.Errorf("tmdb id %q of %s: %w", id, m.Key, err)
		}
		out = append(out, m)
		return nil
	}, `SELECT w.title_key, (SELECT r.kind FROM torrent_releases r WHERE r.title_key = w.title_key AND r.kind IN ('movie', 'series') ORDER BY r.id DESC LIMIT 1), i.id
		FROM tmdb_works w JOIN tmdb_ids i ON i.title_key = w.title_key AND i.provider = ?
		WHERE w.layer IS NOT NULL AND w.details_at IS NULL
			AND EXISTS (SELECT 1 FROM torrent_releases r WHERE r.title_key = w.title_key AND r.kind IN ('movie', 'series'))
		ORDER BY w.matched_at, w.title_key LIMIT ?`, ProviderTMDB, limit)
	return out, err
}

// RecordTitle records the match of the title key, replacing any before it,
// with its Details when it has them.
func (s *Store) RecordTitle(ctx context.Context, key string, t Title, at time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tmdb_works (title_key, year, confidence, layer, matched_at, error) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (title_key) DO UPDATE SET year = excluded.year, confidence = excluded.confidence, layer = excluded.layer,
			matched_at = excluded.matched_at, error = excluded.error`,
			key, nullInt(t.Year), nullFloat(t.Layer != "", t.Confidence), null(t.Layer), at.Unix(), null(t.Error)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM tmdb_ids WHERE title_key = ?", key); err != nil {
			return err
		}
		if err := insertIDs(ctx, tx, key, t.IDs); err != nil {
			return err
		}
		return writeDetails(ctx, tx, key, t.Details, at)
	})
}

// RecordDetails records the details of the matched title key, and the IDs
// with them, keeping the IDs of other providers.
func (s *Store) RecordDetails(ctx context.Context, key string, d Details, ids []ExternalID, at time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := insertIDs(ctx, tx, key, ids); err != nil {
			return err
		}
		return writeDetails(ctx, tx, key, &d, at)
	})
}

func insertIDs(ctx context.Context, tx *sql.Tx, key string, ids []ExternalID) error {
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, "INSERT INTO tmdb_ids (title_key, provider, id) VALUES (?, ?, ?) ON CONFLICT (title_key, provider) DO UPDATE SET id = excluded.id",
			key, id.Provider, id.ID); err != nil {
			return err
		}
	}
	return nil
}

// writeDetails sets the details columns and texts of the key to d, or clears
// them when d is nil.
func writeDetails(ctx context.Context, tx *sql.Tx, key string, d *Details, at time.Time) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM tmdb_texts WHERE title_key = ?", key); err != nil {
		return err
	}
	if d == nil {
		_, err := tx.ExecContext(ctx, `UPDATE tmdb_works SET type = NULL, original_title = NULL, poster_path = NULL, rating = NULL, votes = NULL,
			runtime = NULL, status = NULL, seasons = NULL, countries = NULL, language = NULL, details_at = NULL WHERE title_key = ?`, key)
		return err
	}
	countries, err := jsonList(d.Countries)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tmdb_works SET type = ?, original_title = ?, poster_path = ?, rating = ?, votes = ?,
		runtime = ?, status = ?, seasons = ?, countries = ?, language = ?, details_at = ? WHERE title_key = ?`,
		null(d.Type), null(d.OriginalTitle), null(d.PosterPath), nullFloat(d.Rating != 0, d.Rating), nullInt(d.Votes),
		nullInt(d.Runtime), null(d.Status), nullInt(d.Seasons), countries, null(d.Language), at.Unix(), key); err != nil {
		return err
	}
	for _, lang := range slices.Sorted(maps.Keys(d.Texts)) {
		t := d.Texts[lang]
		genres, err := jsonList(t.Genres)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO tmdb_texts (title_key, lang, title, overview, genres) VALUES (?, ?, ?, ?, ?)",
			key, lang, null(t.Title), null(t.Overview), genres); err != nil {
			return err
		}
	}
	return nil
}

// jsonList is the JSON of a list of strings, or nil for an empty one.
func jsonList(xs []string) (any, error) {
	if len(xs) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(xs)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// parseList reads a list of strings jsonList wrote; "" is an empty list.
func parseList(s string) ([]string, error) {
	if s == "" {
		return nil, nil
	}
	var xs []string
	if err := json.Unmarshal([]byte(s), &xs); err != nil {
		return nil, err
	}
	return xs, nil
}

// titles returns the recorded matches of the keys, with their IDs and
// details; a key never looked up is absent.
func (s *Store) titles(ctx context.Context, keys []string) (map[string]Title, error) {
	found := map[string]Title{}
	if len(keys) == 0 {
		return found, nil
	}
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	in := placeholders(len(keys))
	if err := s.each(ctx, func(rows *sql.Rows) error {
		var k, countries string
		var t Title
		var matched int64
		var details sql.NullInt64
		var d Details
		if err := rows.Scan(&k, &t.Year, &t.Confidence, &t.Layer, &t.Error, &matched, &details, &d.Type, &d.OriginalTitle, &d.PosterPath,
			&d.Rating, &d.Votes, &d.Runtime, &d.Status, &d.Seasons, &countries, &d.Language); err != nil {
			return err
		}
		t.MatchedAt = time.Unix(matched, 0).UTC()
		if details.Valid {
			var err error
			if d.Countries, err = parseList(countries); err != nil {
				return fmt.Errorf("countries of %s: %w", k, err)
			}
			t.Details = &d
		}
		found[k] = t
		return nil
	}, `SELECT title_key, coalesce(year, 0), coalesce(confidence, 0), coalesce(layer, ''), coalesce(error, ''), matched_at, details_at,
		coalesce(type, ''), coalesce(original_title, ''), coalesce(poster_path, ''), coalesce(rating, 0), coalesce(votes, 0), coalesce(runtime, 0),
		coalesce(status, ''), coalesce(seasons, 0), coalesce(countries, ''), coalesce(language, '')
		FROM tmdb_works WHERE title_key IN (`+in+`)`, args...); err != nil {
		return nil, err
	}
	if err := s.each(ctx, func(rows *sql.Rows) error {
		var k string
		var id ExternalID
		if err := rows.Scan(&k, &id.Provider, &id.ID); err != nil {
			return err
		}
		t := found[k]
		t.IDs = append(t.IDs, id)
		found[k] = t
		return nil
	}, "SELECT title_key, provider, id FROM tmdb_ids WHERE title_key IN ("+in+") ORDER BY title_key, provider", args...); err != nil {
		return nil, err
	}
	return found, s.each(ctx, func(rows *sql.Rows) error {
		var k, lang, genres string
		var x Text
		if err := rows.Scan(&k, &lang, &x.Title, &x.Overview, &genres); err != nil {
			return err
		}
		var err error
		if x.Genres, err = parseList(genres); err != nil {
			return fmt.Errorf("genres of %s: %w", k, err)
		}
		t := found[k]
		if t.Details == nil {
			return nil
		}
		if t.Details.Texts == nil {
			t.Details.Texts = map[string]Text{}
		}
		t.Details.Texts[lang] = x
		return nil
	}, "SELECT title_key, lang, coalesce(title, ''), coalesce(overview, ''), coalesce(genres, '') FROM tmdb_texts WHERE title_key IN ("+in+")", args...)
}

// tmdb fills in the Title of the releases when Options.TMDB is set.
func (s *Store) tmdb(ctx context.Context, rs []Release) error {
	if !s.opts.TMDB {
		return nil
	}
	byKey := map[string][]int{}
	for i, r := range rs {
		if r.TitleKey != "" {
			byKey[r.TitleKey] = append(byKey[r.TitleKey], i)
		}
	}
	if len(byKey) == 0 {
		return nil
	}
	found, err := s.titles(ctx, slices.Sorted(maps.Keys(byKey)))
	if err != nil {
		return err
	}
	for k, is := range byKey {
		for _, i := range is {
			rs[i].Title = found[k]
		}
	}
	return nil
}

// workTitles fills in the TMDB match of the works by work key from the title
// keys, by work key, when Options.TMDB is set.
func (s *Store) workTitles(ctx context.Context, works map[string]Work, titleKeys map[string]string) error {
	if !s.opts.TMDB {
		return nil
	}
	var keys []string
	for _, k := range titleKeys {
		if k != "" && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	found, err := s.titles(ctx, keys)
	if err != nil {
		return err
	}
	for work, k := range titleKeys {
		if w, ok := works[work]; ok {
			w.TMDB = found[k]
			works[work] = w
		}
	}
	return nil
}
