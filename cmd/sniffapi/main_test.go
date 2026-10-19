package main

import (
	"maps"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/ingest"
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
			lease: ingest.DefaultLease, attempts: ingest.DefaultAttempts, backoff: ingest.DefaultBackoff}},
		{name: "everything set", env: with("SNIFFARR_ADDR", ":9000", "TYPESAFE_API_KEY", "ts", "SNIFFARR_CLASSIFY_CONCURRENCY", "8", "SNIFFARR_CLASSIFY_DAILY_TOKENS", "1000000",
			"SNIFFARR_FETCH_LEASE", "2m", "SNIFFARR_FETCH_ATTEMPTS", "5", "SNIFFARR_FETCH_BACKOFF", "1h"),
			want: config{addr: ":9000", db: "/data/sniffarr.db", ingestToken: "in", searchToken: "se", typesafeKey: "ts", concurrency: 8, dailyTokens: 1000000,
				lease: 2 * time.Minute, attempts: 5, backoff: time.Hour}},
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
