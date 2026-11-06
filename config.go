package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mikluko/sniffarr/internal/app/api"
	"github.com/mikluko/sniffarr/internal/app/client"
	"github.com/mikluko/sniffarr/internal/app/db"
	"github.com/mikluko/sniffarr/internal/app/dht"
	"github.com/mikluko/sniffarr/internal/app/fetch"
	"github.com/mikluko/sniffarr/internal/app/sniff"
	"github.com/mikluko/sniffarr/internal/app/ui"
	"github.com/mikluko/sniffarr/internal/audio"
	"github.com/mikluko/sniffarr/internal/classify"
	"github.com/mikluko/sniffarr/internal/feed"
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
)

// components are the flags that switch each component on.
type components struct {
	api, ui, sniff, fetch bool
}

// clients reports whether a component that reads the API is on.
func (c components) clients() bool { return c.ui || c.sniff || c.fetch }

// dht reports whether a component that needs the DHT node is on.
func (c components) dht() bool { return c.sniff || c.fetch }

type config struct {
	on       components
	api      api.Config
	db       db.Config
	ui       ui.Config
	client   client.Config
	dht      dht.Config
	sniff    sniff.Config
	fetch    fetch.Config
	logLevel slog.Level
}

func (c config) LogLevel() slog.Level { return c.logLevel }

// defaultRetention is how long a stored torrent is kept without being seen.
const defaultRetention = 90 * 24 * time.Hour

// defaultRetentionIdle is defaultRetention for a torrent with no TMDB work.
const defaultRetentionIdle = 7 * 24 * time.Hour

// defaultStatsInterval is how often the API recomputes the counts of the
// data and home pages.
const defaultStatsInterval = 5 * time.Minute

// defaultUIAddr is the UI's listen address when SNIFFARR_UI_ADDR is unset.
const defaultUIAddr = ":8081"

