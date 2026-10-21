package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/mikluko/sniffarr/internal/names"
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

// TMDB's types of a work.
const (
	TypeMovie = "movie"
	TypeTV    = "tv"
)

// TypeOf is the TMDB type of works of the kind: TypeTV for a series, else
// TypeMovie.
func TypeOf(kind string) string {
	if kind == names.Series {
		return TypeTV
	}
	return TypeMovie
}

// KindOf is the kind of releases of works of the TMDB type.
func KindOf(typ string) string {
	if typ == TypeTV {
		return names.Series
	}
	return names.Movie
}

// SearchTTL is how long CachedSearch serves a recorded TMDB search answer.
const SearchTTL = 6 * time.Hour

// ExternalID is a work's ID with one provider.
type ExternalID struct {
	Provider string
	ID       string
}

// Work is a movie or series on TMDB, one per TMDB type and ID, stored with
// or without releases.
type Work struct {
	// ID is the store's ID of the work, what Release.Work holds.
	ID     int64
	TMDBID int
	Details
	// Popularity is TMDB's popularity in the latest search answer that had
	// the work; 0 while none had.
	Popularity float64
	// PeersWeek is the distinct peers of its releases over the last 7 days,
	// as ComputeTop last counted them.
	PeersWeek int64
	// IDs are its IDs with providers other than TMDB, in provider order.
	IDs []ExternalID
	// DetailsAt is when its details were fetched; zero while only a search
	// answer told of it.
	DetailsAt time.Time
}

// Kind is the kind of the work's releases, movie or series.
func (w Work) Kind() string {
	return KindOf(w.Type)
}

// ExternalIDs are the work's TMDB ID and IDs, in provider order.
func (w Work) ExternalIDs() []ExternalID {
	out := append([]ExternalID{{Provider: ProviderTMDB, ID: fmt.Sprint(w.TMDBID)}}, w.IDs...)
	slices.SortFunc(out, func(a, b ExternalID) int { return strings.Compare(a.Provider, b.Provider) })
	return out
}

// Details is what TMDB tells of a work besides its IDs.
type Details struct {
	// Type is TypeMovie or TypeTV.
	Type          string
	OriginalTitle string
	// Year is the year of release or first air, 0 when unknown.
	Year int
	// PosterPath is the poster of the first language fetched, shown where no
	// Text has one.
	PosterPath string
	Rating     float64
	Votes      int
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
	Title      string
	Overview   string
	Genres     []string
	PosterPath string
}

// Text returns the work's text in lang; each field it lacks there is taken
// from the other languages in order, and the title from OriginalTitle and
// the poster from PosterPath last.
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
		if out.PosterPath == "" {
			out.PosterPath = t.PosterPath
		}
	}
	if out.Title == "" {
		out.Title = d.OriginalTitle
	}
	if out.PosterPath == "" {
		out.PosterPath = d.PosterPath
	}
	return out
}

// Hit is a work as one TMDB search answer gives it.
type Hit struct {
	// Type is TypeMovie or TypeTV.
	Type   string
	TMDBID int
	// Lang is the language of Title and Overview, "en" or "ru".
	Lang          string
	Title         string
	OriginalTitle string
	Overview      string
	PosterPath    string
	Year          int
	Popularity    float64
}

// Found is what looking a title key up on TMDB found: the work in Hit, or
// nothing when Hit is nil, with Error saying why when the lookup failed.
type Found struct {
	Hit        *Hit
	Confidence float64
	Layer      string
	Error      string
}

// Match is the recorded match of a title key. A nil Work is no match: the
// key was looked up and nothing fit, or Error says why it failed.
type Match struct {
	Work       *Work
	Confidence float64
	Layer      string
	Error      string
	// MatchedAt is when the key was looked up; zero when it never was.
	MatchedAt time.Time
}

// TitleKey is a title key awaiting a match, with the kind and name of its
// newest torrent.
type TitleKey struct {
	Key  string
	Kind string
	Name string
}

// Due is a work whose details are not fetched yet.
type Due struct {
	Work   int64
	Type   string
	TMDBID int
}

