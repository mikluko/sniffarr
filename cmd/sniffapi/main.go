// Command sniffapi serves the ingest work queue and the search API over a
// SQLite database and classifies stored torrents with TypeSafe's Jev. It is
// configured by environment variables; see the README.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/mikluko/sniffarr/internal/audio"
	"github.com/mikluko/sniffarr/internal/classify"
	"github.com/mikluko/sniffarr/internal/cli"
	"github.com/mikluko/sniffarr/internal/database"
	"github.com/mikluko/sniffarr/internal/ingest"
	"github.com/mikluko/sniffarr/internal/queue"
	"github.com/mikluko/sniffarr/internal/rules"
	"github.com/mikluko/sniffarr/internal/search"
	"github.com/mikluko/sniffarr/internal/server"
	"github.com/mikluko/sniffarr/internal/store"
	"github.com/mikluko/sniffarr/internal/telemetry"
	"github.com/mikluko/sniffarr/internal/titles"
)

type config struct {
	addr, db                 string
	ingestToken, searchToken string
	typesafeKey              string
	tmdbKey                  string
	titlesConcurrency        int
	titlesRate               int
	titlesRetry              time.Duration
	lazyTitles               int
	concurrency              int
	dailyTokens              int64
	searchReserve            int
	lease, backoff           time.Duration
	attempts                 int
	queue                    queue.Options
	store                    store.Options
	retention                time.Duration
	lazyMax                  int
	lazyWait                 time.Duration
	scripts                  rules.Scripts
	languages                []audio.Language
	logLevel                 slog.Level
}

func (c config) LogLevel() slog.Level { return c.logLevel }

// defaultRetention is how long a stored torrent is kept without being seen.
const defaultRetention = 90 * 24 * time.Hour

// sweepBatch is the most torrents one retention transaction deletes.
const sweepBatch = 1000

func main() {
	cli.Main(func() (config, error) { return load(os.LookupEnv) }, run)
}

