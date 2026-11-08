// Package api is the API component: the ingest, search and browse services,
// the intake of the sightings stream, the Torznab indexer and the Radarr and
// Sonarr webhook over the store, with the classify and TMDB workers, the
// imports of the Radarr and Sonarr histories, the store's one-time backfills
// and maintenance, the WAL's truncation, the rollup of peer sketches, the
// tops of works, the counts of the browse service and the daily check of the
// latest release.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"go.uber.org/fx"

	"sarak.sh/mikluko/libarr/database"
	"sarak.sh/mikluko/sniffarr/internal/app"
	"sarak.sh/mikluko/sniffarr/internal/app/broker"
	"sarak.sh/mikluko/sniffarr/internal/arr"
	"sarak.sh/mikluko/sniffarr/internal/audio"
	"sarak.sh/mikluko/sniffarr/internal/classify"
	"sarak.sh/mikluko/sniffarr/internal/ingest"
	"sarak.sh/mikluko/sniffarr/internal/latest"
	"sarak.sh/mikluko/sniffarr/internal/memconn"
	"sarak.sh/mikluko/sniffarr/internal/rules"
	"sarak.sh/mikluko/sniffarr/internal/search"
	"sarak.sh/mikluko/sniffarr/internal/server"
	"sarak.sh/mikluko/sniffarr/internal/store"
	"sarak.sh/mikluko/sniffarr/internal/tmdb"
	"sarak.sh/mikluko/sniffarr/internal/version"
)

// Config is what Module needs.
type Config struct {
	// Addr is the listen address.
	Addr string
	// ClassifyURL is the classifier's System One endpoint; empty is
	// classify.DefaultURL. Another endpoint is asked outside DailyTokens.
	ClassifyURL string
	// ClassifyKey is the key sent to ClassifyURL; empty sends none.
	ClassifyKey string
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
	// RetentionJournal is how long a torrent's journal entries are kept.
	RetentionJournal time.Duration
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
	// Probe tunes the probe tasks of the ingest service.
	Probe ingest.ProbeConfig
	// Probes has the API queue and lease probes; without it none is queued.
	Probes bool
	// ReleaseCheck has the API ask the forge for the latest stable release
	// once a day and report it in BrowseService.Settings.
	ReleaseCheck bool
	// ReleaseNotice is how far behind that release a stable build is before
	// the web UI announces it.
	ReleaseNotice version.Policy
}

// ArrHistory is where a Radarr or Sonarr serves its API, URL the root of
// its v3 API; a zero one imports no history.
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
	fx.Provide(memconn.Listen, newClassifiers, newCounts, newRecorder, newChecker, newIngest),
	fx.Invoke(serve, work),
)

// maintenance is the interval of maintain.
const maintenance = time.Minute

// rollup is the interval of the peer sketches' rollup.
const rollup = time.Hour

// topInterval is how often the tops of works are recomputed, and topDelay
// how long after start the first time: the counts and the page cache come
// first, so a fresh pod answers before its minutes of sketch unions.
const (
	topInterval = 10 * time.Minute
	topDelay    = 2 * time.Minute
)

// sweepBatch is the most torrents one retention transaction deletes.
const sweepBatch = 1000

// workerPoll is how often an idle worker looks for work.
const workerPoll = 10 * time.Second

// classifiers are the layers above rules and reuse; each is nil without its
// key.
type classifiers struct {
	endpoint   endpoint
	classifier *classify.Classifier
	matcher    *tmdb.Matcher
	lazy       *search.Lazy
}

// endpoint is where the classifier is asked, and on what terms.
type endpoint struct {
	url, key    string
	dailyTokens int64
	// setting is the variable that turns the classifier on.
	setting string
}