// UnmatchedTitles returns, newest torrent first, up to limit title keys of
// movies and series that have no match recorded, or only one that found
// nothing, older than before. A non-nil keys narrows them to those keys.
func (s *Store) UnmatchedTitles(ctx context.Context, keys []string, before time.Time, limit int) ([]TitleKey, error) {
	if keys != nil && len(keys) == 0 {
		return nil, nil
	}
	where := "t.title_key IS NOT NULL AND t.kind IN ('movie', 'series') AND (m.title_key IS NULL OR m.work IS NULL AND m.matched_at < ?)"
	args := []any{before.Unix()}
	if keys != nil {
		where += " AND t.title_key IN (" + placeholders(len(keys)) + ")"
		for _, k := range keys {
			args = append(args, k)
		}
	}
	var out []TitleKey
	err := s.each(ctx, func(rows *sql.Rows) error {
		var k TitleKey
		var id int64
		if err := rows.Scan(&k.Key, &k.Kind, &k.Name, &id); err != nil {
			return err
		}
		out = append(out, k)
		return nil
	}, `SELECT t.title_key, t.kind, t.name, max(t.id) FROM torrent_releases t
		LEFT JOIN tmdb_matches m ON m.title_key = t.title_key WHERE `+where+`
		GROUP BY t.title_key ORDER BY max(t.id) DESC LIMIT ?`, append(args, limit)...)
	return out, err
}

// WithoutDetails returns up to limit works whose details are not fetched
// yet: those with releases first, then the oldest first.
func (s *Store) WithoutDetails(ctx context.Context, limit int) ([]Due, error) {
	var out []Due
	err := s.each(ctx, func(rows *sql.Rows) error {
		var d Due
		if err := rows.Scan(&d.Work, &d.Type, &d.TMDBID); err != nil {
			return err
		}
		out = append(out, d)
		return nil
	}, `SELECT w.id, w.type, w.tmdb_id FROM tmdb_works w WHERE w.details_at IS NULL
		ORDER BY EXISTS (SELECT 1 FROM torrent_releases r WHERE r.work = w.id) DESC, w.created_at, w.id LIMIT ?`, limit)
	return out, err
}

// RecordMatch records what looking the title key up found, replacing any
// match before it, and points every release of the key at the work found,
// or at none. It returns the work's ID, 0 for none, and whether its details
// are fetched.
func (s *Store) RecordMatch(ctx context.Context, key string, f Found, at time.Time) (work int64, detailed bool, err error) {
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var ref any
		if f.Hit != nil {
			if work, detailed, err = upsertHit(ctx, tx, *f.Hit, at); err != nil {
				return err
			}
			ref = work
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tmdb_matches (title_key, work, layer, confidence, matched_at, error) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (title_key) DO UPDATE SET work = excluded.work, layer = excluded.layer, confidence = excluded.confidence,
			matched_at = excluded.matched_at, error = excluded.error`,
			key, ref, null(f.Layer), nullFloat(f.Hit != nil, f.Confidence), at.Unix(), null(f.Error)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE torrent_releases SET work = ? WHERE title_key = ? AND work IS NOT ?", ref, key, ref)
		return err
	})
	return work, detailed, err
}

// upsertHit stores the work of the hit, taking the hit's title, original
// title, year and poster only while the work's details are not fetched,
// and returns its ID and whether they are.
func upsertHit(ctx context.Context, tx *sql.Tx, h Hit, at time.Time) (int64, bool, error) {
	var id int64
	var detailed bool
	err := tx.QueryRowContext(ctx, `INSERT INTO tmdb_works (type, tmdb_id, original_title, year, poster_path, popularity, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (type, tmdb_id) DO UPDATE SET popularity = coalesce(excluded.popularity, popularity),
			original_title = CASE WHEN details_at IS NULL THEN coalesce(excluded.original_title, original_title) ELSE original_title END,
			year = CASE WHEN details_at IS NULL THEN coalesce(excluded.year, year) ELSE year END,
			poster_path = CASE WHEN details_at IS NULL THEN coalesce(excluded.poster_path, poster_path) ELSE poster_path END
		RETURNING id, details_at IS NOT NULL`,
		h.Type, h.TMDBID, null(h.OriginalTitle), nullInt(h.Year), null(h.PosterPath), nullFloat(h.Popularity != 0, h.Popularity), at.Unix()).Scan(&id, &detailed)
	if err != nil {
		return 0, false, err
	}
	if detailed || h.Lang == "" {
		return id, detailed, nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tmdb_texts (work, lang, title, overview, poster_path) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (work, lang) DO UPDATE SET title = coalesce(excluded.title, title), overview = coalesce(excluded.overview, overview),
		poster_path = coalesce(excluded.poster_path, poster_path)`,
		id, h.Lang, null(h.Title), null(h.Overview), null(h.PosterPath)); err != nil {
		return 0, false, err
	}
	return id, false, indexTitles(ctx, tx, id)
}

// indexTitles writes the titles of the work into tmdb_titles_fts.
func indexTitles(ctx context.Context, tx *sql.Tx, id int64) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM tmdb_titles_fts WHERE rowid = ?", id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO tmdb_titles_fts (rowid, titles)
		SELECT w.id, concat_ws(char(10), w.original_title, (SELECT group_concat(t.title, char(10)) FROM tmdb_texts t WHERE t.work = w.id))
		FROM tmdb_works w WHERE w.id = ?`, id)
	return err
}

// RecordDetails records the details of the work, and the IDs with them,
// keeping the IDs of other providers. Details without a Type mark the work
// fetched and keep what a search told of it: TMDB refuses to tell more.
func (s *Store) RecordDetails(ctx context.Context, work int64, d Details, ids []ExternalID, at time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if d.Type == "" {
			_, err := tx.ExecContext(ctx, "UPDATE tmdb_works SET details_at = ? WHERE id = ?", at.Unix(), work)
			return err
		}
		for _, id := range ids {
			if id.Provider == ProviderTMDB {
				continue
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO tmdb_ids (work, provider, id) VALUES (?, ?, ?) ON CONFLICT (work, provider) DO UPDATE SET id = excluded.id",
				work, id.Provider, id.ID); err != nil {
				return err
			}
		}
		countries, err := jsonList(d.Countries)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tmdb_works SET original_title = ?, year = ?, poster_path = ?, rating = ?, votes = ?,
			runtime = ?, status = ?, seasons = ?, countries = ?, language = ?, details_at = ? WHERE id = ?`,
			null(d.OriginalTitle), nullInt(d.Year), null(d.PosterPath), nullFloat(d.Rating != 0, d.Rating), nullInt(d.Votes),
			nullInt(d.Runtime), null(d.Status), nullInt(d.Seasons), countries, null(d.Language), at.Unix(), work); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM tmdb_texts WHERE work = ?", work); err != nil {
			return err
		}
		for _, lang := range slices.Sorted(maps.Keys(d.Texts)) {
			t := d.Texts[lang]
			genres, err := jsonList(t.Genres)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO tmdb_texts (work, lang, title, overview, genres, poster_path) VALUES (?, ?, ?, ?, ?, ?)",
				work, lang, null(t.Title), null(t.Overview), genres, null(t.PosterPath)); err != nil {
				return err
			}
		}
		return indexTitles(ctx, tx, work)
	})
}

