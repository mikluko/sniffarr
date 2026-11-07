package latest

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// forge serves body with status at the feed, counting the requests.
func forge(t *testing.T, status *atomic.Int32, body *atomic.Value) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// rss is a Forgejo releases feed listing items, newest first.
func rss(items ...string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>sniffarr</title>` + strings.Join(items, "") + `</channel></rss>`
}

// item is a Forgejo releases feed item of tag.
func item(tag, date string) string {
	return `<item><title>` + tag + `</title><link>https://sarak.sh/mikluko/sniffarr/releases/tag/` + tag + `</link><pubDate>` + date + `</pubDate></item>`
}

var v046 = rss(
	item("v0.47.0-rc.1", "Wed, 02 Dec 2026 09:00:00 +0000"),
	item("v0.46.0", "Tue, 01 Dec 2026 10:00:00 +0100"),
	item("v0.45.0", "Mon, 23 Nov 2026 10:00:00 +0000"),
)

func TestRefreshChecksDailyAndCaches(t *testing.T) {
	var status atomic.Int32
	var body atomic.Value
	status.Store(http.StatusOK)
	body.Store(v046)
	srv, hits := forge(t, &status, &body)
	now := time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC)
	c := &Checker{URL: srv.URL, Now: func() time.Time { return now }}
	ctx := context.Background()

	_, ok := c.Latest()
	require.False(t, ok, "nothing before the first check")

	c.Refresh(ctx)
	got, ok := c.Latest()
	require.True(t, ok)
	require.Equal(t, Release{Version: "0.46.0", Published: time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC),
		URL: "https://sarak.sh/mikluko/sniffarr/releases/tag/v0.46.0"}, got, "the pre-release above it is skipped")
	require.EqualValues(t, 1, hits.Load())

	now = now.Add(23 * time.Hour)
	c.Refresh(ctx)
	require.EqualValues(t, 1, hits.Load(), "within a day the cached answer stands")

	body.Store(rss(item("v0.47.0", "Wed, 02 Dec 2026 10:00:00 +0000"), item("v0.46.0", "Tue, 01 Dec 2026 10:00:00 +0100")))
	now = now.Add(time.Hour)
	c.Refresh(ctx)
	require.EqualValues(t, 2, hits.Load(), "a day on it checks again")
	got, _ = c.Latest()
	require.Equal(t, "0.47.0", got.Version)
}

func TestRefreshFailureKeepsNothingAndRetriesSooner(t *testing.T) {
	var status atomic.Int32
	var body atomic.Value
	status.Store(http.StatusOK)
	body.Store(v046)
	srv, hits := forge(t, &status, &body)
	now := time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC)
	c := &Checker{URL: srv.URL, Now: func() time.Time { return now }}
	ctx := context.Background()
	c.Refresh(ctx)

	status.Store(http.StatusBadGateway)
	now = now.Add(DefaultInterval)
	c.Refresh(ctx)
	_, ok := c.Latest()
	require.False(t, ok, "an error shows nothing")

	now = now.Add(DefaultRetry - time.Minute)
	c.Refresh(ctx)
	require.EqualValues(t, 2, hits.Load())
	status.Store(http.StatusOK)
	now = now.Add(time.Minute)
	c.Refresh(ctx)
	require.EqualValues(t, 3, hits.Load(), "a failed check is retried after Retry")
	_, ok = c.Latest()
	require.True(t, ok)
}

func TestRefreshWithoutStableRelease(t *testing.T) {
	tests := map[string]struct {
		status int
		body   string
	}{
		"no feed":           {http.StatusNotFound, `not found`},
		"no item":           {http.StatusOK, rss()},
		"pre-releases only": {http.StatusOK, rss(item("v0.47.0-rc.1", "Wed, 02 Dec 2026 09:00:00 +0000"))},
		"not a version":     {http.StatusOK, rss(`<item><title>notes</title><link>https://example.com/notes</link></item>`)},
		"not xml":           {http.StatusOK, `{"tag_name":"v0.47.0"}`},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var status atomic.Int32
			var body atomic.Value
			status.Store(int32(tt.status))
			body.Store(tt.body)
			srv, _ := forge(t, &status, &body)
			c := &Checker{URL: srv.URL}
			c.Refresh(context.Background())
			_, ok := c.Latest()
			require.False(t, ok)
		})
	}
}

func TestRefreshWithoutNetwork(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c := &Checker{URL: url}
	c.Refresh(context.Background())
	_, ok := c.Latest()
	require.False(t, ok)
}

func TestRunChecksAtStartAndStops(t *testing.T) {
	var status atomic.Int32
	var body atomic.Value
	status.Store(http.StatusOK)
	body.Store(v046)
	srv, hits := forge(t, &status, &body)
	c := &Checker{URL: srv.URL}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- c.Run(ctx) }()
	require.Eventually(t, func() bool { _, ok := c.Latest(); return ok }, time.Second, 5*time.Millisecond)
	cancel()
	require.NoError(t, <-done)
	require.EqualValues(t, 1, hits.Load())
}

// The highest stable version wins, whatever the feed's order; a GitHub Atom
// feed reads the same, its tag taken from the entry's link.
func TestLatestStableFromFeeds(t *testing.T) {
	var f feed
	require.NoError(t, xml.Unmarshal([]byte(rss(
		item("v0.42.5", "Wed, 02 Dec 2026 09:00:00 +0000"),
		item("v0.43.0", "Sat, 07 Nov 2026 10:59:51 +0000"),
	)), &f))
	got, ok := latestStable(f.releases())
	require.True(t, ok)
	require.Equal(t, "0.43.0", got.Version, "a backported patch does not outrank a later minor")
	require.Equal(t, time.Date(2026, 11, 7, 10, 59, 51, 0, time.UTC), got.Published)

	atom := `<?xml version="1.0" encoding="UTF-8"?><feed xmlns="http://www.w3.org/2005/Atom"><title>Release notes from sniffarr</title>
<entry><title>0.44.0-beta.1</title><link rel="alternate" type="text/html" href="https://github.com/mikluko/sniffarr/releases/tag/v0.44.0-beta.1"/><updated>2026-11-07T20:26:27Z</updated></entry>
<entry><title>Release 0.43</title><link rel="alternate" type="text/html" href="https://github.com/mikluko/sniffarr/releases/tag/v0.43.0"/><updated>2026-11-07T10:59:51Z</updated></entry></feed>`
	f = feed{}
	require.NoError(t, xml.Unmarshal([]byte(atom), &f))
	got, ok = latestStable(f.releases())
	require.True(t, ok)
	require.Equal(t, Release{Version: "0.43.0", Published: time.Date(2026, 11, 7, 10, 59, 51, 0, time.UTC),
		URL: "https://github.com/mikluko/sniffarr/releases/tag/v0.43.0"}, got)
}
