package waitapi

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const want = "sniffarr/0.44.0"

// api answers /healthz with the Server header servers[i] on its i-th
// request, and the last of them from then on.
func api(t *testing.T, servers ...string) (*url.URL, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/healthz", r.URL.Path)
		i := int(hits.Add(1)) - 1
		w.Header().Set("Server", servers[min(i, len(servers)-1)])
		_, _ = w.Write([]byte("ok\n"))
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	return u, &hits
}

// logs returns a logger writing text to the returned buffer.
func logs() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

func TestRunReturnsOnMatch(t *testing.T) {
	u, hits := api(t, want)
	log, _ := logs()
	require.NoError(t, Run(t.Context(), http.DefaultClient, u, want, time.Millisecond, log))
	require.Equal(t, int32(1), hits.Load())
}

func TestRunPollsPastMismatch(t *testing.T) {
	u, hits := api(t, "sniffarr/0.43.0", "sniffarr/0.43.0", "sniffarr/0.43.0", want)
	log, buf := logs()
	require.NoError(t, Run(t.Context(), http.DefaultClient, u, want, time.Millisecond, log))
	require.Equal(t, int32(4), hits.Load())
	require.Equal(t, 1, strings.Count(buf.String(), "api version mismatch"), "an unchanged answer is logged once:\n%s", buf)
	require.Contains(t, buf.String(), "server=sniffarr/0.43.0")
	require.Contains(t, buf.String(), "api ready")
}

func TestRunPollsPastConnectionRefused(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	u := &url.URL{Scheme: "http", Host: l.Addr().String()}
	require.NoError(t, l.Close())

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var tries atomic.Int32
	c := &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		if tries.Add(1) == 3 {
			cancel()
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	log, buf := logs()
	err = Run(ctx, c, u, want, time.Millisecond, log)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, int32(3), tries.Load())
	require.Equal(t, 1, strings.Count(buf.String(), "api unreachable"), "an unchanged error is logged once:\n%s", buf)
}

func TestRunEndsWithContext(t *testing.T) {
	u, _ := api(t, "sniffarr/0.43.0")
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	log, _ := logs()
	err := Run(ctx, http.DefaultClient, u, want, time.Millisecond, log)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