// classifierEndpoint returns cfg's classifier endpoint, and whether the
// classifier is on: always with an endpoint other than classify.DefaultURL,
// else only with a key.
func classifierEndpoint(cfg Config) (endpoint, bool) {
	if cfg.ClassifyURL != "" && cfg.ClassifyURL != classify.DefaultURL {
		return endpoint{url: cfg.ClassifyURL, key: cfg.ClassifyKey, setting: "SNIFFARR_CLASSIFY_URL"}, true
	}
	e := endpoint{url: classify.DefaultURL, key: cfg.ClassifyKey, dailyTokens: cfg.DailyTokens, setting: "SNIFFARR_CLASSIFY_API_KEY"}
	return e, e.key != ""
}

func newClassifiers(cfg Config, s *store.Store, log *slog.Logger) *classifiers {
	e, on := classifierEndpoint(cfg)
	c := &classifiers{endpoint: e}
	if !on && cfg.TMDBKey == "" {
		log.Info("the classifier is off and TMDB_API_KEY is not set: torrents are classified by their names only")
		return c
	}
	c.lazy = &search.Lazy{Max: cfg.LazyMax, MaxTMDB: cfg.LazyTMDB, Wait: cfg.LazyWait, MaxFiles: classify.MaxFiles, Log: log}
	if !on {
		log.Info("the classifier is off: torrents are classified by their names only")
	} else {
		client := classify.New(e.key)
		client.URL = e.url
		client.Languages = cfg.Languages
		c.classifier = &classify.Classifier{Store: s, Model: client, DailyTokens: e.dailyTokens, Log: log}
		c.lazy.Classifier = c.classifier
	}
	if cfg.TMDBKey != "" {
		c.matcher = &tmdb.Matcher{Client: tmdb.NewClient(cfg.TMDBKey, cfg.TMDBRate), Store: s, Retry: cfg.TMDBRetry, Log: log}
		if c.classifier != nil {
			c.matcher.Chooser = c.classifier
		}
		c.lazy.TMDB = c.matcher
	}
	return c
}

func newCounts(cfg Config, s *store.Store, log *slog.Logger) *search.Counts {
	return &search.Counts{Store: s, Log: log, Interval: cfg.StatsInterval}
}

// releaseTimeout bounds one request for the latest release.
const releaseTimeout = 30 * time.Second

// newChecker returns the release check, nil while Config.ReleaseCheck is
// off.
func newChecker(cfg Config, log *slog.Logger) *latest.Checker {
	if !cfg.ReleaseCheck {
		return nil
	}
	return &latest.Checker{HTTP: &http.Client{Timeout: releaseTimeout}, Log: log}
}

// newIngest returns the ingest service the API serves, whose Intake takes the
// sightings stream.
func newIngest(cfg Config, s *store.Store, q *store.Tasks, log *slog.Logger) *ingest.Service {
	return &ingest.Service{Store: s, Tasks: q, Log: log, Lease: cfg.Lease, Attempts: cfg.Attempts, Backoff: cfg.Backoff,
		Scripts: cfg.Scripts, Languages: cfg.Languages, Probe: cfg.Probe, Probes: cfg.Probes}
}

func newRecorder(s *store.Store, q *store.Tasks, c *classifiers, log *slog.Logger) *arr.Recorder {
	r := &arr.Recorder{Store: s, Tasks: q, Log: log}
	if c.matcher != nil {
		r.Finder = c.matcher.Client
	}
	return r
}

