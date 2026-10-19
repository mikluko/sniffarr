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

	"github.com/mikluko/sniffarr/internal/classify"
	"github.com/mikluko/sniffarr/internal/cli"
	"github.com/mikluko/sniffarr/internal/database"
	"github.com/mikluko/sniffarr/internal/ingest"
	"github.com/mikluko/sniffarr/internal/queue"
	"github.com/mikluko/sniffarr/internal/server"
	"github.com/mikluko/sniffarr/internal/store"
	"github.com/mikluko/sniffarr/internal/telemetry"
)

type config struct {
	addr, db                 string
	ingestToken, searchToken string
	typesafeKey              string
	concurrency              int
	dailyTokens              int64
	lease, backoff           time.Duration
	attempts                 int
	queue                    queue.Options
	store                    store.Options
	retention                time.Duration
}

// defaultRetention is how long a stored torrent is kept without being seen.
const defaultRetention = 90 * 24 * time.Hour

// sweepBatch is the most torrents one retention transaction deletes.
const sweepBatch = 1000

func main() {
	cli.Main(func() (config, error) { return load(os.Getenv) }, run)
}

// load reads the configuration from getenv.
func load(getenv func(string) string) (config, error) {
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
	}
	var err error
	if c.concurrency, err = positive(getenv, "SNIFFARR_CLASSIFY_CONCURRENCY", 4); err != nil {
		return c, err
	}
	if c.dailyTokens, err = strconv.ParseInt(or(getenv("SNIFFARR_CLASSIFY_DAILY_TOKENS"), "0"), 10, 64); err != nil || c.dailyTokens < 0 {
		return c, errors.New("SNIFFARR_CLASSIFY_DAILY_TOKENS: want a non-negative integer")
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
	if c.store.MaxFiles, err = positive(getenv, "SNIFFARR_STORE_MAX_FILES", store.DefaultMaxFiles); err != nil {
		return c, err
	}
	if c.retention, err = duration(getenv, "SNIFFARR_RETENTION", defaultRetention); err != nil {
		return c, err
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

	handler, err := server.New(server.Config{
		Store:       s,
		Queue:       q,
		IngestToken: cfg.ingestToken,
		SearchToken: cfg.searchToken,
		Lease:       cfg.lease,
		Attempts:    cfg.attempts,
		Backoff:     cfg.backoff,
		Log:         log,
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
	if cfg.typesafeKey == "" {
		log.Warn("TYPESAFE_API_KEY is not set; torrents stay unclassified")
	} else {
		w := &classify.Worker{
			Store:       s,
			Classifier:  classify.New(cfg.typesafeKey),
			Concurrency: cfg.concurrency,
			DailyTokens: cfg.dailyTokens,
			Poll:        10 * time.Second,
			Log:         log,
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
