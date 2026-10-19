package main

import (
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/sniffer"
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
			require.Equal(t, sniffer.NetworkConfig{
				Port:         sniffer.DefaultPort,
				Rate:         sniffer.DefaultRate,
				Fetchers:     sniffer.DefaultFetchers,
				PeerWait:     sniffer.DefaultPeerWait,
				FetchTimeout: sniffer.DefaultFetchTimeout,
			}, c.network)
			require.Equal(t, sniffer.DefaultWalkers, c.walkers)
		}},
		{name: "flags", env: env, args: []string{"-port", "6881", "-fetchers", "100", "-fetch-timeout", "30s"}, want: func(t *testing.T, c config) {
			require.Equal(t, 6881, c.network.Port)
			require.Equal(t, 100, c.network.Fetchers)
			require.Equal(t, 30*time.Second, c.network.FetchTimeout)
		}},
		{name: "no URL", env: map[string]string{"SNIFFARR_INGEST_TOKEN": "secret"}, err: "SNIFFARR_URL is not set"},
		{name: "no token", env: map[string]string{"SNIFFARR_URL": "http://sniffapi:8080"}, err: "SNIFFARR_INGEST_TOKEN is not set"},
		{name: "nothing set", env: map[string]string{}, err: "SNIFFARR_URL is not set"},
		{name: "bad scheme", env: map[string]string{"SNIFFARR_URL": "sniffapi:8080", "SNIFFARR_INGEST_TOKEN": "secret"}, err: "SNIFFARR_URL: want an http or https URL"},
		{name: "not a number", env: env, args: []string{"-fetchers", "many"}, err: `invalid value "many" for flag -fetchers`},
		{name: "zero rate", env: env, args: []string{"-rate", "0"}, err: "-fetchers, -rate and -walkers: want positive integers"},
		{name: "port out of range", env: env, args: []string{"-port", "70000"}, err: "-port: want 1 to 65535"},
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
