// Package api is the API component: the ingest, search and browse services
// and the Torznab indexer over the store, with the classify and titles
// workers and the store's maintenance.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"go.uber.org/fx"

	"github.com/mikluko/sniffarr/internal/app"
	"github.com/mikluko/sniffarr/internal/audio"
	"github.com/mikluko/sniffarr/internal/classify"
	"github.com/mikluko/sniffarr/internal/memconn"
	"github.com/mikluko/sniffarr/internal/queue"
	"github.com/mikluko/sniffarr/internal/rules"
	"github.com/mikluko/sniffarr/internal/search"
	"github.com/mikluko/sniffarr/internal/server"
	"github.com/mikluko/sniffarr/internal/store"
	"github.com/mikluko/sniffarr/internal/titles"
)

// Config is what Module needs; the tokens must be set.
type Config struct {
	// Addr is the listen address.
	Addr string
	// IngestToken is the bearer token of IngestService.
	IngestToken string
	// SearchToken is the bearer token of SearchService and BrowseService.
	SearchToken string
	// TypesafeKey is the TypeSafe API key; empty leaves Jev out.
	TypesafeKey string
	// TMDBKey is the TMDB API key or read access token; empty matches no
	// titles.
	TMDBKey string
	// TorznabToken is the Torznab API key; empty serves no Torznab.
	TorznabToken string
	// ClassifyConcurrency is how many background classifications run at once.
	ClassifyConcurrency int
	// DailyTokens is the classifier's tokens per UTC day; 0 is no limit.
	DailyTokens int64
	// SearchReserve is the percentage of DailyTokens the background
	// classifier leaves for searches.
	SearchReserve int
	// Lease, Attempts and Backoff are those of ingest.Service.
	Lease    time.Duration
	Attempts int
	Backoff  time.Duration
	// Retention is how long a stored torrent is kept without being seen.
	Retention time.Duration
	// LazyMax, LazyWait and LazyTitles are those of search.Lazy.
	LazyMax    int
	LazyWait   time.Duration
	LazyTitles int
	// TitlesConcurrency, TitlesRate and TitlesRetry tune the title matcher.
	TitlesConcurrency int
	TitlesRate        int
	TitlesRetry       time.Duration
	// Scripts are the writing systems a torrent's name may have letters of.
	Scripts rules.Scripts
	// Languages are the audio languages every stored torrent gets an entry
	// for.
	Languages []audio.Language
}

// Module provides *memconn.Listener, on which the API is served to the
// process's own clients, and serves it on Config.Addr from start to stop
// with its workers.
var Module = fx.Module("api",
	fx.Provide(memconn.Listen, newClassifiers),
	fx.Invoke(serve, work),
)

// maintenance is the interval of maintain.
const maintenance = time.Minute

// sweepBatch is the most torrents one retention transaction deletes.
const sweepBatch = 1000

// workerPoll is how often an idle worker looks for work.
const workerPoll = 10 * time.Second

// classifiers are the layers above rules and reuse; each is nil without its
// key.
type classifiers struct {
	jev     *classify.Jev
	matcher *titles.Matcher
	lazy    *search.Lazy
}

func newClassifiers(cfg Config, s *store.Store, log *slog.Logger) *classifiers {
	c := &classifiers{}
	if cfg.TypesafeKey == "" && cfg.TMDBKey == "" {
		log.Info("TYPESAFE_API_KEY and TMDB_API_KEY are not set: torrents are classified by their names only")
		return c
	}
	c.lazy = &search.Lazy{Max: cfg.LazyMax, MaxTitles: cfg.LazyTitles, Wait: cfg.LazyWait, MaxFiles: classify.MaxFiles, Log: log}
	if cfg.TypesafeKey == "" {
		log.Info("TYPESAFE_API_KEY is not set: torrents are classified by their names only")
	} else {
		client := classify.New(cfg.TypesafeKey)
		client.Languages = cfg.Languages
		c.jev = &classify.Jev{Store: s, Classifier: client, DailyTokens: cfg.DailyTokens, Log: log}
		c.lazy.Classifier = c.jev
	}
	if cfg.TMDBKey != "" {
		c.matcher = &titles.Matcher{TMDB: titles.NewTMDB(cfg.TMDBKey, cfg.TitlesRate), Store: s, Retry: cfg.TitlesRetry, Log: log}
		if c.jev != nil {
			c.matcher.Chooser = c.jev
		}
		c.lazy.Titles = c.matcher
	}
	return c
}

func serve(lc fx.Lifecycle, sd fx.Shutdowner, log *slog.Logger, cfg Config, s *store.Store, q *queue.Queue, c *classifiers, mem *memconn.Listener) error {
	handler, err := server.New(server.Config{
		Store:        s,
		Queue:        q,
		IngestToken:  cfg.IngestToken,
		SearchToken:  cfg.SearchToken,
		Lease:        cfg.Lease,
		Attempts:     cfg.Attempts,
		Backoff:      cfg.Backoff,
		Scripts:      cfg.Scripts,
		Languages:    cfg.Languages,
		Log:          log,
		Lazy:         c.lazy,
		TorznabToken: cfg.TorznabToken,
		DailyTokens:  cfg.DailyTokens,
	})
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: handler, Protocols: server.Protocols(), ReadHeaderTimeout: 10 * time.Second}
	app.Serve(lc, sd, log, "api", srv, app.TCP(cfg.Addr), app.Ready(mem))
	return nil
}

func work(lc fx.Lifecycle, sd fx.Shutdowner, log *slog.Logger, cfg Config, s *store.Store, q *queue.Queue, c *classifiers) {
	app.Go(lc, sd, log, "maintenance", func(ctx context.Context) error {
		every(ctx, maintenance, func() { maintain(ctx, q, s, cfg.Retention, log) })
		return nil
	})
	if c.jev != nil {
		w := &classify.Worker{Store: s, Jev: c.jev, Concurrency: cfg.ClassifyConcurrency, Reserve: cfg.SearchReserve, Poll: workerPoll, Log: log}
		app.Go(lc, sd, log, "classify worker", w.Run)
	}
	if c.matcher != nil {
		w := &titles.Worker{Matcher: c.matcher, Concurrency: cfg.TitlesConcurrency, Reserve: cfg.SearchReserve, Poll: workerPoll, Log: log}
		if c.jev != nil {
			w.Budget = c.jev
		}
		app.Go(lc, sd, log, "titles worker", w.Run)
	}
}

// maintain trims the queue and deletes the torrents not seen within
// retention.
func maintain(ctx context.Context, q *queue.Queue, s *store.Store, retention time.Duration, log *slog.Logger) {
	now := time.Now()
	t, err := q.Trim(ctx, now)
	if err != nil && !errors.Is(err, context.Canceled) {
		log.ErrorContext(ctx, "trim queue", "err", err)
	} else if t != (queue.Trimmed{}) {
		log.Info("trimmed queue", "aged", t.Aged, "oversize", t.Oversize, "dead_expired", t.Expired)
	}
	n, err := s.Sweep(ctx, now.Add(-retention), sweepBatch)
	if err != nil && !errors.Is(err, context.Canceled) {
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