// RecordSearch stores the works of a TMDB search answer for the query among
// works of the type, and the answer as the works' IDs in hit order, which
// it returns.
func (s *Store) RecordSearch(ctx context.Context, typ, query string, hits []Hit, at time.Time) ([]int64, error) {
	ids := make([]int64, 0, len(hits))
	err := s.tx(ctx, func(tx *sql.Tx) error {
		for _, h := range hits {
			id, _, err := upsertHit(ctx, tx, h, at)
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		b, err := json.Marshal(ids)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO tmdb_searches (kind, query, searched_at, works) VALUES (?, ?, ?, ?)
			ON CONFLICT (kind, query) DO UPDATE SET searched_at = excluded.searched_at, works = excluded.works`, typ, query, at.Unix(), string(b))
		return err
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// CachedSearch returns the works RecordSearch recorded for the query among
// works of the type, and whether it recorded them after after.
func (s *Store) CachedSearch(ctx context.Context, typ, query string, after time.Time) ([]int64, bool, error) {
	var raw string
	err := s.r.QueryRowContext(ctx, "SELECT works FROM tmdb_searches WHERE kind = ? AND query = ? AND searched_at > ?", typ, query, after.Unix()).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var ids []int64
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, false, fmt.Errorf("search %s %q: %w", typ, query, err)
	}
	return ids, true, nil
}

// WorkByTMDB returns the store's ID of the work of the TMDB type and ID, or
// ErrNotFound.
func (s *Store) WorkByTMDB(ctx context.Context, typ string, tmdbID int) (int64, error) {
	var id int64
	err := s.r.QueryRowContext(ctx, "SELECT id FROM tmdb_works WHERE type = ? AND tmdb_id = ?", typ, tmdbID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
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

// int64Args are the IDs as query arguments.
func int64Args(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// Works returns the stored works with the IDs, with their texts and IDs; an
// ID with no work is absent.
func (s *Store) Works(ctx context.Context, ids []int64) (map[int64]Work, error) {
	out := map[int64]Work{}
	for c := range slices.Chunk(ids, 500) {
		if err := s.worksChunk(ctx, c, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) worksChunk(ctx context.Context, ids []int64, out map[int64]Work) error {
	args := int64Args(ids)
	in := placeholders(len(ids))
	if err := s.each(ctx, func(rows *sql.Rows) error {
		var w Work
		var countries string
		var details sql.NullInt64
		if err := rows.Scan(&w.ID, &w.Type, &w.TMDBID, &w.OriginalTitle, &w.Year, &w.PosterPath, &w.Rating, &w.Votes, &w.Runtime,
			&w.Status, &w.Seasons, &countries, &w.Language, &w.Popularity, &w.PeersWeek, &details); err != nil {
			return err
		}
		var err error
		if w.Countries, err = parseList(countries); err != nil {
			return fmt.Errorf("countries of work %d: %w", w.ID, err)
		}
		if details.Valid {
			w.DetailsAt = time.Unix(details.Int64, 0).UTC()
		}
		out[w.ID] = w
		return nil
	}, `SELECT id, type, tmdb_id, coalesce(original_title, ''), coalesce(year, 0), coalesce(poster_path, ''), coalesce(rating, 0), coalesce(votes, 0),
		coalesce(runtime, 0), coalesce(status, ''), coalesce(seasons, 0), coalesce(countries, ''), coalesce(language, ''), coalesce(popularity, 0),
		peers_week, details_at FROM tmdb_works WHERE id IN (`+in+`)`, args...); err != nil {
		return err
	}
	if err := s.each(ctx, func(rows *sql.Rows) error {
		var id int64
		var x ExternalID
		if err := rows.Scan(&id, &x.Provider, &x.ID); err != nil {
			return err
		}
		w := out[id]
		w.IDs = append(w.IDs, x)
		out[id] = w
		return nil
	}, "SELECT work, provider, id FROM tmdb_ids WHERE work IN ("+in+") ORDER BY work, provider", args...); err != nil {
		return err
	}
	return s.each(ctx, func(rows *sql.Rows) error {
		var id int64
		var lang, genres string
		var x Text
		if err := rows.Scan(&id, &lang, &x.Title, &x.Overview, &genres, &x.PosterPath); err != nil {
			return err
		}
		var err error
		if x.Genres, err = parseList(genres); err != nil {
			return fmt.Errorf("genres of work %d: %w", id, err)
		}
		w, ok := out[id]
		if !ok {
			return nil
		}
		if w.Texts == nil {
			w.Texts = map[string]Text{}
		}
		w.Texts[lang] = x
		out[id] = w
		return nil
	}, "SELECT work, lang, coalesce(title, ''), coalesce(overview, ''), coalesce(genres, ''), coalesce(poster_path, '') FROM tmdb_texts WHERE work IN ("+in+")", args...)
}

// matches returns the recorded matches of the keys, with their works; a key
// never looked up is absent.
func (s *Store) matches(ctx context.Context, keys []string) (map[string]Match, error) {
	found := map[string]Match{}
	work := map[string]int64{}
	for c := range slices.Chunk(keys, 500) {
		args := make([]any, len(c))
		for i, k := range c {
			args[i] = k
		}
		if err := s.each(ctx, func(rows *sql.Rows) error {
			var k string
			var m Match
			var id sql.NullInt64
			var at int64
			if err := rows.Scan(&k, &id, &m.Layer, &m.Confidence, &at, &m.Error); err != nil {
				return err
			}
			m.MatchedAt = time.Unix(at, 0).UTC()
			if id.Valid {
				work[k] = id.Int64
			} else {
				m.Layer, m.Confidence = "", 0
			}
			found[k] = m
			return nil
		}, `SELECT title_key, work, coalesce(layer, ''), coalesce(confidence, 0), matched_at, coalesce(error, '')
			FROM tmdb_matches WHERE title_key IN (`+placeholders(len(c))+`)`, args...); err != nil {
			return nil, err
		}
	}
	works, err := s.Works(ctx, slices.Collect(maps.Values(work)))
	if err != nil {
		return nil, err
	}
	for k, id := range work {
		if w, ok := works[id]; ok {
			m := found[k]
			m.Work = &w
			found[k] = m
		}
	}
	return found, nil
}

// tmdb fills in the Match of the releases when Options.TMDB is set.
func (s *Store) tmdb(ctx context.Context, rs []Release) error {
	if !s.opts.TMDB {
		return nil
	}
	var keys []string
	for _, r := range rs {
		if r.TitleKey != "" && !slices.Contains(keys, r.TitleKey) {
			keys = append(keys, r.TitleKey)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	found, err := s.matches(ctx, keys)
	if err != nil {
		return err
	}
	for i := range rs {
		rs[i].Match = found[rs[i].TitleKey]
	}
	return nil
}
