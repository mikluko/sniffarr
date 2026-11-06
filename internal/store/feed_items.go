package store

import (
	"context"
	"database/sql"
	"time"
)

// FeedItem is a release a tracker feed listed.
type FeedItem struct {
	// Source names the feed: "rutracker".
	Source string
	Hash   Hash
	// Title is the release as the source titles it.
	Title string
	// URL is the release's page at the source, "" for none.
	URL string
	// Category is the source's category of the release, "" for none.
	Category string
	// Size is the release's size in bytes as the source gives it, 0 unknown.
	Size int64
	// PublishedAt is when the source listed the release.
	PublishedAt time.Time
}

// FeedRecorded is what RecordFeedItem did with an item.
type FeedRecorded struct {
	// New reports whether the source's item of the hash was not recorded
	// before.
	New bool
	// Stored reports whether the item's torrent is stored.
	Stored bool
}

// RecordFeedItem records the item once per source and hash; a later item of
// the same changes nothing.
func (s *Store) RecordFeedItem(ctx context.Context, it FeedItem, now time.Time) (FeedRecorded, error) {
	var rec FeedRecorded
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO feed_items (source, infohash, title, url, category, size, published_at, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, it.Source, it.Hash[:], it.Title, it.URL, it.Category, it.Size, it.PublishedAt.Unix(), now.Unix())
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		rec.New = n > 0
		return tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM torrent_releases WHERE infohash = ?)", it.Hash[:]).Scan(&rec.Stored)
	})
	return rec, err
}

// FeedItems returns the items recorded of the hash, by source.
func (s *Store) FeedItems(ctx context.Context, h Hash) ([]FeedItem, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT source, title, url, category, size, published_at FROM feed_items WHERE infohash = ? ORDER BY source", h[:])
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var items []FeedItem
	for rows.Next() {
		it := FeedItem{Hash: h}
		var at int64
		if err := rows.Scan(&it.Source, &it.Title, &it.URL, &it.Category, &it.Size, &at); err != nil {
			return nil, err
		}
		it.PublishedAt = time.Unix(at, 0).UTC()
		items = append(items, it)
	}
	return items, rows.Err()
}
