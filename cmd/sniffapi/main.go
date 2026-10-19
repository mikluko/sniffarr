// Command sniffapi serves the ingest and search APIs over a SQLite database
// and classifies stored torrents with TypeSafe's Jev. It is configured by
// environment variables; see the README.
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
	"github.com/mikluko/sniffarr/internal/server"
	"github.com/mikluko/sniffarr/internal/store"
)

type config struct {
	addr, db                 string
	ingestToken, searchToken string
	typesafeKey              string
	concurrency              int
	dailyTokens              int64
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
	n, err := strconv.Atoi(or(getenv("SNIFFARR_CLASSIFY_CONCURRENCY"), "4"))
	if err != nil || n < 1 {
		return c, errors.New("SNIFFARR_CLASSIFY_CONCURRENCY: want a positive integer")
	}
	c.concurrency = n
	if c.dailyTokens, err = strconv.ParseInt(or(getenv("SNIFFARR_CLASSIFY_DAILY_TOKENS"), "0"), 10, 64); err != nil || c.dailyTokens < 0 {
		return c, errors.New("SNIFFARR_CLASSIFY_DAILY_TOKENS: want a non-negative integer")
	}
	return c, nil
}

func or(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

func run(ctx context.Context, cfg config, log *slog.Logger) error {
	s, err := store.Open(ctx, cfg.db)
	if err != nil {
		return fmt.Errorf("open %s: %w", cfg.db, err)
	}
	defer s.Close()

	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           server.New(server.Config{Store: s, IngestToken: cfg.ingestToken, SearchToken: cfg.searchToken, Log: log}),
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