// load reads the configuration from lookup.
func load(lookup func(string) (string, bool)) (config, error) {
	getenv := func(name string) string {
		v, _ := lookup(name)
		return v
	}
	for _, name := range []string{"SNIFFARR_DB", "SNIFFARR_INGEST_TOKEN", "SNIFFARR_SEARCH_TOKEN"} {
		if getenv(name) == "" {
			return config{}, fmt.Errorf("%s is not set", name)
		}
	}
	c := config{
		addr:        or(getenv("SNIFFARR_ADDR"), ":8080"),
		db:          getenv("SNIFFARR_DB"),
		ingestToken: getenv("SNIFFARR_INGEST_TOKEN"),
		searchToken: getenv("SNIFFARR_SEARCH_TOKEN"),
		typesafeKey: getenv("TYPESAFE_API_KEY"),
		tmdbKey:     getenv("TMDB_API_KEY"),
	}
	var err error
	if c.concurrency, err = positive(getenv, "SNIFFARR_CLASSIFY_CONCURRENCY", 4); err != nil {
		return c, err
	}
	if c.dailyTokens, err = strconv.ParseInt(or(getenv("SNIFFARR_CLASSIFY_DAILY_TOKENS"), strconv.Itoa(classify.DefaultDailyTokens)), 10, 64); err != nil || c.dailyTokens < 0 {
		return c, errors.New("SNIFFARR_CLASSIFY_DAILY_TOKENS: want a non-negative integer")
	}
	if c.searchReserve, err = percent(getenv, "SNIFFARR_CLASSIFY_SEARCH_RESERVE", classify.DefaultSearchReserve); err != nil {
		return c, err
	}
	if c.attempts, err = positive(getenv, "SNIFFARR_FETCH_ATTEMPTS", ingest.DefaultAttempts); err != nil {
		return c, err
	}
	if c.lease, err = duration(getenv, "SNIFFARR_FETCH_LEASE", ingest.DefaultLease); err != nil {
		return c, err
	}
	if c.backoff, err = duration(getenv, "SNIFFARR_FETCH_BACKOFF", ingest.DefaultBackoff); err != nil {
		return c, err
	}
	if c.queue.MaxSize, err = positive(getenv, "SNIFFARR_QUEUE_MAX_SIZE", queue.DefaultMaxSize); err != nil {
		return c, err
	}
	if c.queue.MaxAge, err = duration(getenv, "SNIFFARR_QUEUE_MAX_AGE", queue.DefaultMaxAge); err != nil {
		return c, err
	}
	if c.queue.DeadFor, err = duration(getenv, "SNIFFARR_QUEUE_DEAD_FOR", queue.DefaultDeadFor); err != nil {
		return c, err
	}
	if c.queue.NoPeersFor, err = duration(getenv, "SNIFFARR_QUEUE_NO_PEERS_FOR", queue.DefaultNoPeersFor); err != nil {
		return c, err
	}
	if c.store.MaxFiles, err = positive(getenv, "SNIFFARR_STORE_MAX_FILES", store.DefaultMaxFiles); err != nil {
		return c, err
	}
	if c.retention, err = duration(getenv, "SNIFFARR_RETENTION", defaultRetention); err != nil {
		return c, err
	}
	if c.lazyMax, err = positive(getenv, "SNIFFARR_SEARCH_CLASSIFY_MAX", search.DefaultLazyMax); err != nil {
		return c, err
	}
	if c.lazyWait, err = duration(getenv, "SNIFFARR_SEARCH_CLASSIFY_WAIT", search.DefaultLazyWait); err != nil {
		return c, err
	}
	if c.lazyTitles, err = positive(getenv, "SNIFFARR_SEARCH_TITLES_MAX", search.DefaultLazyTitles); err != nil {
		return c, err
	}
	if c.titlesConcurrency, err = positive(getenv, "SNIFFARR_TITLES_CONCURRENCY", titles.DefaultConcurrency); err != nil {
		return c, err
	}
	if c.titlesRate, err = positive(getenv, "SNIFFARR_TITLES_RATE", titles.DefaultRate); err != nil {
		return c, err
	}
	if c.titlesRetry, err = duration(getenv, "SNIFFARR_TITLES_RETRY", titles.DefaultRetry); err != nil {
		return c, err
	}
	c.store.Titles = c.tmdbKey != ""
	if c.scripts, err = rules.ParseScripts(getenv("SNIFFARR_SCRIPTS")); err != nil {
		return c, fmt.Errorf("SNIFFARR_SCRIPTS: %w", err)
	}
	languages, ok := lookup("SNIFFARR_AUDIO_LANGUAGES")
	if !ok {
		languages = audio.DefaultLanguages
	}
	if c.languages, err = audio.Parse(languages); err != nil {
		return c, fmt.Errorf("SNIFFARR_AUDIO_LANGUAGES: %w", err)
	}
	c.store.Languages = audio.Tags(c.languages)
	if err := c.logLevel.UnmarshalText([]byte(or(getenv("SNIFFARR_LOG_LEVEL"), "info"))); err != nil {
		return c, errors.New("SNIFFARR_LOG_LEVEL: want debug, info, warn or error")
	}
	return c, nil
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

func run(ctx context.Context, cfg config, log *slog.Logger) error {
	shutdown, err := telemetry.Setup(ctx, "sniffapi", log)
	if err != nil {
		return fmt.Errorf("telemetry: %w", err)
	}
	defer telemetry.Flush(ctx, shutdown, log)
	db, err := database.Open(ctx, cfg.db)
	if err != nil {
		return fmt.Errorf("open %s: %w", cfg.db, err)
	}
	defer db.Close()
	s, err := store.New(ctx, db, cfg.store)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	q, err := queue.New(ctx, db, cfg.queue)
	if err != nil {
		return fmt.Errorf("queue: %w", err)
	}
	if err := q.Reset(ctx); err != nil {
		return fmt.Errorf("reset leases: %w", err)
	}

	var jev *classify.Jev
	var matcher *titles.Matcher
	var lazy *search.Lazy
	if cfg.typesafeKey != "" || cfg.tmdbKey != "" {
		lazy = &search.Lazy{Max: cfg.lazyMax, MaxTitles: cfg.lazyTitles, Wait: cfg.lazyWait, MaxFiles: classify.MaxFiles, Log: log}
	}
	if cfg.typesafeKey == "" {
		log.Warn("TYPESAFE_API_KEY is not set; torrents are classified by their names only")
	} else {
		client := classify.New(cfg.typesafeKey)
		client.Languages = cfg.languages
		jev = &classify.Jev{Store: s, Classifier: client, DailyTokens: cfg.dailyTokens, Log: log}
		lazy.Classifier = jev
	}
	if cfg.tmdbKey != "" {
		matcher = &titles.Matcher{TMDB: titles.NewTMDB(cfg.tmdbKey, cfg.titlesRate), Store: s, Retry: cfg.titlesRetry, Log: log}
		if jev != nil {
			matcher.Chooser = jev
		}
		lazy.Titles = matcher
	}
	handler, err := server.New(server.Config{
		Store:       s,
		Queue:       q,
		IngestToken: cfg.ingestToken,
		SearchToken: cfg.searchToken,
		Lease:       cfg.lease,
		Attempts:    cfg.attempts,
		Backoff:     cfg.backoff,
		Scripts:     cfg.scripts,
		Languages:   cfg.languages,
		Log:         log,
		Lazy:        lazy,
	})
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: handler, Protocols: server.Protocols(), ReadHeaderTimeout: 10 * time.Second}
	l, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return err
	}
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		log.Info("listening", "addr", l.Addr().String())
		if err := srv.Serve(l); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	})
	g.Go(func() error {
		every(ctx, maintenance, func() { maintain(ctx, q, s, cfg.retention, log) })
		return nil
	})
	if jev != nil {
		w := &classify.Worker{Store: s, Jev: jev, Concurrency: cfg.concurrency, Reserve: cfg.searchReserve, Poll: 10 * time.Second, Log: log}
		g.Go(func() error {
			if err := w.Run(ctx); !errors.Is(err, context.Canceled) {
				return err
			}
			return nil
		})
	}
	if matcher != nil {
		w := &titles.Worker{Matcher: matcher, Concurrency: cfg.titlesConcurrency, Reserve: cfg.searchReserve, Poll: 10 * time.Second, Log: log}
		if jev != nil {
			w.Budget = jev
		}
		g.Go(func() error {
			if err := w.Run(ctx); !errors.Is(err, context.Canceled) {
				return err
			}
			return nil
		})
	}
	return g.Wait()
}

// maintenance is the interval of maintain.
const maintenance = time.Minute

// maintain trims the queue and deletes the torrents not seen within
// retention.
func maintain(ctx context.Context, q *queue.Queue, s *store.Store, retention time.Duration, log *slog.Logger) {
	now := time.Now()
	t, err := q.Trim(ctx, now)
	if err != nil {
		log.ErrorContext(ctx, "trim queue", "err", err)
	} else if t != (queue.Trimmed{}) {
		log.Info("trimmed queue", "aged", t.Aged, "oversize", t.Oversize, "dead_expired", t.Expired)
	}
	n, err := s.Sweep(ctx, now.Add(-retention), sweepBatch)
	if err != nil {
		log.ErrorContext(ctx, "sweep store", "err", err)
	}
	if n > 0 {
		log.Info("swept store", "deleted", n)
	}
}

// every calls f every d until ctx is done.
func every(ctx context.Context, d time.Duration, f func()) {
	tick := time.NewTicker(d)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			f()
		}
	}
}