// load reads the configuration from args and lookup, writing flag usage and
// errors to output. Each component's variables are read only when it is on.
func load(args []string, lookup func(string) (string, bool), output io.Writer) (config, error) {
	getenv := func(name string) string {
		v, _ := lookup(name)
		return v
	}
	var c config
	fs := flag.NewFlagSet("sniffarr", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.BoolVar(&c.on.api, "api", false, "serve the API over the SQLite store, with Torznab and the classify and TMDB workers")
	fs.BoolVar(&c.on.ui, "ui", false, "serve the web UI, a client of the browse API")
	fs.BoolVar(&c.on.sniff, "sniff", false, "walk the DHT and report the infohashes it reveals to the ingest API")
	fs.BoolVar(&c.on.fetch, "fetch", false, "take fetch tasks from the ingest API and return their metadata")
	fs.IntVar(&c.dht.Network.Port, "port", sniffer.DefaultPort, "TCP and UDP port for peers and the DHT; 0 picks a free one")
	fs.BoolVar(&c.dht.Network.UPnP, "upnp", false, "ask the gateways on the local network to forward the listening port, TCP and UDP, by UPnP")
	fs.StringVar(&c.dht.STUN, "stun", stun.DefaultServer, "STUN server that tells -sniff its public address, which gives its node ID the BEP 42 prefix; empty leaves the ID as stored")
	fs.IntVar(&c.dht.Network.Fetchers, "fetchers", sniffer.DefaultFetchers, "metadata fetches at once")
	fs.IntVar(&c.dht.Network.Rate, "rate", sniffer.DefaultRate, "DHT queries, and new peer connections, a second")
	fs.DurationVar(&c.dht.Network.FetchTimeout, "fetch-timeout", sniffer.DefaultFetchTimeout, "how long to wait for a torrent's metadata once it has peers")
	fs.DurationVar(&c.dht.Network.PeerWait, "peer-wait", sniffer.DefaultPeerWait, "how long to look for a torrent's peers on the DHT")
	fs.IntVar(&c.dht.Network.MinPeers, "min-peers", sniffer.DefaultMinPeers, "peers a torrent needs within -peer-wait to have its metadata fetched")
	fs.IntVar(&c.sniff.Walkers, "walkers", sniffer.DefaultWalkers, "sample_infohashes queries at once")
	var probeDir string
	probers, probeTimeout, seedMaxAge := sniffer.DefaultProbers, sniffer.DefaultProbeTimeout, sniffer.DefaultSeedMaxAge
	seedMaxSize := sizeFlag(sniffer.DefaultSeedMaxBytes)
	fs.StringVar(&probeDir, "probe-dir", "", "directory where -fetch keeps the pieces it probes and seeds; empty probes nothing")
	fs.IntVar(&probers, "probers", probers, "probes at once")
	fs.DurationVar(&probeTimeout, "probe-timeout", probeTimeout, "how long one probe may take, from looking for peers to the tracks")
	fs.Var(&seedMaxSize, "seed-max-size", "piece data kept for seeding, such as 200GiB; the oldest torrents go first beyond it")
	fs.DurationVar(&seedMaxAge, "seed-max-age", seedMaxAge, "how long a probed torrent is seeded at most")
	fs.TextVar(&c.logLevel, "log-level", slog.LevelInfo, "level to log at: debug, info, warn or error; SNIFFARR_LOG_LEVEL when not given")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if v := getenv("SNIFFARR_LOG_LEVEL"); v != "" && !set["log-level"] {
		if err := c.logLevel.UnmarshalText([]byte(v)); err != nil {
			return c, errors.New("SNIFFARR_LOG_LEVEL: want debug, info, warn or error")
		}
	}
	switch {
	case c.on == (components{}):
		return c, errors.New("-api, -ui, -sniff and -fetch are all off: nothing to do")
	case c.dht.Network.Port < 0 || c.dht.Network.Port > 65535:
		return c, errors.New("-port: want 0 to 65535")
	case c.dht.Network.Fetchers < 1, c.dht.Network.Rate < 1, c.sniff.Walkers < 1, c.dht.Network.MinPeers < 1:
		return c, errors.New("-fetchers, -rate, -walkers and -min-peers: want positive integers")
	case c.dht.Network.FetchTimeout <= 0, c.dht.Network.PeerWait <= 0:
		return c, errors.New("-fetch-timeout and -peer-wait: want positive durations")
	}
	c.dht.Network.Passive = !c.on.sniff
	c.fetch.Workers = c.dht.Network.Fetchers
	switch {
	case probers < 1:
		return c, errors.New("-probers: want a positive integer")
	case probeTimeout <= 0, seedMaxAge <= 0:
		return c, errors.New("-probe-timeout and -seed-max-age: want positive durations")
	case probeDir != "" && !c.on.fetch:
		return c, errors.New("-probe-dir: probes are taken by -fetch, which is off")
	case probeDir != "":
		c.dht.Network.Seed = sniffer.SeederConfig{Dir: probeDir, MaxBytes: int64(seedMaxSize), MaxAge: seedMaxAge}
		c.fetch.Probe, c.fetch.Probers, c.fetch.ProbeTimeout = true, probers, probeTimeout
	}

	for _, name := range required(c.on) {
		if getenv(name) == "" {
			return c, fmt.Errorf("%s is not set", name)
		}
	}
	var err error
	if c.client, err = loadClient(c.on, getenv); err != nil {
		return c, err
	}
	if c.on.ui {
		c.ui = ui.Config{Addr: or(getenv("SNIFFARR_UI_ADDR"), defaultUIAddr), OIDCIssuer: getenv("SNIFFARR_OIDC_ISSUER"), SeerrURL: getenv("SNIFFARR_SEERR_URL")}
	}
	if c.on.api {
		if c.api, c.db, err = loadAPI(lookup); err != nil {
			return c, err
		}
	}
	return c, nil
}

// required lists the variables the components that are on need, each once.
func required(on components) []string {
	var names []string
	add := func(name string) {
		for _, n := range names {
			if n == name {
				return
			}
		}
		names = append(names, name)
	}
	if on.api {
		add("SNIFFARR_DB")
	}
	if on.clients() && !on.api {
		add("SNIFFARR_URL")
	}
	return names
}

// loadClient reads where the clients reach the API: in this process with
// -api, where SNIFFARR_URL must be unset, else at SNIFFARR_URL.
func loadClient(on components, getenv func(string) string) (client.Config, error) {
	var c client.Config
	raw := getenv("SNIFFARR_URL")
	switch {
	case raw == "":
		return c, nil
	case on.api:
		return c, errors.New("SNIFFARR_URL is set, but -api serves the API in this process: unset it")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return c, fmt.Errorf("SNIFFARR_URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return c, errors.New("SNIFFARR_URL: want an http or https URL")
	}
	c.URL = u
	return c, nil
}

// loadAPI reads the API's and the store's configuration. An unset or empty
// SNIFFARR_AUDIO_LANGUAGES leaves the audio extension off.
func loadAPI(lookup func(string) (string, bool)) (api.Config, db.Config, error) {
	getenv := func(name string) string {
		v, _ := lookup(name)
		return v
	}
	a := api.Config{
		Addr:         or(getenv("SNIFFARR_ADDR"), ":8080"),
		TypesafeKey:  getenv("TYPESAFE_API_KEY"),
		TorznabToken: getenv("SNIFFARR_TORZNAB_TOKEN"),
		WebhookToken: getenv("SNIFFARR_WEBHOOK_TOKEN"),
	}
	d := db.Config{Path: getenv("SNIFFARR_DB")}
	var err error
	if a.Languages, err = audio.Parse(getenv("SNIFFARR_AUDIO_LANGUAGES")); err != nil {
		return a, d, fmt.Errorf("SNIFFARR_AUDIO_LANGUAGES: %w", err)
	}
	if a.ClassifyConcurrency, err = positive(getenv, "SNIFFARR_CLASSIFY_CONCURRENCY", 4); err != nil {
		return a, d, err
	}
	if a.DailyTokens, err = strconv.ParseInt(or(getenv("SNIFFARR_CLASSIFY_DAILY_TOKENS"), strconv.Itoa(classify.DefaultDailyTokens)), 10, 64); err != nil || a.DailyTokens < 0 {
		return a, d, errors.New("SNIFFARR_CLASSIFY_DAILY_TOKENS: want a non-negative integer")
	}
	if a.SearchReserve, err = percent(getenv, "SNIFFARR_CLASSIFY_SEARCH_RESERVE", classify.DefaultSearchReserve); err != nil {
		return a, d, err
	}
	if a.Attempts, err = positive(getenv, "SNIFFARR_FETCH_ATTEMPTS", ingest.DefaultAttempts); err != nil {
		return a, d, err
	}
	if a.Lease, err = duration(getenv, "SNIFFARR_FETCH_LEASE", ingest.DefaultLease); err != nil {
		return a, d, err
	}
	if a.Backoff, err = duration(getenv, "SNIFFARR_FETCH_BACKOFF", ingest.DefaultBackoff); err != nil {
		return a, d, err
	}
	if a.Probe, err = loadProbe(getenv); err != nil {
		return a, d, err
	}
	if d.Tasks.MaxSize, err = positive(getenv, "SNIFFARR_QUEUE_MAX_SIZE", store.DefaultMaxSize); err != nil {
		return a, d, err
	}
	if d.Tasks.MaxAge, err = duration(getenv, "SNIFFARR_QUEUE_MAX_AGE", store.DefaultMaxAge); err != nil {
		return a, d, err
	}
	if d.Tasks.DeadFor, err = duration(getenv, "SNIFFARR_QUEUE_DEAD_FOR", store.DefaultDeadFor); err != nil {
		return a, d, err
	}
	if d.Tasks.NoPeersFor, err = duration(getenv, "SNIFFARR_QUEUE_NO_PEERS_FOR", store.DefaultNoPeersFor); err != nil {
		return a, d, err
	}
	if d.Store.MaxFiles, err = positive(getenv, "SNIFFARR_STORE_MAX_FILES", store.DefaultMaxFiles); err != nil {
		return a, d, err
	}
	if a.Retention, err = duration(getenv, "SNIFFARR_RETENTION", defaultRetention); err != nil {
		return a, d, err
	}
	if a.RetentionIdle, err = duration(getenv, "SNIFFARR_RETENTION_IDLE", defaultRetentionIdle); err != nil {
		return a, d, err
	}
	if a.StatsInterval, err = duration(getenv, "SNIFFARR_STATS_INTERVAL", defaultStatsInterval); err != nil {
		return a, d, err
	}
	if a.LazyMax, err = positive(getenv, "SNIFFARR_SEARCH_CLASSIFY_MAX", search.DefaultLazyMax); err != nil {
		return a, d, err
	}
	if a.LazyWait, err = duration(getenv, "SNIFFARR_SEARCH_CLASSIFY_WAIT", search.DefaultLazyWait); err != nil {
		return a, d, err
	}
	if a.LazyTMDB, err = positive(getenv, "SNIFFARR_SEARCH_TMDB_MAX", search.DefaultLazyTMDB); err != nil {
		return a, d, err
	}
	if a.TMDBConcurrency, err = positive(getenv, "SNIFFARR_TMDB_CONCURRENCY", tmdb.DefaultConcurrency); err != nil {
		return a, d, err
	}
	if a.TMDBRate, err = positive(getenv, "SNIFFARR_TMDB_RATE", tmdb.DefaultRate); err != nil {
		return a, d, err
	}
	if a.TMDBRetry, err = duration(getenv, "SNIFFARR_TMDB_RETRY", tmdb.DefaultRetry); err != nil {
		return a, d, err
	}
	if a.Radarr, err = arrHistory(getenv, "RADARR"); err != nil {
		return a, d, err
	}
	if a.Sonarr, err = arrHistory(getenv, "SONARR"); err != nil {
		return a, d, err
	}
	if a.Scripts, err = rules.ParseScripts(getenv("SNIFFARR_SCRIPTS")); err != nil {
		return a, d, fmt.Errorf("SNIFFARR_SCRIPTS: %w", err)
	}
	if a.Feeds, err = loadFeeds(getenv); err != nil {
		return a, d, err
	}
	if key, ok := lookup("TMDB_API_KEY"); ok {
		a.TMDBKey = key
	} else {
		a.TMDBKey = tmdb.DefaultKey
	}
	d.Store.TMDB = a.TMDBKey != ""
	d.Store.Languages = audio.Tags(a.Languages)
	return a, d, nil
}

// loadProbe reads the probe tasks' settings.
func loadProbe(getenv func(string) string) (ingest.ProbeConfig, error) {
	var p ingest.ProbeConfig
	var err error
	if p.DailyBytes, err = size(getenv, "SNIFFARR_PROBE_DAILY_BYTES", ingest.DefaultProbeDailyBytes, false); err != nil {
		return p, err
	}
	if p.SearchReserve, err = percent(getenv, "SNIFFARR_PROBE_SEARCH_RESERVE", ingest.DefaultProbeSearchReserve); err != nil {
		return p, err
	}
	if p.MaxBytes, err = size(getenv, "SNIFFARR_PROBE_MAX_BYTES", ingest.DefaultProbeMaxBytes, true); err != nil {
		return p, err
	}
	if p.Lease, err = duration(getenv, "SNIFFARR_PROBE_LEASE", ingest.DefaultProbeLease); err != nil {
		return p, err
	}
	if p.Attempts, err = positive(getenv, "SNIFFARR_PROBE_ATTEMPTS", ingest.DefaultProbeAttempts); err != nil {
		return p, err
	}
	p.Backoff, err = duration(getenv, "SNIFFARR_PROBE_BACKOFF", ingest.DefaultProbeBackoff)
	return p, err
}

// loadFeeds reads SNIFFARR_FEEDS, the comma-separated names of the feeds
// read, and each named feed's settings.
func loadFeeds(getenv func(string) string) (api.Feeds, error) {
	var f api.Feeds
	for _, name := range strings.Split(getenv("SNIFFARR_FEEDS"), ",") {
		name = strings.TrimSpace(name)
		switch name {
		case "":
		case rutracker.Name, nnmclub.Name, rutor.Name:
			f.Sources = append(f.Sources, name)
		default:
			return f, fmt.Errorf("SNIFFARR_FEEDS: %q is not a feed; %s, %s and %s are", name, rutracker.Name, nnmclub.Name, rutor.Name)
		}
	}
	if len(f.Sources) == 0 {
		return f, nil
	}
	var err error
	if f.Interval, err = duration(getenv, "SNIFFARR_FEED_INTERVAL", feed.DefaultInterval); err != nil {
		return f, err
	}
	f.Rutracker.URL = or(getenv("SNIFFARR_RUTRACKER_FEED_URL"), rutracker.DefaultURL)
	if f.Rutracker.Forums, err = numbers(getenv, "SNIFFARR_RUTRACKER_FORUMS", "0", "forum numbers, 0 for the whole tracker"); err != nil {
		return f, err
	}
	f.Nnmclub.URL = or(getenv("SNIFFARR_NNMCLUB_URL"), nnmclub.DefaultURL)
	if f.Nnmclub.Forums, err = numbers(getenv, "SNIFFARR_NNMCLUB_FORUMS", "", "forum numbers, none for every public forum"); err != nil {
		return f, err
	}
	f.Rutor.URL = or(getenv("SNIFFARR_RUTOR_URL"), rutor.DefaultURL)
	if f.Rutor.Categories, err = numbers(getenv, "SNIFFARR_RUTOR_CATEGORIES", "0", "category numbers, 0 for every one"); err != nil {
		return f, err
	}
	if f.Rutor.Depth, err = positive(getenv, "SNIFFARR_RUTOR_DEPTH", rutor.DefaultDepth); err != nil {
		return f, err
	}
	return f, nil
}

// numbers reads a comma-separated list of non-negative integers from the
// variable, def when it is unset; empty is none. want names the list in
// the error.
func numbers(getenv func(string) string, name, def, want string) ([]int, error) {
	var out []int
	for _, s := range strings.Split(or(getenv(name), def), ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("%s: want comma-separated %s", name, want)
		}
		out = append(out, n)
	}
	return out, nil
}

// arrHistory reads SNIFFARR_<app>_URL and SNIFFARR_<app>_API_KEY, which
// are set together or not at all.
func arrHistory(getenv func(string) string, app string) (api.ArrHistory, error) {
	urlName, keyName := "SNIFFARR_"+app+"_URL", "SNIFFARR_"+app+"_API_KEY"
	h := api.ArrHistory{URL: getenv(urlName), APIKey: getenv(keyName)}
	switch {
	case h.URL == "" && h.APIKey == "":
		return h, nil
	case h.URL == "" || h.APIKey == "":
		return h, fmt.Errorf("%s and %s: set both or neither", urlName, keyName)
	}
	u, err := url.Parse(h.URL)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return h, fmt.Errorf("%s: want an http or https URL", urlName)
	}
	return h, nil
}

