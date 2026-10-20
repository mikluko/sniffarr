package store

import (
	"context"
	"database/sql"
	"errors"
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
}

// TitleKey is a title key awaiting a match, with the kind and name of its
// newest torrent.
type TitleKey struct {
	Key  string
	Kind string
	Name string
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
	rows, err := s.r.QueryContext(ctx, `SELECT t.title_key, t.kind, t.name, max(t.id) FROM torrents t
		LEFT JOIN titles ti ON ti.title_key = t.title_key WHERE `+where+`
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

// RecordTitle records the match of the title key, replacing any before it.
func (s *Store) RecordTitle(ctx context.Context, key string, t Title, at time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO titles (title_key, year, confidence, layer, matched_at, error) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (title_key) DO UPDATE SET year = excluded.year, confidence = excluded.confidence, layer = excluded.layer,
			matched_at = excluded.matched_at, error = excluded.error`,
			key, nullInt(t.Year), nullFloat(t.Layer != "", t.Confidence), null(t.Layer), at.Unix(), null(t.Error)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM title_ids WHERE title_key = ?", key); err != nil {
			return err
		}
		for _, id := range t.IDs {
			if _, err := tx.ExecContext(ctx, "INSERT INTO title_ids (title_key, provider, id) VALUES (?, ?, ?)", key, id.Provider, id.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

// titles fills in the Title of the releases when Options.Titles is set.
func (s *Store) titles(ctx context.Context, rs []Release) error {
	if !s.opts.Titles {
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
	args := make([]any, 0, len(byKey))
	for k := range byKey {
		args = append(args, k)
	}
	in := placeholders(len(byKey))
	rows, err := s.r.QueryContext(ctx, "SELECT title_key, coalesce(year, 0), coalesce(confidence, 0), coalesce(layer, ''), coalesce(error, '') FROM titles WHERE title_key IN ("+in+")", args...)
	if err != nil {
		return err
	}
	found := map[string]Title{}
	for rows.Next() {
		var k string
		var t Title
		if err := rows.Scan(&k, &t.Year, &t.Confidence, &t.Layer, &t.Error); err != nil {
			rows.Close()
			return err
		}
		found[k] = t
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	rows, err = s.r.QueryContext(ctx, "SELECT title_key, provider, id FROM title_ids WHERE title_key IN ("+in+") ORDER BY title_key, provider", args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var id ExternalID
		if err := rows.Scan(&k, &id.Provider, &id.ID); err != nil {
			return err
		}
		t := found[k]
		t.IDs = append(t.IDs, id)
		found[k] = t
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for k, is := range byKey {
		for _, i := range is {
			rs[i].Title = found[k]
		}
	}
	return nil
}

func nullInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func nullFloat(valid bool, f float64) any {
	if !valid {
		return nil
	}
	return f
}
