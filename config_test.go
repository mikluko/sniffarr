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
	"github.com/mikluko/sniffarr/internal/feed/nnmclub"
	"github.com/mikluko/sniffarr/internal/feed/rutor"
	"github.com/mikluko/sniffarr/internal/feed/rutracker"
	"github.com/mikluko/sniffarr/internal/ingest"
	"github.com/mikluko/sniffarr/internal/rules"
	"github.com/mikluko/sniffarr/internal/search"
	sniffer "github.com/mikluko/sniffarr/internal/sniff"
	"github.com/mikluko/sniffarr/internal/store"
	"github.com/mikluko/sniffarr/internal/stun"
	"github.com/mikluko/sniffarr/internal/tmdb"
	"github.com/mikluko/sniffarr/internal/version"
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
	apiEnv := []string{"SNIFFARR_DB", "/data/sniffarr.db"}
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
			require.Equal(t, client.Config{}, c.client, "clients would reach this process")
			require.Equal(t, ui.Config{}, c.ui, "the UI's variables are not read")
			require.Equal(t, slog.LevelInfo, c.logLevel)
		}},
		{name: "api and ui in one process", args: []string{"-api", "-ui"}, env: apiEnv, want: func(t *testing.T, c config) {
			require.Equal(t, components{api: true, ui: true}, c.on)
			require.Nil(t, c.client.URL, "memconn")
			require.Equal(t, ui.Config{Addr: defaultUIAddr}, c.ui)
		}},
		{name: "api with a URL", args: []string{"-api", "-ui"}, env: append(apiEnv, "SNIFFARR_URL", "http://api:8080"), err: "SNIFFARR_URL is set, but -api serves the API in this process: unset it"},
		{name: "no feeds", args: []string{"-api"}, env: apiEnv, want: func(t *testing.T, c config) {
			require.Equal(t, api.Feeds{}, c.api.Feeds, "feeds are off unless named")
		}},
		{name: "feeds", args: []string{"-api"}, env: append(apiEnv, "SNIFFARR_FEEDS", "rutracker, nnmclub,rutor", "SNIFFARR_RUTRACKER_FORUMS", "0, 1457", "SNIFFARR_FEED_INTERVAL", "30m",
			"SNIFFARR_NNMCLUB_FORUMS", "218", "SNIFFARR_RUTOR_DEPTH", "3"),
			want: func(t *testing.T, c config) {
				require.Equal(t, api.Feeds{Sources: []string{"rutracker", "nnmclub", "rutor"}, Interval: 30 * time.Minute,
					Rutracker: rutracker.Config{URL: "https://feed.rutracker.cc", Forums: []int{0, 1457}},
					Nnmclub:   nnmclub.Config{URL: "https://nnmclub.to/forum", Forums: []int{218}},
					Rutor:     rutor.Config{URL: "https://rutor.info", Categories: []int{0}, Depth: 3}}, c.api.Feeds)
			}},
		{name: "feed defaults", args: []string{"-api"}, env: append(apiEnv, "SNIFFARR_FEEDS", "nnmclub"), want: func(t *testing.T, c config) {
			require.Nil(t, c.api.Feeds.Nnmclub.Forums, "every public forum")
			require.Equal(t, 10, c.api.Feeds.Rutor.Depth)
		}},
		{name: "a feed that is not one", args: []string{"-api"}, env: append(apiEnv, "SNIFFARR_FEEDS", "kinozal"), err: `SNIFFARR_FEEDS: "kinozal" is not a feed; rutracker, nnmclub and rutor are`},
		{name: "a forum that is not a number", args: []string{"-api"}, env: append(apiEnv, "SNIFFARR_FEEDS", "rutracker", "SNIFFARR_RUTRACKER_FORUMS", "uhd"),
			err: "SNIFFARR_RUTRACKER_FORUMS: want comma-separated forum numbers, 0 for the whole tracker"},
		{name: "ui alone", args: []string{"-ui"}, env: []string{"SNIFFARR_URL", "http://api:8080", "SNIFFARR_UI_ADDR", ":9001", "SNIFFARR_OIDC_ISSUER", "https://id.example.com", "SNIFFARR_SEERR_URL", "https://seerr.example.com", "SNIFFARR_AUDIO_LANGUAGES", "xx"},
			want: func(t *testing.T, c config) {
				require.Equal(t, client.Config{URL: &url.URL{Scheme: "http", Host: "api:8080"}}, c.client)
				require.Equal(t, ui.Config{Addr: ":9001", OIDCIssuer: "https://id.example.com", SeerrURL: "https://seerr.example.com"}, c.ui)
				require.Equal(t, api.Config{}, c.api, "the API's variables are not read, the unknown audio language among them")
				require.Equal(t, db.Config{}, c.db)
			}},
		{name: "ui with Radarr and Sonarr", args: []string{"-ui"}, env: []string{"SNIFFARR_URL", "http://api:8080", "SNIFFARR_RADARR_URL", "https://radarr.example.com", "SNIFFARR_SONARR_URL", "https://example.com/sonarr/"},
			want: func(t *testing.T, c config) {
				require.Equal(t, ui.Config{Addr: defaultUIAddr, RadarrURL: "https://radarr.example.com", SonarrURL: "https://example.com/sonarr/"}, c.ui, "no API key: the UI only links them")
			}},
		{name: "ui with a Sonarr that is not a URL", args: []string{"-ui"}, env: []string{"SNIFFARR_URL", "http://api:8080", "SNIFFARR_SONARR_URL", "sonarr:8989"},
			err: "SNIFFARR_SONARR_URL: want an http or https URL"},
		{name: "ui without a URL", args: []string{"-ui"}, err: "SNIFFARR_URL is not set"},
		{name: "sniff and fetch", args: []string{"-sniff", "-fetch"}, env: []string{"SNIFFARR_URL", "https://api.example.com"}, want: func(t *testing.T, c config) {
			require.Equal(t, components{sniff: true, fetch: true}, c.on)
			require.Equal(t, dht.Config{Network: defaultNetwork(false), STUN: stun.DefaultServer}, c.dht, "a discovering node is not passive")
			require.Equal(t, sniff.Config{Walkers: sniffer.DefaultWalkers}, c.sniff)
			require.Equal(t, fetch.Config{Workers: sniffer.DefaultFetchers}, c.fetch)
			require.Equal(t, client.Config{URL: &url.URL{Scheme: "https", Host: "api.example.com"}}, c.client)
		}},
		{name: "fetch alone", args: []string{"-fetch", "-port", "0", "-fetchers", "100", "-upnp"}, env: []string{"SNIFFARR_URL", "http://api:8080"}, want: func(t *testing.T, c config) {
			want := defaultNetwork(true)
			want.Port, want.Fetchers, want.UPnP = 0, 100, true
			require.Equal(t, dht.Config{Network: want, STUN: stun.DefaultServer}, c.dht, "a node that does not discover is passive")
			require.Equal(t, fetch.Config{Workers: 100}, c.fetch)
		}},
		{name: "fetch with probes", args: []string{"-fetch", "-probe", "-probe-dir", "/var/lib/sniffarr", "-probers", "2", "-probe-timeout", "5m", "-seed-max-size", "50GiB", "-seed-max-age", "72h"},
			env: []string{"SNIFFARR_URL", "http://api:8080"}, want: func(t *testing.T, c config) {
				require.Equal(t, sniffer.SeederConfig{Dir: "/var/lib/sniffarr", MaxBytes: 50 << 30, MaxAge: 72 * time.Hour}, c.dht.Network.Seed)
				require.False(t, c.dht.TempSeed)
				require.Equal(t, fetch.Config{Workers: sniffer.DefaultFetchers, Probe: true, Probers: 2, ProbeTimeout: 5 * time.Minute}, c.fetch)
			}},
		{name: "probe defaults", args: []string{"-fetch", "-probe"}, env: []string{"SNIFFARR_URL", "http://api:8080"}, want: func(t *testing.T, c config) {
			require.Equal(t, sniffer.SeederConfig{MaxBytes: sniffer.DefaultSeedMaxBytes, MaxAge: sniffer.DefaultSeedMaxAge}, c.dht.Network.Seed)
			require.True(t, c.dht.TempSeed, "no -probe-dir: a temporary directory")
			require.Equal(t, fetch.Config{Workers: sniffer.DefaultFetchers, Probe: true, Probers: sniffer.DefaultProbers, ProbeTimeout: sniffer.DefaultProbeTimeout}, c.fetch)
		}},
		{name: "probes off", args: []string{"-fetch", "-sniff"}, env: []string{"SNIFFARR_URL", "http://api:8080"}, want: func(t *testing.T, c config) {
			require.Equal(t, sniffer.SeederConfig{}, c.dht.Network.Seed)
			require.False(t, c.dht.TempSeed)
			require.False(t, c.fetch.Probe)
		}},
		{name: "probe dir without probe", args: []string{"-fetch", "-probe-dir", "/var/lib/sniffarr"}, env: []string{"SNIFFARR_URL", "http://api:8080"}, err: "-probe-dir: probes are off without -probe"},
		{name: "probers without probe", args: []string{"-fetch", "-probers", "2"}, env: []string{"SNIFFARR_URL", "http://api:8080"}, err: "-probers: probes are off without -probe"},
		{name: "seed age without probe", args: []string{"-fetch", "-seed-max-age", "0s"}, env: []string{"SNIFFARR_URL", "http://api:8080"}, err: "-seed-max-age: probes are off without -probe"},
		{name: "probe dir without fetch", args: []string{"-sniff", "-probe", "-probe-dir", "/var/lib/sniffarr"}, env: []string{"SNIFFARR_URL", "http://api:8080"}, err: "-probe-dir: probes are taken by -fetch, which is off"},
		{name: "probe without fetch", args: []string{"-sniff", "-probe"}, env: []string{"SNIFFARR_URL", "http://api:8080"}, want: func(t *testing.T, c config) {
			require.Equal(t, sniffer.SeederConfig{}, c.dht.Network.Seed, "a node that does not fetch seeds nothing")
			require.False(t, c.dht.TempSeed)
		}},
		{name: "seed size in decimal units", args: []string{"-fetch", "-probe", "-seed-max-size", "10GB"}, err: `invalid value "10GB" for flag -seed-max-size`},
		{name: "zero probers", args: []string{"-fetch", "-probe", "-probers", "0"}, err: "-probers: want a positive integer"},
		{name: "zero seed age", args: []string{"-fetch", "-probe", "-seed-max-age", "0s"}, err: "-probe-timeout and -seed-max-age: want positive durations"},
		{name: "api with probes", args: []string{"-api", "-probe"}, env: apiEnv, want: func(t *testing.T, c config) {
			require.True(t, c.api.Probes)
			require.False(t, c.db.Tasks.NoProbes)
		}},
		{name: "api without probes", args: []string{"-api"}, env: apiEnv, want: func(t *testing.T, c config) {
			require.False(t, c.api.Probes)
			require.True(t, c.db.Tasks.NoProbes, "Trim drops the probes queued")
		}},
		{name: "sniff with another STUN server", args: []string{"-sniff", "-stun", "stun.example.net:3478"}, env: []string{"SNIFFARR_URL", "http://api:8080"}, want: func(t *testing.T, c config) {
			require.Equal(t, dht.Config{Network: defaultNetwork(false), STUN: "stun.example.net:3478"}, c.dht)
		}},
		{name: "sniff without STUN", args: []string{"-sniff", "-stun", ""}, env: []string{"SNIFFARR_URL", "http://api:8080"}, want: func(t *testing.T, c config) {
			require.Equal(t, dht.Config{Network: defaultNetwork(false)}, c.dht, "an empty -stun asks no server")
		}},
		{name: "sniff without a URL", args: []string{"-sniff"}, err: "SNIFFARR_URL is not set"},
		{name: "bad scheme", args: []string{"-fetch"}, env: []string{"SNIFFARR_URL", "api:8080"}, err: "SNIFFARR_URL: want an http or https URL"},
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
		{name: "wait for the API", args: []string{"-wait-api"}, env: []string{"SNIFFARR_URL", "http://api:8080"}, want: func(t *testing.T, c config) {
			require.True(t, c.waitAPI)
			require.Equal(t, components{}, c.on)
			require.Equal(t, client.Config{URL: &url.URL{Scheme: "http", Host: "api:8080"}}, c.client)
		}},
		{name: "wait for the API without a URL", args: []string{"-wait-api"}, err: "SNIFFARR_URL is not set"},
		{name: "wait for the API at a bad URL", args: []string{"-wait-api"}, env: []string{"SNIFFARR_URL", "api:8080"}, err: "SNIFFARR_URL: want an http or https URL"},
		{name: "wait for the API with a component", args: []string{"-wait-api", "-ui"}, env: []string{"SNIFFARR_URL", "http://api:8080"}, err: "-wait-api runs alone: drop -api, -ui, -sniff, -fetch and -probe"},
		{name: "wait for the API with the API", args: []string{"-wait-api", "-api"}, env: apiEnv, err: "-wait-api runs alone"},
		{name: "wait for the API with probes", args: []string{"-wait-api", "-probe"}, env: []string{"SNIFFARR_URL", "http://api:8080"}, err: "-wait-api runs alone"},
		{name: "unknown flag", args: []string{"-discover"}, err: "flag provided but not defined: -discover"},
		{name: "not a number", args: []string{"-fetch", "-fetchers", "many"}, err: `invalid value "many" for flag -fetchers`},
		{name: "zero rate", args: []string{"-fetch", "-rate", "0"}, err: "-fetchers, -rate, -walkers and -min-peers: want positive integers"},
		{name: "zero peers", args: []string{"-fetch", "-min-peers", "0"}, err: "-fetchers, -rate, -walkers and -min-peers: want positive integers"},
		{name: "port out of range", args: []string{"-sniff", "-port", "70000"}, err: "-port: want 0 to 65535"},
		{name: "negative wait", args: []string{"-fetch", "-peer-wait", "-1s"}, err: "-fetch-timeout and -peer-wait: want positive durations"},
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
	defaultProbe := ingest.ProbeConfig{DailyBytes: ingest.DefaultProbeDailyBytes, SearchReserve: ingest.DefaultProbeSearchReserve,
		MaxBytes: ingest.DefaultProbeMaxBytes, Lease: ingest.DefaultProbeLease, Attempts: ingest.DefaultProbeAttempts, Backoff: ingest.DefaultProbeBackoff}
	russian, err := audio.Parse("ru")
	require.NoError(t, err)
	required := []string{"SNIFFARR_DB", "/data/sniffarr.db"}
	with := func(kv ...string) []string { return append(append([]string{}, required...), kv...) }
	defaults := func() (api.Config, db.Config) {
		return api.Config{Addr: ":8080", TMDBKey: tmdb.DefaultKey, ClassifyConcurrency: 4,
				DailyTokens: classify.DefaultDailyTokens, SearchReserve: classify.DefaultSearchReserve, LazyMax: search.DefaultLazyMax, LazyWait: search.DefaultLazyWait,
				LazyTMDB: search.DefaultLazyTMDB, TMDBConcurrency: tmdb.DefaultConcurrency, TMDBRate: tmdb.DefaultRate, TMDBRetry: tmdb.DefaultRetry,
				Lease: ingest.DefaultLease, Attempts: ingest.DefaultAttempts, Backoff: ingest.DefaultBackoff,
				Retention: defaultRetention, RetentionIdle: defaultRetentionIdle, RetentionJournal: defaultRetentionJournal, StatsInterval: defaultStatsInterval, Scripts: defaultScripts,
				Probe: defaultProbe, ReleaseCheck: true},
			db.Config{Path: "/data/sniffarr.db",
				Tasks: store.TaskOptions{MaxSize: store.DefaultMaxSize, MaxAge: store.DefaultMaxAge, DeadFor: store.DefaultDeadFor, NoPeersFor: store.DefaultNoPeersFor, NoProbes: true},
				Store: store.Options{MaxFiles: store.DefaultMaxFiles, ProbeBatch: store.DefaultProbeBatch, TMDB: true}}
	}
	tests := []struct {
		name string
		env  []string
		want func(*api.Config, *db.Config)
		err  string
	}{
		{name: "defaults", env: required, want: func(*api.Config, *db.Config) {}},
		{name: "tmdb key empty", env: with("TMDB_API_KEY", ""), want: func(*api.Config, *db.Config) {}},
		{name: "everything set", env: with("SNIFFARR_ADDR", ":9000", "TYPESAFE_API_KEY", "ts", "SNIFFARR_CLASSIFY_CONCURRENCY", "8", "SNIFFARR_CLASSIFY_DAILY_TOKENS", "1000000",
			"SNIFFARR_CLASSIFY_SEARCH_RESERVE", "40",
			"SNIFFARR_FETCH_LEASE", "2m", "SNIFFARR_FETCH_ATTEMPTS", "5", "SNIFFARR_FETCH_BACKOFF", "1h",
			"SNIFFARR_PROBE_DAILY_BYTES", "20GiB", "SNIFFARR_PROBE_SEARCH_RESERVE", "10", "SNIFFARR_PROBE_MAX_BYTES", "32MiB",
			"SNIFFARR_PROBE_LEASE", "5m", "SNIFFARR_PROBE_ATTEMPTS", "2", "SNIFFARR_PROBE_BACKOFF", "12h", "SNIFFARR_PROBE_BATCH", "50",
			"SNIFFARR_QUEUE_MAX_SIZE", "5000", "SNIFFARR_QUEUE_MAX_AGE", "6h", "SNIFFARR_QUEUE_DEAD_FOR", "48h", "SNIFFARR_QUEUE_NO_PEERS_FOR", "12h",
			"SNIFFARR_STORE_MAX_FILES", "50", "SNIFFARR_RETENTION", "720h", "SNIFFARR_RETENTION_IDLE", "72h", "SNIFFARR_RETENTION_JOURNAL", "168h", "SNIFFARR_STATS_INTERVAL", "1m",
			"SNIFFARR_SEARCH_CLASSIFY_MAX", "5", "SNIFFARR_SEARCH_CLASSIFY_WAIT", "500ms",
			"SNIFFARR_SCRIPTS", "Latin,Cyrillic,Greek", "SNIFFARR_AUDIO_LANGUAGES", "ru",
			"TMDB_API_KEY", "tm", "SNIFFARR_TORZNAB_TOKEN", "tz", "SNIFFARR_TMDB_CONCURRENCY", "3", "SNIFFARR_TMDB_RATE", "10", "SNIFFARR_TMDB_RETRY", "240h", "SNIFFARR_SEARCH_TMDB_MAX", "7"),
			want: func(a *api.Config, d *db.Config) {
				*a = api.Config{Addr: ":9000", TypesafeKey: "ts", TMDBKey: "tm", TorznabToken: "tz", ClassifyConcurrency: 8,
					TMDBConcurrency: 3, TMDBRate: 10, TMDBRetry: 240 * time.Hour, LazyTMDB: 7, DailyTokens: 1000000, SearchReserve: 40,
					Lease: 2 * time.Minute, Attempts: 5, Backoff: time.Hour, Retention: 720 * time.Hour, RetentionIdle: 72 * time.Hour, RetentionJournal: 168 * time.Hour,
					StatsInterval: time.Minute, LazyMax: 5, LazyWait: 500 * time.Millisecond,
					Scripts: rules.Scripts{unicode.Latin, unicode.Cyrillic, unicode.Greek}, Languages: russian,
					Probe: ingest.ProbeConfig{DailyBytes: 20 << 30, SearchReserve: 10, MaxBytes: 32 << 20, Lease: 5 * time.Minute, Attempts: 2, Backoff: 12 * time.Hour}, ReleaseCheck: true}
				d.Tasks = store.TaskOptions{MaxSize: 5000, MaxAge: 6 * time.Hour, DeadFor: 48 * time.Hour, NoPeersFor: 12 * time.Hour, NoProbes: true}
				d.Store = store.Options{MaxFiles: 50, ProbeBatch: 50, Languages: []string{"ru"}, TMDB: true}
			}},
		{name: "classifier endpoint", env: with("SNIFFARR_CLASSIFY_URL", "http://laya:8000/v1/systemone"),
			want: func(a *api.Config, _ *db.Config) { a.ClassifyURL = "http://laya:8000/v1/systemone" }},
		{name: "classifier key", env: with("SNIFFARR_CLASSIFY_API_KEY", "ck"), want: func(a *api.Config, _ *db.Config) { a.ClassifyKey = "ck" }},
		{name: "classifier endpoint not a URL", env: with("SNIFFARR_CLASSIFY_URL", "laya:8000"), err: "SNIFFARR_CLASSIFY_URL: want an http or https URL"},
		{name: "unlimited budget", env: with("SNIFFARR_CLASSIFY_DAILY_TOKENS", "0"), want: func(a *api.Config, _ *db.Config) { a.DailyTokens = 0 }},
		{name: "no reserve", env: with("SNIFFARR_CLASSIFY_SEARCH_RESERVE", "0"), want: func(a *api.Config, _ *db.Config) { a.SearchReserve = 0 }},
		{name: "unlimited downloads", env: with("SNIFFARR_PROBE_DAILY_BYTES", "0"), want: func(a *api.Config, _ *db.Config) { a.Probe.DailyBytes = 0 }},
		{name: "download budget in bytes", env: with("SNIFFARR_PROBE_DAILY_BYTES", "1000"), want: func(a *api.Config, _ *db.Config) { a.Probe.DailyBytes = 1000 }},
		{name: "download budget in a unit not known", env: with("SNIFFARR_PROBE_DAILY_BYTES", "100GB"), err: "SNIFFARR_PROBE_DAILY_BYTES: want a non-negative size in B, KiB, MiB, GiB or TiB"},
		{name: "probe size zero", env: with("SNIFFARR_PROBE_MAX_BYTES", "0"), err: "SNIFFARR_PROBE_MAX_BYTES: want a positive size in B, KiB, MiB, GiB or TiB"},
		{name: "probe reserve over 100", env: with("SNIFFARR_PROBE_SEARCH_RESERVE", "101"), err: "SNIFFARR_PROBE_SEARCH_RESERVE: want an integer from 0 to 100"},
		{name: "audio languages set but empty", env: with("SNIFFARR_AUDIO_LANGUAGES", ""), want: func(*api.Config, *db.Config) {}},
		{name: "audio languages", env: with("SNIFFARR_AUDIO_LANGUAGES", "ru"), want: func(a *api.Config, d *db.Config) { a.Languages, d.Store.Languages = russian, []string{"ru"} }},
		{name: "unknown audio language", env: with("SNIFFARR_AUDIO_LANGUAGES", "ru,xx"), err: `SNIFFARR_AUDIO_LANGUAGES: unknown audio language "xx"`},
		{name: "queue size not a number", env: with("SNIFFARR_QUEUE_MAX_SIZE", "1M"), err: "SNIFFARR_QUEUE_MAX_SIZE: want a positive integer"},
		{name: "probe batch zero", env: with("SNIFFARR_PROBE_BATCH", "0"), err: "SNIFFARR_PROBE_BATCH: want a positive integer"},
		{name: "nothing set", err: "SNIFFARR_DB is not set"},
		{name: "release check off", env: with("SNIFFARR_RELEASE_CHECK", "off"), want: func(a *api.Config, _ *db.Config) { a.ReleaseCheck = false }},
		{name: "release check no", env: with("SNIFFARR_RELEASE_CHECK", "no"), want: func(a *api.Config, _ *db.Config) { a.ReleaseCheck = false }},
		{name: "release check lazy", env: with("SNIFFARR_RELEASE_CHECK", "lazy"), want: func(*api.Config, *db.Config) {}},
		{name: "release check eager", env: with("SNIFFARR_RELEASE_CHECK", "eager"), want: func(a *api.Config, _ *db.Config) { a.ReleaseNotice = version.Eager }},
		{name: "release check unknown", env: with("SNIFFARR_RELEASE_CHECK", "false"), err: `SNIFFARR_RELEASE_CHECK: want no, off, lazy or eager, got "false"`},
		{name: "concurrency not a number", env: with("SNIFFARR_CLASSIFY_CONCURRENCY", "four"), err: "SNIFFARR_CLASSIFY_CONCURRENCY: want a positive integer"},
		{name: "zero concurrency", env: with("SNIFFARR_CLASSIFY_CONCURRENCY", "0"), err: "SNIFFARR_CLASSIFY_CONCURRENCY: want a positive integer"},
		{name: "negative budget", env: with("SNIFFARR_CLASSIFY_DAILY_TOKENS", "-1"), err: "SNIFFARR_CLASSIFY_DAILY_TOKENS: want a non-negative integer"},
		{name: "budget not a number", env: with("SNIFFARR_CLASSIFY_DAILY_TOKENS", "1e6"), err: "SNIFFARR_CLASSIFY_DAILY_TOKENS: want a non-negative integer"},
		{name: "lease not a duration", env: with("SNIFFARR_FETCH_LEASE", "60"), err: "SNIFFARR_FETCH_LEASE: want a positive duration"},
		{name: "zero attempts", env: with("SNIFFARR_FETCH_ATTEMPTS", "0"), err: "SNIFFARR_FETCH_ATTEMPTS: want a positive integer"},
		{name: "negative backoff", env: with("SNIFFARR_FETCH_BACKOFF", "-1m"), err: "SNIFFARR_FETCH_BACKOFF: want a positive duration"},
		{name: "idle retention not a duration", env: with("SNIFFARR_RETENTION_IDLE", "7d"), err: "SNIFFARR_RETENTION_IDLE: want a positive duration"},
		{name: "journal retention zero", env: with("SNIFFARR_RETENTION_JOURNAL", "0"), err: "SNIFFARR_RETENTION_JOURNAL: want a positive duration"},
		{name: "stats interval zero", env: with("SNIFFARR_STATS_INTERVAL", "0s"), err: "SNIFFARR_STATS_INTERVAL: want a positive duration"},
		{name: "no-peers window not a duration", env: with("SNIFFARR_QUEUE_NO_PEERS_FOR", "1d"), err: "SNIFFARR_QUEUE_NO_PEERS_FOR: want a positive duration"},
		{name: "reserve over 100", env: with("SNIFFARR_CLASSIFY_SEARCH_RESERVE", "101"), err: "SNIFFARR_CLASSIFY_SEARCH_RESERVE: want an integer from 0 to 100"},
		{name: "reserve a fraction", env: with("SNIFFARR_CLASSIFY_SEARCH_RESERVE", "0.25"), err: "SNIFFARR_CLASSIFY_SEARCH_RESERVE: want an integer from 0 to 100"},
		{name: "unknown script", env: with("SNIFFARR_SCRIPTS", "Latin,Elvish"), err: `SNIFFARR_SCRIPTS: unknown script "Elvish"`},
		{name: "zero tmdb rate", env: with("SNIFFARR_TMDB_RATE", "0"), err: "SNIFFARR_TMDB_RATE: want a positive integer"},
		{name: "arr", env: with("SNIFFARR_WEBHOOK_TOKEN", "wh", "SNIFFARR_RADARR_URL", "http://radarr:7878", "SNIFFARR_RADARR_API_KEY", "rk",
			"SNIFFARR_SONARR_URL", "https://example.org/sonarr", "SNIFFARR_SONARR_API_KEY", "sk"),
			want: func(a *api.Config, _ *db.Config) {
				a.WebhookToken = "wh"
				a.Radarr = api.ArrHistory{URL: "http://radarr:7878/api/v3", APIKey: "rk"}
				a.Sonarr = api.ArrHistory{URL: "https://example.org/sonarr/api/v3", APIKey: "sk"}
			}},
		{name: "arr API URL over the root", env: with("SNIFFARR_RADARR_URL", "https://radarr.example.com/", "SNIFFARR_RADARR_API_URL", "http://radarr.media:7878/api/v3", "SNIFFARR_RADARR_API_KEY", "rk"),
			want: func(a *api.Config, _ *db.Config) {
				a.Radarr = api.ArrHistory{URL: "http://radarr.media:7878/api/v3", APIKey: "rk"}
			}},
		{name: "arr API URL alone", env: with("SNIFFARR_SONARR_API_URL", "http://sonarr.media:8989/api/v3", "SNIFFARR_SONARR_API_KEY", "sk"),
			want: func(a *api.Config, _ *db.Config) {
				a.Sonarr = api.ArrHistory{URL: "http://sonarr.media:8989/api/v3", APIKey: "sk"}
			}},
		{name: "arr root with a slash", env: with("SNIFFARR_RADARR_URL", "https://radarr.example.com/", "SNIFFARR_RADARR_API_KEY", "rk"),
			want: func(a *api.Config, _ *db.Config) {
				a.Radarr = api.ArrHistory{URL: "https://radarr.example.com/api/v3", APIKey: "rk"}
			}},
		{name: "arr API URL without key", env: with("SNIFFARR_RADARR_API_URL", "http://radarr:7878/api/v3"),
			err: "SNIFFARR_RADARR_URL or SNIFFARR_RADARR_API_URL, and SNIFFARR_RADARR_API_KEY: set a URL with the key or neither"},
		{name: "arr API URL not http", env: with("SNIFFARR_SONARR_API_URL", "sonarr:8989/api/v3", "SNIFFARR_SONARR_API_KEY", "sk"), err: "SNIFFARR_SONARR_API_URL: want an http or https URL"},
		{name: "arr url without key", env: with("SNIFFARR_SONARR_URL", "http://sonarr:8989"), err: "SNIFFARR_SONARR_URL or SNIFFARR_SONARR_API_URL, and SNIFFARR_SONARR_API_KEY: set a URL with the key or neither"},
		{name: "arr key without url", env: with("SNIFFARR_RADARR_API_KEY", "rk"), err: "SNIFFARR_RADARR_URL or SNIFFARR_RADARR_API_URL, and SNIFFARR_RADARR_API_KEY: set a URL with the key or neither"},
		{name: "arr url not http", env: with("SNIFFARR_RADARR_URL", "radarr:7878", "SNIFFARR_RADARR_API_KEY", "rk"), err: "SNIFFARR_RADARR_URL: want an http or https URL"},
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
