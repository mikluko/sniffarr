package latest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// forge serves body with status at the latest-release endpoint, counting
// the requests.
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

const v046 = `{"tag_name":"v0.46.0","published_at":"2026-12-01T10:00:00+01:00","html_url":"https://sarak.sh/mikluko/sniffarr/releases/tag/v0.46.0","prerelease":false,"draft":false}`

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
		URL: "https://sarak.sh/mikluko/sniffarr/releases/tag/v0.46.0"}, got)
	require.EqualValues(t, 1, hits.Load())

	now = now.Add(23 * time.Hour)
	c.Refresh(ctx)
	require.EqualValues(t, 1, hits.Load(), "within a day the cached answer stands")

	body.Store(`{"tag_name":"v0.47.0","published_at":"2026-12-02T10:00:00Z","html_url":"u"}`)
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
		"no release":  {http.StatusNotFound, `{"message":"not found"}`},
		"pre-release": {http.StatusOK, `{"tag_name":"v0.47.0-rc.1","prerelease":true}`},
		"draft":       {http.StatusOK, `{"tag_name":"v0.47.0","draft":true}`},
		"not json":    {http.StatusOK, `<html>`},
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
