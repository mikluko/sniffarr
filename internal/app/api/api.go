// Package api is the API component: the ingest, search and browse services,
// the Torznab indexer and the Radarr and Sonarr webhook over the store,
// with the classify and TMDB workers, the imports of the Radarr and Sonarr
// histories, the store's one-time backfills and maintenance, the WAL's
// truncation, the rollup of peer sketches, the tops of works and the counts
// of the browse service.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"go.uber.org/fx"

	"github.com/mikluko/sniffarr/internal/app"
	"github.com/mikluko/sniffarr/internal/arr"
	"github.com/mikluko/sniffarr/internal/audio"
	"github.com/mikluko/sniffarr/internal/classify"
	"github.com/mikluko/sniffarr/internal/database"
	"github.com/mikluko/sniffarr/internal/memconn"
	"github.com/mikluko/sniffarr/internal/rules"
	"github.com/mikluko/sniffarr/internal/search"
	"github.com/mikluko/sniffarr/internal/server"
	"github.com/mikluko/sniffarr/internal/store"
	"github.com/mikluko/sniffarr/internal/tmdb"
)

// Config is what Module needs.
type Config struct {
	// Addr is the listen address.
	Addr string
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
	// RetentionIdle is Retention for a torrent with no TMDB work.
	RetentionIdle time.Duration
	// LazyMax, LazyWait and LazyTMDB are those of search.Lazy.
	LazyMax  int
	LazyWait time.Duration
	LazyTMDB int
	// TMDBConcurrency, TMDBRate and TMDBRetry tune the TMDB matcher.
	TMDBConcurrency int
	TMDBRate        int
	TMDBRetry       time.Duration
	// Scripts are the writing systems a torrent's name may have letters of.
	Scripts rules.Scripts
	// Languages are the audio languages every stored torrent gets an entry
	// for.
	Languages []audio.Language
	// StatsInterval is how often the counts BrowseService.Stats and Home
	// serve are recomputed; positive.
	StatsInterval time.Duration
	// WebhookToken is the password of the Radarr and Sonarr webhook; empty
	// serves no webhook.
	WebhookToken string
	// Radarr and Sonarr are the apps whose grab history is imported.
	Radarr ArrHistory
	Sonarr ArrHistory
}

// ArrHistory is where a Radarr or Sonarr serves its API; a zero one imports
// no history.
type ArrHistory struct {
	URL    string
	APIKey string
}

// arrTimeout bounds one request to a Radarr or Sonarr.
const arrTimeout = time.Minute

// Module provides *memconn.Listener, on which the API is served to the
// process's own clients, and serves it on Config.Addr from start to stop
// with its workers.
var Module = fx.Module("api",
	fx.Provide(memconn.Listen, newClassifiers, newCounts, newRecorder),
	fx.Invoke(serve, work),
)

// maintenance is the interval of maintain.
const maintenance = time.Minute

// rollup is the interval of the peer sketches' rollup.
const rollup = time.Hour

// topInterval is how often the tops of works are recomputed.
const topInterval = 10 * time.Minute

// sweepBatch is the most torrents one retention transaction deletes.
const sweepBatch = 1000

// workerPoll is how often an idle worker looks for work.
const workerPoll = 10 * time.Second

// classifiers are the layers above rules and reuse; each is nil without its
// key.
type classifiers struct {
	jev     *classify.Jev
	matcher *tmdb.Matcher
	lazy    *search.Lazy
}

func newClassifiers(cfg Config, s *store.Store, log *slog.Logger) *classifiers {
	c := &classifiers{}
	if cfg.TypesafeKey == "" && cfg.TMDBKey == "" {
		log.Info("TYPESAFE_API_KEY and TMDB_API_KEY are not set: torrents are classified by their names only")
		return c
	}
	c.lazy = &search.Lazy{Max: cfg.LazyMax, MaxTMDB: cfg.LazyTMDB, Wait: cfg.LazyWait, MaxFiles: classify.MaxFiles, Log: log}
	if cfg.TypesafeKey == "" {
		log.Info("TYPESAFE_API_KEY is not set: torrents are classified by their names only")
	} else {
		client := classify.New(cfg.TypesafeKey)
		client.Languages = cfg.Languages
		c.jev = &classify.Jev{Store: s, Classifier: client, DailyTokens: cfg.DailyTokens, Log: log}
		c.lazy.Classifier = c.jev
	}
	if cfg.TMDBKey != "" {
		c.matcher = &tmdb.Matcher{Client: tmdb.NewClient(cfg.TMDBKey, cfg.TMDBRate), Store: s, Retry: cfg.TMDBRetry, Log: log}
		if c.jev != nil {
			c.matcher.Chooser = c.jev
		}
		c.lazy.TMDB = c.matcher
	}
	return c
}

func newCounts(s *store.Store, log *slog.Logger) *search.Counts {
	return &search.Counts{Store: s, Log: log}
}

func newRecorder(s *store.Store, q *store.Queue, c *classifiers, log *slog.Logger) *arr.Recorder {
	r := &arr.Recorder{Store: s, Queue: q, Log: log}
	if c.matcher != nil {
		r.Finder = c.matcher.Client
	}
	return r
}

