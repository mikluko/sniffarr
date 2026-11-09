package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// Listing is a release a Torznab source listed.
type Listing struct {
	// Source names the source in the configuration: "spidarr".
	Source string
	Hash   Hash
	// Title is the release as the source titles it.
	Title string
	// Comments is the release's page, "" for none.
	Comments string
	// Categories are the release's Newznab categories in the source's order.
	Categories []int
	// Size is the release's size in bytes as the source gives it, 0 unknown.
	Size int64
	// IMDb is the IMDb ID of the release's work, "tt0078748"; TMDBID and
	// TVDBID are its TMDB and TVDB IDs. "" and 0 name none.
	IMDb   string
	TMDBID int
	TVDBID int
	// PublishedAt is when the source listed the release.
	PublishedAt time.Time
}

// ListingRecorded is what RecordListing did with a listing.
type ListingRecorded struct {
	// New reports whether the source's listing of the hash was not recorded
	// before.
	New bool
	// Stored reports whether the listing's torrent is stored.
	Stored bool
}

// RecordListing records the listing once per source and hash at now; a
// later listing of the same replaces what the source tells of the release
// and when it last listed it, keeping when it was first recorded.
func (s *Store) RecordListing(ctx context.Context, l Listing, now time.Time) (ListingRecorded, error) {
	var rec ListingRecorded
	categories, err := json.Marshal(l.Categories)
	if err != nil {
		return rec, err
	}
	if l.Categories == nil {
		categories = []byte("[]")
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var known bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM torznab_listings WHERE source = ? AND infohash = ?)", l.Source, l.Hash[:]).Scan(&known); err != nil {
			return err
		}
		rec.New = !known
		if _, err := tx.ExecContext(ctx, `INSERT INTO torznab_listings (source, infohash, title, comments, categories, size, imdb, tmdb_id, tvdb_id, published_at, recorded_at, listed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (source, infohash) DO UPDATE SET title = excluded.title, comments = excluded.comments, categories = excluded.categories,
				size = excluded.size, imdb = excluded.imdb, tmdb_id = excluded.tmdb_id, tvdb_id = excluded.tvdb_id, published_at = excluded.published_at,
				listed_at = excluded.listed_at`,
			l.Source, l.Hash[:], l.Title, l.Comments, string(categories), l.Size, l.IMDb, l.TMDBID, l.TVDBID, l.PublishedAt.Unix(), now.Unix(), now.Unix()); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM torrent_releases WHERE infohash = ?)", l.Hash[:]).Scan(&rec.Stored)
	})
	return rec, err
}

// Listings returns the listings recorded of the hash, by source.
func (s *Store) Listings(ctx context.Context, h Hash) ([]Listing, error) {
	var out []Listing
	err := s.each(ctx, func(rows *sql.Rows) error {
		l := Listing{Hash: h}
		var categories string
		var at int64
		if err := rows.Scan(&l.Source, &l.Title, &l.Comments, &categories, &l.Size, &l.IMDb, &l.TMDBID, &l.TVDBID, &at); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(categories), &l.Categories); err != nil {
			return err
		}
		if len(l.Categories) == 0 {
			l.Categories = nil
		}
		l.PublishedAt = time.Unix(at, 0).UTC()
		out = append(out, l)
		return nil
	}, "SELECT source, title, comments, categories, size, imdb, tmdb_id, tvdb_id, published_at FROM torznab_listings WHERE infohash = ? ORDER BY source", h[:])
	return out, err
}

// JournalListed records what a Torznab source's first listing of the hash
// came to.
func (s *Store) JournalListed(ctx context.Context, h Hash, at time.Time, outcome string, detail map[string]any) error {
	return s.tx(ctx, func(tx *sql.Tx) error { return journal(ctx, tx, h, at, JournalListed, outcome, detail) })
}

// SweepListings deletes, batch at a time, the listings no source listed
// since before, and returns how many it deleted.
func (s *Store) SweepListings(ctx context.Context, before time.Time, batch int) (int, error) {
	total := 0
	for {
		var n int
		err := s.tx(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, `DELETE FROM torznab_listings WHERE (source, infohash) IN
				(SELECT source, infohash FROM torznab_listings WHERE listed_at < ? ORDER BY listed_at LIMIT ?)`, before.Unix(), batch)
			if err != nil {
				return err
			}
			d, err := res.RowsAffected()
			n = int(d)
			return err
		})
		if err != nil {
			return total, err
		}
		total += n
		if n < batch {
			return total, nil
		}
	}
}
