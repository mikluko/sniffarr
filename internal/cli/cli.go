// Package cli runs a command: it loads the configuration, logs as JSON to
// stderr with the trace and span IDs of the context, and runs until SIGINT
// or SIGTERM.
package cli

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/mikluko/sniffarr/internal/telemetry"
)

// Config is a configuration that names the level to log at.
type Config interface {
	LogLevel() slog.Level
}

// Main exits with the status Status returns, logging at the level of the
// loaded configuration.
func Main[C Config](load func() (C, error), run func(context.Context, C, *slog.Logger) error) {
	var level slog.LevelVar
	log := slog.New(telemetry.Handler{Handler: slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: &level})})
	os.Exit(Status(leveled(load, &level), run, log))
}

// leveled returns load that also sets level to the loaded configuration's,
// leaving it as is when the configuration does not load.
func leveled[C Config](load func() (C, error), level *slog.LevelVar) func() (C, error) {
	return func() (C, error) {
		c, err := load()
		if err == nil {
			level.Set(c.LogLevel())
		}
		return c, err
	}
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
