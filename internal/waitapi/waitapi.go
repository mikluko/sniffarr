// Package waitapi waits for the sniffarr API to serve a given release.
package waitapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// Interval is how often Run asks the API.
const Interval = 2 * time.Second

// Run asks base's /healthz every interval until an answer, of any status,
// carries the Server header want, and returns nil then. It logs what it saw
// each time that changes. It has no deadline of its own and returns ctx's
// error when ctx ends first.
func Run(ctx context.Context, c *http.Client, base *url.URL, want string, interval time.Duration, log *slog.Logger) error {
	target := base.JoinPath("healthz").String()
	t := time.NewTicker(interval)
	defer t.Stop()
	var last string
	for {
		got, err := server(ctx, c, target)
		if err == nil && got == want {
			log.Info("api ready", "url", target, "server", got)
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		seen := "server " + got
		if err != nil {
			seen = "err " + err.Error()
		}
		if seen != last {
			last = seen
			if err != nil {
				log.Info("api unreachable", "url", target, "err", err)
			} else {
				log.Info("api version mismatch", "url", target, "server", got, "want", want)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// server returns the Server header of a GET of target.
func server(ctx context.Context, c *http.Client, target string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Header.Get("Server"), nil
}
