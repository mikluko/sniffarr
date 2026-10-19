// Command sniffapi serves the ingest work queue and the search API over a
// SQLite database and classifies stored torrents with TypeSafe's Jev. It is
// configured by environment variables; see the README.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
)

type config struct {
	addr, db                 string
	ingestToken, searchToken string
	typesafeKey              string
	concurrency              int
	dailyTokens              int64
	lease, backoff           time.Duration
	attempts                 int
}

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
	db, err := database.Open(ctx, cfg.db)
	if err != nil {
		return fmt.Errorf("open %s: %w", cfg.db, err)
	}
	defer db.Close()
	s, err := store.New(ctx, db)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	q, err := queue.New(ctx, db)
	if err != nil {
		return fmt.Errorf("queue: %w", err)
	}
	if err := q.Reset(ctx); err != nil {
		return fmt.Errorf("reset leases: %w", err)
	}

	srv := &http.Server{
		Addr: cfg.addr,
		Handler: server.New(server.Config{
			Store:       s,
			Queue:       q,
			IngestToken: cfg.ingestToken,
			SearchToken: cfg.searchToken,
			Lease:       cfg.lease,
			Attempts:    cfg.attempts,
			Backoff:     cfg.backoff,
			Log:         log,
		}),
		Protocols:         server.Protocols(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		log.Info("listening", "addr", cfg.addr)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
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
