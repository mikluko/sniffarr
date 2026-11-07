// Package latest reads sniffarr's latest stable release from a feed of its
// releases, and keeps the answer between checks.
package latest

import (
	"context"
	"encoding/xml"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/mikluko/sniffarr/internal/version"
)

// URL is the RSS feed of the project's releases.
const URL = "https://sarak.sh/mikluko/sniffarr/releases.rss"

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

// feed is the part of an RSS 2.0 or Atom feed of releases the check reads.
type feed struct {
	Items   []feedItem  `xml:"channel>item"`
	Entries []feedEntry `xml:"entry"`
}

// feedItem is an RSS item.
type feedItem struct {
	Title   string `xml:"title"`
	Link    string `xml:"link"`
	PubDate string `xml:"pubDate"`
}

// feedEntry is an Atom entry.
type feedEntry struct {
	Title string `xml:"title"`
	Links []struct {
		Href string `xml:"href,attr"`
		Rel  string `xml:"rel,attr"`
	} `xml:"link"`
	Published string `xml:"published"`
	Updated   string `xml:"updated"`
}

// releases are the feed's items and entries as releases, those whose
// version is not Tagged included.
func (f feed) releases() []Release {
	var out []Release
	for _, it := range f.Items {
		at, _ := time.Parse(time.RFC1123Z, it.PubDate)
		out = append(out, Release{Version: tagOf(it.Link, it.Title), Published: at.UTC(), URL: it.Link})
	}
	for _, e := range f.Entries {
		link := ""
		for _, l := range e.Links {
			if l.Rel == "" || l.Rel == "alternate" {
				link = l.Href
				break
			}
		}
		at, err := time.Parse(time.RFC3339, e.Published)
		if err != nil {
			at, _ = time.Parse(time.RFC3339, e.Updated)
		}
		out = append(out, Release{Version: tagOf(link, e.Title), Published: at.UTC(), URL: link})
	}
	return out
}

// tagOf is a release's version without "v": the last segment of its link,
// which ends in the tag on Forgejo and GitHub, else its title.
func tagOf(link, title string) string {
	if v := path.Base(link); version.Tagged(v) {
		return strings.TrimPrefix(v, "v")
	}
	return strings.TrimPrefix(strings.TrimSpace(title), "v")
}

// latestStable is the highest stable version among rs; false for none.
func latestStable(rs []Release) (Release, bool) {
	var best Release
	ok := false
	for _, r := range rs {
		if version.Stable(r.Version) && (!ok || version.Newer(r.Version, best.Version)) {
			best, ok = r, true
		}
	}
	return best, ok
}

// fetch reads the feed's latest stable release; false without error when it
// lists none.
func (c *Checker) fetch(ctx context.Context) (Release, bool, error) {
	url := c.URL
	if url == "" {
		url = URL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, false, err
	}
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml")
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
	var f feed
	if err := xml.NewDecoder(res.Body).Decode(&f); err != nil {
		return Release{}, false, fmt.Errorf("%s: %w", url, err)
	}
	r, ok := latestStable(f.releases())
	return r, ok, nil
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
