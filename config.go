package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"net/url"
	"strconv"
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
	"github.com/mikluko/sniffarr/internal/ingest"
	"github.com/mikluko/sniffarr/internal/queue"
	"github.com/mikluko/sniffarr/internal/rules"
	"github.com/mikluko/sniffarr/internal/search"
	sniffer "github.com/mikluko/sniffarr/internal/sniff"
	"github.com/mikluko/sniffarr/internal/store"
	"github.com/mikluko/sniffarr/internal/titles"
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
	fs.BoolVar(&c.on.api, "api", false, "serve the API over the SQLite store, with Torznab and the classify and titles workers")
	fs.BoolVar(&c.on.ui, "ui", false, "serve the web UI, a client of the browse API")
	fs.BoolVar(&c.on.sniff, "sniff", false, "walk the DHT and report the infohashes it reveals to the ingest API")
	fs.BoolVar(&c.on.fetch, "fetch", false, "take fetch tasks from the ingest API and return their metadata")
	fs.IntVar(&c.dht.Network.Port, "port", sniffer.DefaultPort, "TCP and UDP port for peers and the DHT; 0 picks a free one")
	fs.BoolVar(&c.dht.Network.UPnP, "upnp", false, "ask the gateways on the local network to forward the listening port, TCP and UDP, by UPnP")
	fs.TextVar(&c.dht.Network.PublicIP, "public-ip", netip.Addr{}, "the address other DHT nodes see this node at, which gives its node ID the BEP 42 prefix; empty leaves the ID as stored")
	fs.IntVar(&c.dht.Network.Fetchers, "fetchers", sniffer.DefaultFetchers, "metadata fetches at once")
	fs.IntVar(&c.dht.Network.Rate, "rate", sniffer.DefaultRate, "DHT queries, and new peer connections, a second")
	fs.DurationVar(&c.dht.Network.FetchTimeout, "fetch-timeout", sniffer.DefaultFetchTimeout, "how long to wait for a torrent's metadata once it has peers")
	fs.DurationVar(&c.dht.Network.PeerWait, "peer-wait", sniffer.DefaultPeerWait, "how long to look for a torrent's peers on the DHT")
	fs.IntVar(&c.dht.Network.MinPeers, "min-peers", sniffer.DefaultMinPeers, "peers a torrent needs within -peer-wait to have its metadata fetched")
	fs.IntVar(&c.sniff.Walkers, "walkers", sniffer.DefaultWalkers, "sample_infohashes queries at once")
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

	for _, name := range required(c.on) {
		if getenv(name) == "" {
			return c, fmt.Errorf("%s is not set", name)
		}
	}
	var err error
	if c.client, err = loadClient(c.on, getenv); err != nil {
		return c, err
	}
	languages, ok := lookup("SNIFFARR_AUDIO_LANGUAGES")
	if !ok {
		languages = audio.DefaultLanguages
	}
	langs, err := audio.Parse(languages)
	if err != nil {
		return c, fmt.Errorf("SNIFFARR_AUDIO_LANGUAGES: %w", err)
	}
	if c.on.ui {
		c.ui = ui.Config{Addr: or(getenv("SNIFFARR_UI_ADDR"), defaultUIAddr), OIDCIssuer: getenv("SNIFFARR_OIDC_ISSUER"), Languages: langs}
	}
	if c.on.api {
		if c.api, c.db, err = loadAPI(lookup, langs); err != nil {
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
		add("SNIFFARR_INGEST_TOKEN")
		add("SNIFFARR_SEARCH_TOKEN")
	}
	if on.ui {
		add("SNIFFARR_SEARCH_TOKEN")
	}
	if on.dht() {
		add("SNIFFARR_INGEST_TOKEN")
	}
	if on.clients() && !on.api {
		add("SNIFFARR_URL")
	}
	return names
}