func serve(lc fx.Lifecycle, sd fx.Shutdowner, log *slog.Logger, cfg Config, s *store.Store, q *store.Tasks, c *classifiers, counts *search.Counts,
	rec *arr.Recorder, mem *memconn.Listener, chk *latest.Checker, ing *ingest.Service) error {
	var releases search.LatestRelease
	if chk != nil {
		releases = chk
	}
	var titles search.TitleSearch
	var matcher search.TitleMatcher
	if c.matcher != nil {
		titles, matcher = c.matcher, c.matcher
	}
	handler, err := server.New(server.Config{
		TMDB:              titles,
		Matcher:           matcher,
		Store:             s,
		Tasks:             q,
		Ingest:            ing,
		Scripts:           cfg.Scripts,
		Languages:         cfg.Languages,
		Log:               log,
		Lazy:              c.lazy,
		TorznabToken:      cfg.TorznabToken,
		DailyTokens:       c.endpoint.dailyTokens,
		Counts:            counts,
		WebhookToken:      cfg.WebhookToken,
		Arr:               rec,
		Probe:             cfg.Probe,
		Probes:            cfg.Probes,
		Classifier:        c.classifier != nil,
		ClassifierSetting: c.endpoint.setting,
		JournalRetention:  cfg.RetentionJournal,
		Releases:          releases,
		ReleaseNotice:     cfg.ReleaseNotice,
	})
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: handler, Protocols: server.Protocols(), ReadHeaderTimeout: 10 * time.Second}
	app.Serve(lc, sd, log, "api", srv, app.TCP(cfg.Addr), app.Ready(mem))
	return nil
}

func work(lc fx.Lifecycle, sd fx.Shutdowner, log *slog.Logger, cfg Config, db *database.DB, s *store.Store, q *store.Tasks, c *classifiers, counts *search.Counts, rec *arr.Recorder, chk *latest.Checker,
	b *broker.Broker, ing *ingest.Service) {
	app.Go(lc, sd, log, "intake", func(ctx context.Context) error { return ing.Intake(ctx, b.JetStream) })
	if chk != nil {
		app.Go(lc, sd, log, "release check", chk.Run)
	}
	app.Go(lc, sd, log, "backfill", func(ctx context.Context) error {
		backfill(ctx, s, log)
		return nil
	})
	app.Go(lc, sd, log, "maintenance", func(ctx context.Context) error {
		every(ctx, maintenance, func() { maintain(ctx, q, s, cfg.Retention, cfg.RetentionIdle, cfg.RetentionJournal, log) })
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
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(topDelay):
		}
		computeTop(ctx, s, log)
		every(ctx, topInterval, func() { computeTop(ctx, s, log) })
		return nil
	})
	app.Go(lc, sd, log, "counts", func(ctx context.Context) error { return counts.Run(ctx) })
	if c.classifier != nil {
		w := &classify.Worker{Store: s, Classifier: c.classifier, Concurrency: cfg.ClassifyConcurrency, Reserve: cfg.SearchReserve, Poll: workerPoll, Log: log}
		app.Go(lc, sd, log, "classify worker", w.Run)
	}
	if c.matcher != nil {
		w := &tmdb.Worker{Matcher: c.matcher, Concurrency: cfg.TMDBConcurrency, Reserve: cfg.SearchReserve, Poll: workerPoll, Log: log}
		if c.classifier != nil {
			w.Budget = c.classifier
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

// maintain trims the queue, deletes the torrents not seen within
// retention, or within idle for those with no TMDB work, and the journal
// entries older than journal.
func maintain(ctx context.Context, q *store.Tasks, s *store.Store, retention, idle, journal time.Duration, log *slog.Logger) {
	now := time.Now()
	t, err := q.Trim(ctx, now)
	if err != nil && !errors.Is(err, context.Canceled) {
		log.ErrorContext(ctx, "trim queue", "err", err)
	} else if t != (store.Trimmed{}) {
		log.Info("trimmed queue", "aged", t.Aged, "oversize", t.Oversize, "dead_expired", t.Expired)
	}
	sw, err := s.Sweep(ctx, now.Add(-retention), now.Add(-idle), now, sweepBatch)
	if err != nil && !errors.Is(err, context.Canceled) {
		log.ErrorContext(ctx, "sweep store", "err", err)
	}
	if sw != (store.Swept{}) {
		log.Info("swept store", "idle", sw.Idle, "expired", sw.Expired)
	}
	n, err := s.SweepJournal(ctx, now.Add(-journal), sweepBatch)
	if err != nil && !errors.Is(err, context.Canceled) {
		log.ErrorContext(ctx, "sweep journal", "err", err)
	}
	if n > 0 {
		log.Debug("swept journal", "entries", n)
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
