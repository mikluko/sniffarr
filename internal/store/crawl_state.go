package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Crawl is what a feed source knows of a URL it fetched: when first and
// last, what the last fetch answered, and the validators a conditional
// fetch sends back.
type Crawl struct {
	Source string
	URL    string
	// CreatedAt is the first fetch, UpdatedAt the last.
	CreatedAt time.Time
	UpdatedAt time.Time
	// Status is the last fetch's HTTP status, 0 when no answer came.
	Status int
	// ETag and LastModified are the last answer's, "" for none.
	ETag         string
	LastModified string
	// Error is why the last fetch failed, "" when it did not.
	Error string
}

// Fetched reports whether the last fetch answered 2xx.
func (c Crawl) Fetched() bool { return c.Status >= 200 && c.Status < 300 }

// CrawlState returns what the source knows of the URL; ok is false when it
// never fetched it.
func (s *Store) CrawlState(ctx context.Context, source, url string) (c Crawl, ok bool, err error) {
	var created, updated int64
	err = s.r.QueryRowContext(ctx, "SELECT created_at, updated_at, status, etag, last_modified, error FROM crawl_state WHERE source = ? AND url = ?", source, url).
		Scan(&created, &updated, &c.Status, &c.ETag, &c.LastModified, &c.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return Crawl{}, false, nil
	}
	if err != nil {
		return Crawl{}, false, err
	}
	c.Source, c.URL = source, url
	c.CreatedAt, c.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return c, true, nil
}

// RecordCrawl records a fetch of c.URL by c.Source at now: its status,
// validators and error replace the last fetch's, CreatedAt stays the
// first's.
func (s *Store) RecordCrawl(ctx context.Context, c Crawl, now time.Time) error {
	_, err := s.w.ExecContext(ctx, `INSERT INTO crawl_state (source, url, created_at, updated_at, status, etag, last_modified, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source, url) DO UPDATE SET updated_at = excluded.updated_at, status = excluded.status,
			etag = excluded.etag, last_modified = excluded.last_modified, error = excluded.error`,
		c.Source, c.URL, now.Unix(), now.Unix(), c.Status, c.ETag, c.LastModified, c.Error)
	return err
}
