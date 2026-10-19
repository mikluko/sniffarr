// Package cli runs a command: it loads the configuration, logs as JSON to
// stderr, and runs until SIGINT or SIGTERM.
package cli

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

// Main exits with the status Status returns.
func Main[C any](load func() (C, error), run func(context.Context, C, *slog.Logger) error) {
	os.Exit(Status(load, run, slog.New(slog.NewJSONHandler(os.Stderr, nil))))
}

// Status loads the configuration and runs it until SIGINT or SIGTERM. It
// returns 0 after -h, 2 when the configuration does not load, 1 when run
// fails.
func Status[C any](load func() (C, error), run func(context.Context, C, *slog.Logger) error, log *slog.Logger) int {
	cfg, err := load()
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		log.Error("config", "err", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, log); err != nil {
		log.Error("exit", "err", err)
		return 1
	}
	return 0
}