// loadClient reads where the clients reach the API: in this process with
// -api, where SNIFFARR_URL must be unset, else at SNIFFARR_URL.
func loadClient(on components, getenv func(string) string) (client.Config, error) {
	c := client.Config{IngestToken: getenv("SNIFFARR_INGEST_TOKEN"), SearchToken: getenv("SNIFFARR_SEARCH_TOKEN")}
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

// loadAPI reads the API's and the store's configuration.
func loadAPI(lookup func(string) (string, bool), langs []audio.Language) (api.Config, db.Config, error) {
	getenv := func(name string) string {
		v, _ := lookup(name)
		return v
	}
	a := api.Config{
		Addr:         or(getenv("SNIFFARR_ADDR"), ":8080"),
		IngestToken:  getenv("SNIFFARR_INGEST_TOKEN"),
		SearchToken:  getenv("SNIFFARR_SEARCH_TOKEN"),
		TypesafeKey:  getenv("TYPESAFE_API_KEY"),
		TorznabToken: getenv("SNIFFARR_TORZNAB_TOKEN"),
		Languages:    langs,
	}
	d := db.Config{Path: getenv("SNIFFARR_DB")}
	var err error
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
	if d.Queue.MaxSize, err = positive(getenv, "SNIFFARR_QUEUE_MAX_SIZE", queue.DefaultMaxSize); err != nil {
		return a, d, err
	}
	if d.Queue.MaxAge, err = duration(getenv, "SNIFFARR_QUEUE_MAX_AGE", queue.DefaultMaxAge); err != nil {
		return a, d, err
	}
	if d.Queue.DeadFor, err = duration(getenv, "SNIFFARR_QUEUE_DEAD_FOR", queue.DefaultDeadFor); err != nil {
		return a, d, err
	}
	if d.Queue.NoPeersFor, err = duration(getenv, "SNIFFARR_QUEUE_NO_PEERS_FOR", queue.DefaultNoPeersFor); err != nil {
		return a, d, err
	}
	if d.Store.MaxFiles, err = positive(getenv, "SNIFFARR_STORE_MAX_FILES", store.DefaultMaxFiles); err != nil {
		return a, d, err
	}
	if a.Retention, err = duration(getenv, "SNIFFARR_RETENTION", defaultRetention); err != nil {
		return a, d, err
	}
	if a.LazyMax, err = positive(getenv, "SNIFFARR_SEARCH_CLASSIFY_MAX", search.DefaultLazyMax); err != nil {
		return a, d, err
	}
	if a.LazyWait, err = duration(getenv, "SNIFFARR_SEARCH_CLASSIFY_WAIT", search.DefaultLazyWait); err != nil {
		return a, d, err
	}
	if a.LazyTitles, err = positive(getenv, "SNIFFARR_SEARCH_TITLES_MAX", search.DefaultLazyTitles); err != nil {
		return a, d, err
	}
	if a.TitlesConcurrency, err = positive(getenv, "SNIFFARR_TITLES_CONCURRENCY", titles.DefaultConcurrency); err != nil {
		return a, d, err
	}
	if a.TitlesRate, err = positive(getenv, "SNIFFARR_TITLES_RATE", titles.DefaultRate); err != nil {
		return a, d, err
	}
	if a.TitlesRetry, err = duration(getenv, "SNIFFARR_TITLES_RETRY", titles.DefaultRetry); err != nil {
		return a, d, err
	}
	if a.Scripts, err = rules.ParseScripts(getenv("SNIFFARR_SCRIPTS")); err != nil {
		return a, d, fmt.Errorf("SNIFFARR_SCRIPTS: %w", err)
	}
	if key, ok := lookup("TMDB_API_KEY"); ok {
		a.TMDBKey = key
	} else {
		a.TMDBKey = titles.DefaultKey
	}
	d.Store.Titles = a.TMDBKey != ""
	d.Store.Languages = audio.Tags(langs)
	return a, d, nil
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
