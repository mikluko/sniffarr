// Package latest reads sniffarr's latest stable release from the forge it
// is published on, and keeps the answer between checks.
package latest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// URL is the Forgejo endpoint of the project's latest stable release.
const URL = "https://forge.sarak.sh/api/v1/repos/mikluko/sniffarr/releases/latest"

// DefaultInterval is the wait between checks that answered; DefaultRetry
// the wait after one that failed.
const (
	DefaultInterval = 24 * time.Hour
	DefaultRetry    = time.Hour
)

// Release is a published release.
type Release struct {
	// Version has no leading "v".
	Version   string
	Published time.Time
	// URL is the page of its release notes.
	URL string
}

// Checker asks URL for the latest stable release at most once per
// Interval. The zero value is ready; it is safe for concurrent use.
type Checker struct {
	// URL is the package's URL when empty.
	URL string
	// HTTP is http.DefaultClient when nil.
	HTTP *http.Client
	// Interval is DefaultInterval and Retry DefaultRetry when zero.
	Interval, Retry time.Duration
	// Log discards everything when nil.
	Log *slog.Logger
	// Now is time.Now when nil.
	Now func() time.Time

	mu     sync.Mutex
	latest Release
	ok     bool
	next   time.Time
}

// Latest returns the release the last check read; false before the first
// check, and after one that failed or found no stable release.
func (c *Checker) Latest() (Release, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.latest, c.ok
}

// Refresh checks when the last check is older than Interval, or than Retry
// after a failure, and keeps what it reads; a failed check keeps nothing.
func (c *Checker) Refresh(ctx context.Context) {
	now := c.now()
	c.mu.Lock()
	due := !now.Before(c.next)
	c.mu.Unlock()
	if !due {
		return
	}
	r, ok, err := c.fetch(ctx)
	wait := c.interval()
	if err != nil {
		c.log().WarnContext(ctx, "release check", "err", err)
		wait = c.retry()
	}
	c.mu.Lock()
	c.latest, c.ok = r, ok
	c.next = now.Add(wait)
	c.mu.Unlock()
}

// Run refreshes at start and whenever the next check is due, until ctx is
// done.
func (c *Checker) Run(ctx context.Context) error {
	for {
		c.Refresh(ctx)
		c.mu.Lock()
		wait := c.next.Sub(c.now())
		c.mu.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

// forgejoRelease is the part of a Forgejo release the check reads.
type forgejoRelease struct {
	TagName     string    `json:"tag_name"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Prerelease  bool      `json:"prerelease"`
	Draft       bool      `json:"draft"`
}

// fetch reads the latest release; false without error when there is no
// stable one.
func (c *Checker) fetch(ctx context.Context) (Release, bool, error) {
	url := c.URL
	if url == "" {
		url = URL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, false, err
	}
	req.Header.Set("Accept", "application/json")
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return Release{}, false, err
	}
	defer func() { _ = res.Body.Close() }()
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return Release{}, false, nil
	default:
		return Release{}, false, fmt.Errorf("%s: %s", url, res.Status)
	}
	var fr forgejoRelease
	if err := json.NewDecoder(res.Body).Decode(&fr); err != nil {
		return Release{}, false, fmt.Errorf("%s: %w", url, err)
	}
	if fr.Prerelease || fr.Draft || fr.TagName == "" {
		return Release{}, false, nil
	}
	return Release{Version: strings.TrimPrefix(fr.TagName, "v"), Published: fr.PublishedAt.UTC(), URL: fr.HTMLURL}, true, nil
}

func (c *Checker) interval() time.Duration {
	if c.Interval > 0 {
		return c.Interval
	}
	return DefaultInterval
}

func (c *Checker) retry() time.Duration {
	if c.Retry > 0 {
		return c.Retry
	}
	return DefaultRetry
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Checker) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.New(slog.DiscardHandler)
}
