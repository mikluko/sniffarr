package main

import (
	"io"
	"log/slog"
	"net/url"
	"testing"
	"time"
	"unicode"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/app/api"
	"github.com/mikluko/sniffarr/internal/app/client"
	"github.com/mikluko/sniffarr/internal/app/db"
	"github.com/mikluko/sniffarr/internal/app/dht"
	"github.com/mikluko/sniffarr/internal/app/fetch"
	"github.com/mikluko/sniffarr/internal/app/sniff"
	"github.com/mikluko/sniffarr/internal/app/ui"
	"github.com/mikluko/sniffarr/internal/audio"
	"github.com/mikluko/sniffarr/internal/classify"
	"github.com/mikluko/sniffarr/internal/ingest"
	"github.com/mikluko/sniffarr/internal/rules"
	"github.com/mikluko/sniffarr/internal/search"
	sniffer "github.com/mikluko/sniffarr/internal/sniff"
	"github.com/mikluko/sniffarr/internal/store"
	"github.com/mikluko/sniffarr/internal/stun"
	"github.com/mikluko/sniffarr/internal/tmdb"
)

// env is a lookup over kv pairs.
func env(kv ...string) func(string) (string, bool) {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

// defaultNetwork is the DHT node's configuration from the flags' defaults,
// passive unless -sniff is on.
func defaultNetwork(passive bool) sniffer.NetworkConfig {
	return sniffer.NetworkConfig{
		Port:         sniffer.DefaultPort,
		Passive:      passive,
		Rate:         sniffer.DefaultRate,
		Fetchers:     sniffer.DefaultFetchers,
		PeerWait:     sniffer.DefaultPeerWait,
		MinPeers:     sniffer.DefaultMinPeers,
		FetchTimeout: sniffer.DefaultFetchTimeout,
	}
}

func TestLoadComponents(t *testing.T) {
	apiEnv := []string{"SNIFFARR_DB", "/data/sniffarr.db", "SNIFFARR_INGEST_TOKEN", "in", "SNIFFARR_SEARCH_TOKEN", "se"}
	russian, err := audio.Parse("ru")
	require.NoError(t, err)
	tests := []struct {
		name string
		args []string
		env  []string
		want func(*testing.T, config)
		err  string
	}{
		{name: "nothing on", err: "-api, -ui, -sniff and -fetch are all off: nothing to do"},
		{name: "api alone", args: []string{"-api"}, env: apiEnv, want: func(t *testing.T, c config) {
			require.Equal(t, components{api: true}, c.on)
			require.Equal(t, "/data/sniffarr.db", c.db.Path)
			require.Equal(t, ":8080", c.api.Addr)
			require.Equal(t, client.Config{IngestToken: "in", SearchToken: "se"}, c.client, "clients would reach this process")
			require.Equal(t, ui.Config{}, c.ui, "the UI's variables are not read")
			require.Equal(t, slog.LevelInfo, c.logLevel)
		}},
		{name: "api and ui in one process", args: []string{"-api", "-ui"}, env: apiEnv, want: func(t *testing.T, c config) {
			require.Equal(t, components{api: true, ui: true}, c.on)
			require.Nil(t, c.client.URL, "memconn")
			require.Equal(t, ui.Config{Addr: defaultUIAddr, Languages: russian}, c.ui)
		}},
		{name: "api with a URL", args: []string{"-api", "-ui"}, env: append(apiEnv, "SNIFFARR_URL", "http://api:8080"), err: "SNIFFARR_URL is set, but -api serves the API in this process: unset it"},
		{name: "ui alone", args: []string{"-ui"}, env: []string{"SNIFFARR_URL", "http://api:8080", "SNIFFARR_SEARCH_TOKEN", "se", "SNIFFARR_UI_ADDR", ":9001", "SNIFFARR_OIDC_ISSUER", "https://id.example.com", "SNIFFARR_AUDIO_LANGUAGES", ""},
			want: func(t *testing.T, c config) {
				require.Equal(t, client.Config{URL: &url.URL{Scheme: "http", Host: "api:8080"}, SearchToken: "se"}, c.client)
				require.Equal(t, ui.Config{Addr: ":9001", OIDCIssuer: "https://id.example.com"}, c.ui)
				require.Equal(t, api.Config{}, c.api, "the API's variables are not read")
				require.Equal(t, db.Config{}, c.db)
			}},
		{name: "ui without a URL", args: []string{"-ui"}, env: []string{"SNIFFARR_SEARCH_TOKEN", "se"}, err: "SNIFFARR_URL is not set"},
		{name: "ui without a token", args: []string{"-ui"}, env: []string{"SNIFFARR_URL", "http://api:8080"}, err: "SNIFFARR_SEARCH_TOKEN is not set"},
		{name: "sniff and fetch", args: []string{"-sniff", "-fetch"}, env: []string{"SNIFFARR_URL", "https://api.example.com", "SNIFFARR_INGEST_TOKEN", "in"}, want: func(t *testing.T, c config) {
			require.Equal(t, components{sniff: true, fetch: true}, c.on)
			require.Equal(t, dht.Config{Network: defaultNetwork(false), STUN: stun.DefaultServer}, c.dht, "a discovering node is not passive")
			require.Equal(t, sniff.Config{Walkers: sniffer.DefaultWalkers}, c.sniff)
			require.Equal(t, fetch.Config{Workers: sniffer.DefaultFetchers}, c.fetch)
			require.Equal(t, client.Config{URL: &url.URL{Scheme: "https", Host: "api.example.com"}, IngestToken: "in"}, c.client)
		}},
		{name: "fetch alone", args: []string{"-fetch", "-port", "0", "-fetchers", "100", "-upnp"}, env: []string{"SNIFFARR_URL", "http://api:8080", "SNIFFARR_INGEST_TOKEN", "in"}, want: func(t *testing.T, c config) {
			want := defaultNetwork(true)
			want.Port, want.Fetchers, want.UPnP = 0, 100, true
			require.Equal(t, dht.Config{Network: want, STUN: stun.DefaultServer}, c.dht, "a node that does not discover is passive")
			require.Equal(t, fetch.Config{Workers: 100}, c.fetch)
		}},
		{name: "sniff with another STUN server", args: []string{"-sniff", "-stun", "stun.example.net:3478"}, env: []string{"SNIFFARR_URL", "http://api:8080", "SNIFFARR_INGEST_TOKEN", "in"}, want: func(t *testing.T, c config) {
			require.Equal(t, dht.Config{Network: defaultNetwork(false), STUN: "stun.example.net:3478"}, c.dht)
		}},
		{name: "sniff without STUN", args: []string{"-sniff", "-stun", ""}, env: []string{"SNIFFARR_URL", "http://api:8080", "SNIFFARR_INGEST_TOKEN", "in"}, want: func(t *testing.T, c config) {
			require.Equal(t, dht.Config{Network: defaultNetwork(false)}, c.dht, "an empty -stun asks no server")
		}},
		{name: "sniff without a URL", args: []string{"-sniff"}, env: []string{"SNIFFARR_INGEST_TOKEN", "in"}, err: "SNIFFARR_URL is not set"},
		{name: "fetch without a token", args: []string{"-fetch"}, env: []string{"SNIFFARR_URL", "http://api:8080"}, err: "SNIFFARR_INGEST_TOKEN is not set"},
		{name: "bad scheme", args: []string{"-fetch"}, env: []string{"SNIFFARR_URL", "api:8080", "SNIFFARR_INGEST_TOKEN", "in"}, err: "SNIFFARR_URL: want an http or https URL"},
		{name: "everything in one process", args: []string{"-api", "-ui", "-sniff", "-fetch"}, env: apiEnv, want: func(t *testing.T, c config) {
			require.Equal(t, components{api: true, ui: true, sniff: true, fetch: true}, c.on)
			require.Nil(t, c.client.URL)
			require.False(t, c.dht.Network.Passive)
		}},
		{name: "level from the flag", args: []string{"-api", "-log-level", "warn"}, env: append(apiEnv, "SNIFFARR_LOG_LEVEL", "debug"), want: func(t *testing.T, c config) {
			require.Equal(t, slog.LevelWarn, c.logLevel, "the flag wins")
		}},
		{name: "level from the environment", args: []string{"-api"}, env: append(apiEnv, "SNIFFARR_LOG_LEVEL", "debug"), want: func(t *testing.T, c config) {
			require.Equal(t, slog.LevelDebug, c.logLevel)
		}},
		{name: "unknown level in the environment", args: []string{"-api"}, env: append(apiEnv, "SNIFFARR_LOG_LEVEL", "loud"), err: "SNIFFARR_LOG_LEVEL: want debug, info, warn or error"},
		{name: "unknown level flag", args: []string{"-api", "-log-level", "loud"}, env: apiEnv, err: `invalid value "loud" for flag -log-level`},
		{name: "unknown flag", args: []string{"-discover"}, err: "flag provided but not defined: -discover"},
		{name: "not a number", args: []string{"-fetch", "-fetchers", "many"}, err: `invalid value "many" for flag -fetchers`},
		{name: "zero rate", args: []string{"-fetch", "-rate", "0"}, err: "-fetchers, -rate, -walkers and -min-peers: want positive integers"},
		{name: "zero peers", args: []string{"-fetch", "-min-peers", "0"}, err: "-fetchers, -rate, -walkers and -min-peers: want positive integers"},
		{name: "port out of range", args: []string{"-sniff", "-port", "70000"}, err: "-port: want 0 to 65535"},
		{name: "negative wait", args: []string{"-fetch", "-peer-wait", "-1s"}, err: "-fetch-timeout and -peer-wait: want positive durations"},
		{name: "unknown audio language", args: []string{"-ui"}, env: []string{"SNIFFARR_URL", "http://api:8080", "SNIFFARR_SEARCH_TOKEN", "se", "SNIFFARR_AUDIO_LANGUAGES", "ru,xx"}, err: `SNIFFARR_AUDIO_LANGUAGES: unknown audio language "xx"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := load(tt.args, env(tt.env...), io.Discard)
			if tt.err != "" {
				require.ErrorContains(t, err, tt.err)
				return
			}
			require.NoError(t, err)
			tt.want(t, c)
		})
	}
}

func TestLoadAPI(t *testing.T) {
	defaultScripts := rules.Scripts{unicode.Latin, unicode.Cyrillic}
	russian, err := audio.Parse("ru")
	require.NoError(t, err)
	required := []string{"SNIFFARR_DB", "/data/sniffarr.db", "SNIFFARR_INGEST_TOKEN", "in", "SNIFFARR_SEARCH_TOKEN", "se"}
	with := func(kv ...string) []string { return append(append([]string{}, required...), kv...) }
	defaults := func() (api.Config, db.Config) {
		return api.Config{Addr: ":8080", IngestToken: "in", SearchToken: "se", TMDBKey: tmdb.DefaultKey, ClassifyConcurrency: 4,
				DailyTokens: classify.DefaultDailyTokens, SearchReserve: classify.DefaultSearchReserve, LazyMax: search.DefaultLazyMax, LazyWait: search.DefaultLazyWait,
				LazyTMDB: search.DefaultLazyTMDB, TMDBConcurrency: tmdb.DefaultConcurrency, TMDBRate: tmdb.DefaultRate, TMDBRetry: tmdb.DefaultRetry,
				Lease: ingest.DefaultLease, Attempts: ingest.DefaultAttempts, Backoff: ingest.DefaultBackoff,
				Retention: defaultRetention, Scripts: defaultScripts, Languages: russian},
			db.Config{Path: "/data/sniffarr.db",
				Queue: store.QueueOptions{MaxSize: store.DefaultMaxSize, MaxAge: store.DefaultMaxAge, DeadFor: store.DefaultDeadFor, NoPeersFor: store.DefaultNoPeersFor},
				Store: store.Options{MaxFiles: store.DefaultMaxFiles, Languages: []string{"ru"}, TMDB: true}}
	}
	tests := []struct {
		name string
		env  []string
		want func(*api.Config, *db.Config)
		err  string
	}{
		{name: "defaults", env: required, want: func(*api.Config, *db.Config) {}},
		{name: "tmdb off", env: with("TMDB_API_KEY", ""), want: func(a *api.Config, d *db.Config) { a.TMDBKey, d.Store.TMDB = "", false }},
		{name: "everything set", env: with("SNIFFARR_ADDR", ":9000", "TYPESAFE_API_KEY", "ts", "SNIFFARR_CLASSIFY_CONCURRENCY", "8", "SNIFFARR_CLASSIFY_DAILY_TOKENS", "1000000",
			"SNIFFARR_CLASSIFY_SEARCH_RESERVE", "40",
			"SNIFFARR_FETCH_LEASE", "2m", "SNIFFARR_FETCH_ATTEMPTS", "5", "SNIFFARR_FETCH_BACKOFF", "1h",
			"SNIFFARR_QUEUE_MAX_SIZE", "5000", "SNIFFARR_QUEUE_MAX_AGE", "6h", "SNIFFARR_QUEUE_DEAD_FOR", "48h", "SNIFFARR_QUEUE_NO_PEERS_FOR", "12h",
			"SNIFFARR_STORE_MAX_FILES", "50", "SNIFFARR_RETENTION", "720h",
			"SNIFFARR_SEARCH_CLASSIFY_MAX", "5", "SNIFFARR_SEARCH_CLASSIFY_WAIT", "500ms",
			"SNIFFARR_SCRIPTS", "Latin,Cyrillic,Greek", "SNIFFARR_AUDIO_LANGUAGES", "ru",
			"TMDB_API_KEY", "tm", "SNIFFARR_TORZNAB_TOKEN", "tz", "SNIFFARR_TMDB_CONCURRENCY", "3", "SNIFFARR_TMDB_RATE", "10", "SNIFFARR_TMDB_RETRY", "240h", "SNIFFARR_SEARCH_TMDB_MAX", "7"),
			want: func(a *api.Config, d *db.Config) {
				*a = api.Config{Addr: ":9000", IngestToken: "in", SearchToken: "se", TypesafeKey: "ts", TMDBKey: "tm", TorznabToken: "tz", ClassifyConcurrency: 8,
					TMDBConcurrency: 3, TMDBRate: 10, TMDBRetry: 240 * time.Hour, LazyTMDB: 7, DailyTokens: 1000000, SearchReserve: 40,
					Lease: 2 * time.Minute, Attempts: 5, Backoff: time.Hour, Retention: 720 * time.Hour, LazyMax: 5, LazyWait: 500 * time.Millisecond,
					Scripts: rules.Scripts{unicode.Latin, unicode.Cyrillic, unicode.Greek}, Languages: russian}
				d.Queue = store.QueueOptions{MaxSize: 5000, MaxAge: 6 * time.Hour, DeadFor: 48 * time.Hour, NoPeersFor: 12 * time.Hour}
				d.Store = store.Options{MaxFiles: 50, Languages: []string{"ru"}, TMDB: true}
			}},
		{name: "unlimited budget", env: with("SNIFFARR_CLASSIFY_DAILY_TOKENS", "0"), want: func(a *api.Config, _ *db.Config) { a.DailyTokens = 0 }},
		{name: "no reserve", env: with("SNIFFARR_CLASSIFY_SEARCH_RESERVE", "0"), want: func(a *api.Config, _ *db.Config) { a.SearchReserve = 0 }},
		{name: "no audio languages", env: with("SNIFFARR_AUDIO_LANGUAGES", ""), want: func(a *api.Config, d *db.Config) { a.Languages, d.Store.Languages = nil, nil }},
		{name: "queue size not a number", env: with("SNIFFARR_QUEUE_MAX_SIZE", "1M"), err: "SNIFFARR_QUEUE_MAX_SIZE: want a positive integer"},
		{name: "nothing set", env: nil, err: "SNIFFARR_DB is not set"},
		{name: "no ingest token", env: with("SNIFFARR_INGEST_TOKEN", ""), err: "SNIFFARR_INGEST_TOKEN is not set"},
		{name: "no search token", env: with("SNIFFARR_SEARCH_TOKEN", ""), err: "SNIFFARR_SEARCH_TOKEN is not set"},
		{name: "concurrency not a number", env: with("SNIFFARR_CLASSIFY_CONCURRENCY", "four"), err: "SNIFFARR_CLASSIFY_CONCURRENCY: want a positive integer"},
		{name: "zero concurrency", env: with("SNIFFARR_CLASSIFY_CONCURRENCY", "0"), err: "SNIFFARR_CLASSIFY_CONCURRENCY: want a positive integer"},
		{name: "negative budget", env: with("SNIFFARR_CLASSIFY_DAILY_TOKENS", "-1"), err: "SNIFFARR_CLASSIFY_DAILY_TOKENS: want a non-negative integer"},
		{name: "budget not a number", env: with("SNIFFARR_CLASSIFY_DAILY_TOKENS", "1e6"), err: "SNIFFARR_CLASSIFY_DAILY_TOKENS: want a non-negative integer"},
		{name: "lease not a duration", env: with("SNIFFARR_FETCH_LEASE", "60"), err: "SNIFFARR_FETCH_LEASE: want a positive duration"},
		{name: "zero attempts", env: with("SNIFFARR_FETCH_ATTEMPTS", "0"), err: "SNIFFARR_FETCH_ATTEMPTS: want a positive integer"},
		{name: "negative backoff", env: with("SNIFFARR_FETCH_BACKOFF", "-1m"), err: "SNIFFARR_FETCH_BACKOFF: want a positive duration"},
		{name: "no-peers window not a duration", env: with("SNIFFARR_QUEUE_NO_PEERS_FOR", "1d"), err: "SNIFFARR_QUEUE_NO_PEERS_FOR: want a positive duration"},
		{name: "reserve over 100", env: with("SNIFFARR_CLASSIFY_SEARCH_RESERVE", "101"), err: "SNIFFARR_CLASSIFY_SEARCH_RESERVE: want an integer from 0 to 100"},
		{name: "reserve a fraction", env: with("SNIFFARR_CLASSIFY_SEARCH_RESERVE", "0.25"), err: "SNIFFARR_CLASSIFY_SEARCH_RESERVE: want an integer from 0 to 100"},
		{name: "unknown script", env: with("SNIFFARR_SCRIPTS", "Latin,Elvish"), err: `SNIFFARR_SCRIPTS: unknown script "Elvish"`},
		{name: "zero tmdb rate", env: with("SNIFFARR_TMDB_RATE", "0"), err: "SNIFFARR_TMDB_RATE: want a positive integer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := load([]string{"-api"}, env(tt.env...), io.Discard)
			if tt.err != "" {
				require.EqualError(t, err, tt.err)
				return
			}
			require.NoError(t, err)
			a, d := defaults()
			tt.want(&a, &d)
			require.Equal(t, a, c.api)
			require.Equal(t, d, c.db)
		})
	}
}
