package main

import (
	"context"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/ingest"
	"github.com/mikluko/sniffarr/internal/queue"
	"github.com/mikluko/sniffarr/internal/store"
)

func TestLoad(t *testing.T) {
	required := map[string]string{"SNIFFARR_DB": "/data/sniffarr.db", "SNIFFARR_INGEST_TOKEN": "in", "SNIFFARR_SEARCH_TOKEN": "se"}
	with := func(kv ...string) map[string]string {
		env := maps.Clone(required)
		for i := 0; i < len(kv); i += 2 {
			env[kv[i]] = kv[i+1]
		}
		return env
	}
	tests := []struct {
		name string
		env  map[string]string
		want config
		err  string
	}{
		{name: "defaults", env: required, want: config{addr: ":8080", db: "/data/sniffarr.db", ingestToken: "in", searchToken: "se", concurrency: 4,
			lease: ingest.DefaultLease, attempts: ingest.DefaultAttempts, backoff: ingest.DefaultBackoff,
			queue:     queue.Options{MaxSize: queue.DefaultMaxSize, MaxAge: queue.DefaultMaxAge, DeadFor: queue.DefaultDeadFor},
			store:     store.Options{MaxFiles: store.DefaultMaxFiles},
			retention: defaultRetention}},
		{name: "everything set", env: with("SNIFFARR_ADDR", ":9000", "TYPESAFE_API_KEY", "ts", "SNIFFARR_CLASSIFY_CONCURRENCY", "8", "SNIFFARR_CLASSIFY_DAILY_TOKENS", "1000000",
			"SNIFFARR_FETCH_LEASE", "2m", "SNIFFARR_FETCH_ATTEMPTS", "5", "SNIFFARR_FETCH_BACKOFF", "1h",
			"SNIFFARR_QUEUE_MAX_SIZE", "5000", "SNIFFARR_QUEUE_MAX_AGE", "6h", "SNIFFARR_QUEUE_DEAD_FOR", "48h",
			"SNIFFARR_STORE_MAX_FILES", "50", "SNIFFARR_RETENTION", "720h"),
			want: config{addr: ":9000", db: "/data/sniffarr.db", ingestToken: "in", searchToken: "se", typesafeKey: "ts", concurrency: 8, dailyTokens: 1000000,
				lease: 2 * time.Minute, attempts: 5, backoff: time.Hour,
				queue:     queue.Options{MaxSize: 5000, MaxAge: 6 * time.Hour, DeadFor: 48 * time.Hour},
				store:     store.Options{MaxFiles: 50},
				retention: 720 * time.Hour}},
		{name: "queue size not a number", env: with("SNIFFARR_QUEUE_MAX_SIZE", "1M"), err: "SNIFFARR_QUEUE_MAX_SIZE: want a positive integer"},
		{name: "nothing set", env: map[string]string{}, err: "SNIFFARR_DB is not set"},
		{name: "no ingest token", env: with("SNIFFARR_INGEST_TOKEN", ""), err: "SNIFFARR_INGEST_TOKEN is not set"},
		{name: "no search token", env: with("SNIFFARR_SEARCH_TOKEN", ""), err: "SNIFFARR_SEARCH_TOKEN is not set"},
		{name: "concurrency not a number", env: with("SNIFFARR_CLASSIFY_CONCURRENCY", "four"), err: "SNIFFARR_CLASSIFY_CONCURRENCY: want a positive integer"},
		{name: "zero concurrency", env: with("SNIFFARR_CLASSIFY_CONCURRENCY", "0"), err: "SNIFFARR_CLASSIFY_CONCURRENCY: want a positive integer"},
		{name: "negative budget", env: with("SNIFFARR_CLASSIFY_DAILY_TOKENS", "-1"), err: "SNIFFARR_CLASSIFY_DAILY_TOKENS: want a non-negative integer"},
		{name: "lease not a duration", env: with("SNIFFARR_FETCH_LEASE", "60"), err: "SNIFFARR_FETCH_LEASE: want a positive duration"},
		{name: "zero attempts", env: with("SNIFFARR_FETCH_ATTEMPTS", "0"), err: "SNIFFARR_FETCH_ATTEMPTS: want a positive integer"},
		{name: "negative backoff", env: with("SNIFFARR_FETCH_BACKOFF", "-1m"), err: "SNIFFARR_FETCH_BACKOFF: want a positive duration"},
		{name: "budget not a number", env: with("SNIFFARR_CLASSIFY_DAILY_TOKENS", "1e6"), err: "SNIFFARR_CLASSIFY_DAILY_TOKENS: want a non-negative integer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := load(func(k string) string { return tt.env[k] })
			if tt.err != "" {
				require.EqualError(t, err, tt.err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, c)
		})
	}
}

func TestRunWithTelemetryOff(t *testing.T) {
	t.Setenv("OTEL_SDK_DISABLED", "true")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())

	cfg, err := load(func(k string) string {
		return map[string]string{
			"SNIFFARR_DB": filepath.Join(t.TempDir(), "sniffarr.db"), "SNIFFARR_ADDR": addr,
			"SNIFFARR_INGEST_TOKEN": "in", "SNIFFARR_SEARCH_TOKEN": "se",
		}[k]
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, slog.New(slog.DiscardHandler)) }()

	require.Eventually(t, func() bool {
		res, err := http.Get("http://" + addr + "/healthz")
		if err != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == http.StatusOK
	}, 5*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after cancel")
	}
}
