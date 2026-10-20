package main

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/sniff"
)

func TestLoad(t *testing.T) {
	env := map[string]string{"SNIFFARR_URL": "http://sniffapi:8080", "SNIFFARR_INGEST_TOKEN": "secret"}
	tests := []struct {
		name string
		args []string
		env  map[string]string
		want func(*testing.T, config)
		err  string
	}{
		{name: "defaults", env: env, want: func(t *testing.T, c config) {
			require.Equal(t, "http://sniffapi:8080", c.server.String())
			require.Equal(t, "secret", c.token)
			require.Equal(t, sniff.NetworkConfig{
				Port:         sniff.DefaultPort,
				Rate:         sniff.DefaultRate,
				Fetchers:     sniff.DefaultFetchers,
				PeerWait:     sniff.DefaultPeerWait,
				MinPeers:     sniff.DefaultMinPeers,
				FetchTimeout: sniff.DefaultFetchTimeout,
			}, c.network)
			require.Equal(t, sniff.DefaultWalkers, c.walkers)
			require.Equal(t, slog.LevelInfo, c.logLevel)
			require.True(t, c.discover, "both roles by default")
			require.True(t, c.fetch)
		}},
		{name: "fetcher only", env: env, args: []string{"-discover=false", "-port", "0"}, want: func(t *testing.T, c config) {
			require.False(t, c.discover)
			require.True(t, c.fetch)
			require.True(t, c.network.Passive, "a node that does not discover is passive")
			require.Zero(t, c.network.Port)
		}},
		{name: "discoverer only", env: env, args: []string{"-fetch=false"}, want: func(t *testing.T, c config) {
			require.True(t, c.discover)
			require.False(t, c.fetch)
			require.False(t, c.network.Passive)
		}},
		{name: "no role", env: env, args: []string{"-discover=false", "-fetch=false"}, err: "-discover and -fetch are both off: nothing to do"},
		{name: "flags", env: env, args: []string{"-port", "6881", "-fetchers", "100", "-fetch-timeout", "30s", "-min-peers", "3", "-log-level", "debug"}, want: func(t *testing.T, c config) {
			require.Equal(t, 6881, c.network.Port)
			require.Equal(t, 100, c.network.Fetchers)
			require.Equal(t, 30*time.Second, c.network.FetchTimeout)
			require.Equal(t, 3, c.network.MinPeers)
			require.Equal(t, slog.LevelDebug, c.logLevel)
		}},
		{name: "no URL", env: map[string]string{"SNIFFARR_INGEST_TOKEN": "secret"}, err: "SNIFFARR_URL is not set"},
		{name: "no token", env: map[string]string{"SNIFFARR_URL": "http://sniffapi:8080"}, err: "SNIFFARR_INGEST_TOKEN is not set"},
		{name: "nothing set", env: map[string]string{}, err: "SNIFFARR_URL is not set"},
		{name: "bad scheme", env: map[string]string{"SNIFFARR_URL": "sniffapi:8080", "SNIFFARR_INGEST_TOKEN": "secret"}, err: "SNIFFARR_URL: want an http or https URL"},
		{name: "not a number", env: env, args: []string{"-fetchers", "many"}, err: `invalid value "many" for flag -fetchers`},
		{name: "zero rate", env: env, args: []string{"-rate", "0"}, err: "-fetchers, -rate, -walkers and -min-peers: want positive integers"},
		{name: "unknown level", env: env, args: []string{"-log-level", "loud"}, err: `invalid value "loud" for flag -log-level`},
		{name: "zero peers", env: env, args: []string{"-min-peers", "0"}, err: "-fetchers, -rate, -walkers and -min-peers: want positive integers"},
		{name: "port out of range", env: env, args: []string{"-port", "70000"}, err: "-port: want 0 to 65535"},
		{name: "negative port", env: env, args: []string{"-port", "-1"}, err: "-port: want 0 to 65535"},
		{name: "negative wait", env: env, args: []string{"-peer-wait", "-1s"}, err: "-fetch-timeout and -peer-wait: want positive durations"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := load(tt.args, func(k string) string { return tt.env[k] }, io.Discard)
			if tt.err != "" {
				require.ErrorContains(t, err, tt.err)
				return
			}
			require.NoError(t, err)
			tt.want(t, c)
		})
	}
}
