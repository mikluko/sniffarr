// Package feedtest has a feed.Crawler in memory for the tests of sources.
package feedtest

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
)

// Crawler serves Pages by URL and remembers what it served: Calls in order,
// and the URLs that Once fetched, so a second Once skips them. A URL not in
// Pages answers 404.
type Crawler struct {
	Pages map[string]string

	mu    sync.Mutex
	Calls []string
	once  map[string]bool
}

// Fetch serves the page, every time.
func (c *Crawler) Fetch(_ context.Context, url string) (io.ReadCloser, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Calls = append(c.Calls, url)
	return c.page(url)
}

// Once serves the page the first time it is asked for and succeeds.
func (c *Crawler) Once(_ context.Context, url string) (io.ReadCloser, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.once[url] {
		return nil, false, nil
	}
	c.Calls = append(c.Calls, url)
	body, ok, err := c.page(url)
	if err == nil {
		if c.once == nil {
			c.once = map[string]bool{}
		}
		c.once[url] = true
	}
	return body, ok, err
}

func (c *Crawler) page(url string) (io.ReadCloser, bool, error) {
	p, ok := c.Pages[url]
	if !ok {
		return nil, false, fmt.Errorf("%s: 404 Not Found", url)
	}
	return io.NopCloser(strings.NewReader(p)), true, nil
}

// Done lists the URLs Once fetched.
func (c *Crawler) Done() map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]bool{}
	for u := range c.once {
		out[u] = true
	}
	return out
}