func serve(lc fx.Lifecycle, sd fx.Shutdowner, log *slog.Logger, cfg Config, s *store.Store, q *store.Queue, c *classifiers, counts *search.Counts,
	rec *arr.Recorder, mem *memconn.Listener) error {
	var titles search.TitleSearch
	if c.matcher != nil {
		titles = c.matcher
	}
	handler, err := server.New(server.Config{
		TMDB:         titles,
		Store:        s,
		Queue:        q,
		Lease:        cfg.Lease,
		Attempts:     cfg.Attempts,
		Backoff:      cfg.Backoff,
		Scripts:      cfg.Scripts,
		Languages:    cfg.Languages,
		Log:          log,
		Lazy:         c.lazy,
		TorznabToken: cfg.TorznabToken,
		DailyTokens:  cfg.DailyTokens,
		Counts:       counts,
		WebhookToken: cfg.WebhookToken,
		Arr:          rec,
	})
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: handler, Protocols: server.Protocols(), ReadHeaderTimeout: 10 * time.Second}
	app.Serve(lc, sd, log, "api", srv, app.TCP(cfg.Addr), app.Ready(mem))
	return nil
}

func work(lc fx.Lifecycle, sd fx.Shutdowner, log *slog.Logger, cfg Config, db *database.DB, s *store.Store, q *store.Queue, c *classifiers, counts *search.Counts, rec *arr.Recorder) {
	app.Go(lc, sd, log, "backfill", func(ctx context.Context) error {
		backfill(ctx, s, log)
		return nil
	})
	app.Go(lc, sd, log, "maintenance", func(ctx context.Context) error {
		every(ctx, maintenance, func() { maintain(ctx, q, s, cfg.Retention, cfg.RetentionIdle, log) })
		return nil
	})
	app.Go(lc, sd, log, "wal", func(ctx context.Context) error {
		every(ctx, maintenance, func() { truncateWAL(ctx, db, walLimit, log) })
		return nil
	})
	app.Go(lc, sd, log, "peers rollup", func(ctx context.Context) error {
		every(ctx, rollup, func() { rollupPeers(ctx, s, log) })
		return nil
	})
	app.Go(lc, sd, log, "top", func(ctx context.Context) error {
		computeTop(ctx, s, log)
		every(ctx, topInterval, func() { computeTop(ctx, s, log) })
		return nil
	})
	app.Go(lc, sd, log, "counts", func(ctx context.Context) error { return counts.Run(ctx, cfg.StatsInterval) })
	if c.jev != nil {
		w := &classify.Worker{Store: s, Jev: c.jev, Concurrency: cfg.ClassifyConcurrency, Reserve: cfg.SearchReserve, Poll: workerPoll, Log: log}
		app.Go(lc, sd, log, "classify worker", w.Run)
	}
	if c.matcher != nil {
		w := &tmdb.Worker{Matcher: c.matcher, Concurrency: cfg.TMDBConcurrency, Reserve: cfg.SearchReserve, Poll: workerPoll, Log: log}
		if c.jev != nil {
			w.Budget = c.jev
		}
		app.Go(lc, sd, log, "tmdb worker", w.Run)
	}
	for name, h := range map[string]ArrHistory{store.AppRadarr: cfg.Radarr, store.AppSonarr: cfg.Sonarr} {
		if h.URL == "" {
			continue
		}
		hist := &arr.History{App: name, URL: h.URL, APIKey: h.APIKey, HTTP: &http.Client{Timeout: arrTimeout}, Recorder: rec, Log: log}
		app.Go(lc, sd, log, name+" history", hist.Run)
	}
}

// maintain trims the queue and deletes the torrents not seen within
// retention, or within idle for those with no TMDB work.
func maintain(ctx context.Context, q *store.Queue, s *store.Store, retention, idle time.Duration, log *slog.Logger) {
	now := time.Now()
	t, err := q.Trim(ctx, now)
	if err != nil && !errors.Is(err, context.Canceled) {
		log.ErrorContext(ctx, "trim queue", "err", err)
	} else if t != (store.Trimmed{}) {
		log.Info("trimmed queue", "aged", t.Aged, "oversize", t.Oversize, "dead_expired", t.Expired)
	}
	sw, err := s.Sweep(ctx, now.Add(-retention), now.Add(-idle), sweepBatch)
	if err != nil && !errors.Is(err, context.Canceled) {
		log.ErrorContext(ctx, "sweep store", "err", err)
	}
	if sw != (store.Swept{}) {
		log.Info("swept store", "idle", sw.Idle, "expired", sw.Expired)
	}
}

// rollupPeers rolls the peer sketches up and logs what it did.
func rollupPeers(ctx context.Context, s *store.Store, log *slog.Logger) {
	r, err := s.RollupPeers(ctx, time.Now())
	if err != nil && !errors.Is(err, context.Canceled) {
		log.ErrorContext(ctx, "rollup peers", "err", err)
	} else if r != (store.Rolled{}) {
		log.Info("rolled up peers", "hours", r.Hours, "days", r.Days)
	}
}

// computeTop recomputes the tops of works.
func computeTop(ctx context.Context, s *store.Store, log *slog.Logger) {
	start := time.Now()
	if err := s.ComputeTop(ctx, start); err != nil && !errors.Is(err, context.Canceled) {
		log.ErrorContext(ctx, "compute top", "err", err)
		return
	}
	log.Debug("computed top", "took", time.Since(start))
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