func positive(getenv func(string) string, name string, def int) (int, error) {
	v := getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s: want a positive integer", name)
	}
	return n, nil
}

func percent(getenv func(string) string, name string, def int) (int, error) {
	v := getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > 100 {
		return 0, fmt.Errorf("%s: want an integer from 0 to 100", name)
	}
	return n, nil
}

// size reads a variable of bytes, such as 100GiB; 0 is refused when
// positive.
func size(getenv func(string) string, name string, def int64, positive bool) (int64, error) {
	v := getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := parseSize(v)
	if err != nil || positive && n == 0 {
		want := "non-negative"
		if positive {
			want = "positive"
		}
		return 0, fmt.Errorf("%s: want a %s size in B, KiB, MiB, GiB or TiB", name, want)
	}
	return n, nil
}

// sizeFlag is a flag of bytes, such as 200GiB.
type sizeFlag int64

func (f *sizeFlag) String() string { return strconv.FormatInt(int64(*f), 10) }

func (f *sizeFlag) Set(s string) error {
	n, err := parseSize(s)
	if err != nil || n == 0 {
		return errors.New("want a positive size in B, KiB, MiB, GiB or TiB")
	}
	*f = sizeFlag(n)
	return nil
}

// sizeUnits are the units parseSize takes, longest first.
var sizeUnits = []struct {
	suffix string
	shift  uint
}{{"TiB", 40}, {"GiB", 30}, {"MiB", 20}, {"KiB", 10}, {"B", 0}}

// parseSize parses a non-negative count of bytes with an optional binary
// unit, such as 100GiB or 4096.
func parseSize(s string) (int64, error) {
	var shift uint
	for _, u := range sizeUnits {
		if num, ok := strings.CutSuffix(s, u.suffix); ok {
			s, shift = num, u.shift
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || n > math.MaxInt64>>shift {
		return 0, fmt.Errorf("size %q", s)
	}
	return n << shift, nil
}

func duration(getenv func(string) string, name string, def time.Duration) (time.Duration, error) {
	v := getenv(name)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s: want a positive duration", name)
	}
	return d, nil
}

func or(v, def string) string {
	if v != "" {
		return v
	}
	return def
}
