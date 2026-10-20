package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/auth"
)

func TestGraph(t *testing.T) {
	apiEnv := []string{"SNIFFARR_DB", "/data/sniffarr.db", "SNIFFARR_INGEST_TOKEN", "in", "SNIFFARR_SEARCH_TOKEN", "se"}
	remote := []string{"SNIFFARR_URL", "http://api:8080", "SNIFFARR_INGEST_TOKEN", "in", "SNIFFARR_SEARCH_TOKEN", "se"}
	tests := []struct {
		name string
		args []string
		env  []string
	}{
		{name: "api", args: []string{"-api"}, env: apiEnv},
		{name: "api and ui", args: []string{"-api", "-ui"}, env: apiEnv},
		{name: "api, ui, sniff and fetch", args: []string{"-api", "-ui", "-sniff", "-fetch"}, env: apiEnv},
		{name: "ui", args: []string{"-ui"}, env: remote},
		{name: "sniff", args: []string{"-sniff"}, env: remote},
		{name: "fetch", args: []string{"-fetch"}, env: remote},
		{name: "sniff and fetch", args: []string{"-sniff", "-fetch"}, env: remote},
		{name: "ui and fetch", args: []string{"-ui", "-fetch"}, env: remote},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := load(tt.args, env(tt.env...), io.Discard)
			require.NoError(t, err)
			require.NoError(t, fx.ValidateApp(options(cfg, slog.New(slog.DiscardHandler))...))
		})
	}
}

// freeAddr is a loopback address nothing listens on.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	require.NoError(t, l.Close())
	return l.Addr().String()
}

// recorder keeps the attributes of every "fetched" record.
type recorder struct {
	slog.Handler
	mu      sync.Mutex
	fetched []map[string]any
}

func (r *recorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *recorder) Handle(_ context.Context, rec slog.Record) error {
	if rec.Message != "fetched" {
		return nil
	}
	attrs := map[string]any{}
	rec.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value.Any(); return true })
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fetched = append(r.fetched, attrs)
	return nil
}

func (r *recorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *recorder) WithGroup(string) slog.Handler      { return r }

func (r *recorder) outcomes() []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []any
	for _, f := range r.fetched {
		out = append(out, f["outcome"])
	}
	return out
}

func TestRunWithTelemetryOff(t *testing.T) {
	t.Setenv("OTEL_SDK_DISABLED", "true")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	addr, uiAddr := freeAddr(t), freeAddr(t)
	cfg, err := load([]string{"-api", "-ui", "-fetch", "-port", "0", "-fetchers", "2", "-peer-wait", "200ms"}, env(
		"SNIFFARR_DB", filepath.Join(t.TempDir(), "sniffarr.db"), "SNIFFARR_ADDR", addr, "SNIFFARR_UI_ADDR", uiAddr,
		"SNIFFARR_INGEST_TOKEN", "in", "SNIFFARR_SEARCH_TOKEN", "se",
	), io.Discard)
	require.NoError(t, err)
	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, slog.New(rec)) }()

	ok := func(url string) func() bool {
		return func() bool {
			res, err := http.Get(url)
			if err != nil {
				return false
			}
			res.Body.Close()
			return res.StatusCode == http.StatusOK
		}
	}
	require.Eventually(t, ok("http://"+addr+"/healthz"), 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, ok("http://"+uiAddr+"/data"), 5*time.Second, 10*time.Millisecond, "the UI reads the API in-process")

	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	ingest := sniffarrv1connect.NewIngestServiceClient(&http.Client{Transport: &http.Transport{Protocols: &p}}, "http://"+addr, connect.WithInterceptors(auth.Client("in")))
	sight := ingest.Sight(ctx)
	require.NoError(t, sight.Send(&sniffarrv1.SightRequest{Infohash: []byte("01234567890123456789"), Source: sniffarrv1.Source_SOURCE_ANNOUNCE}))
	res, err := sight.CloseAndReceive()
	require.NoError(t, err)
	require.Equal(t, int64(1), res.Msg.GetNew())
	require.Eventually(t, func() bool { return len(rec.outcomes()) == 1 }, 10*time.Second, 10*time.Millisecond, "the fetcher takes the task over memconn and returns its result")
	require.Equal(t, []any{"no_peers"}, rec.outcomes(), "a passive node with no routing table finds nobody")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(15 * time.Second):
		t.Fatal("run did not return after cancel")
	}
}
