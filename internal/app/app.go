// Package app is what every sniffarr component's fx module shares: the
// process logger, telemetry, and the lifecycle shape of a task that runs from
// start to stop.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/mikluko/sniffarr/internal/telemetry"
)

// ServiceName is the telemetry service name; OTEL_SERVICE_NAME overrides it.
const ServiceName = "sniffarr"

// Logging routes fx's own events to the process logger at Debug and its
// errors at Error.
var Logging = fx.WithLogger(func(log *slog.Logger) fxevent.Logger {
	l := &fxevent.SlogLogger{Logger: log}
	l.UseLogLevel(slog.LevelDebug)
	l.UseErrorLevel(slog.LevelError)
	return l
})

// Telemetry exports traces and metrics from start to stop, by
// telemetry.Setup and telemetry.Flush.
var Telemetry = fx.Invoke(setupTelemetry)

func setupTelemetry(lc fx.Lifecycle, log *slog.Logger) {
	var shutdown func(context.Context) error
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			var err error
			shutdown, err = telemetry.Setup(context.WithoutCancel(ctx), ServiceName, log)
			if err != nil {
				return fmt.Errorf("telemetry: %w", err)
			}
			return nil
		},
		OnStop: func(ctx context.Context) error {
			telemetry.Flush(ctx, shutdown, log)
			return nil
		},
	})
}

// Go runs fn in a goroutine of its own from start to stop: its context ends
// at stop, and an error other than context.Canceled returned before then
// shuts the process down with exit code 1.
func Go(lc fx.Lifecycle, sd fx.Shutdowner, log *slog.Logger, name string, fn func(context.Context) error) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				defer close(done)
				err := fn(ctx)
				if err == nil || errors.Is(err, context.Canceled) || ctx.Err() != nil {
					return
				}
				log.Error(name, "err", err)
				_ = sd.Shutdown(fx.ExitCode(1))
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			cancel()
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
}

// Serve opens each listener at start and serves srv on it until stop; a
// listener that does not open fails the start, and a Serve that fails
// shuts the process down with exit code 1.
func Serve(lc fx.Lifecycle, sd fx.Shutdowner, log *slog.Logger, name string, srv *http.Server, listen ...func() (net.Listener, error)) {
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			for _, open := range listen {
				l, err := open()
				if err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
				log.Info(name, "addr", l.Addr().String())
				go func() {
					if err := srv.Serve(l); !errors.Is(err, http.ErrServerClosed) {
						log.Error(name, "err", err)
						_ = sd.Shutdown(fx.ExitCode(1))
					}
				}()
			}
			return nil
		},
		OnStop: srv.Shutdown,
	})
}

// TCP returns a listen function for a TCP address.
func TCP(addr string) func() (net.Listener, error) {
	return func() (net.Listener, error) { return net.Listen("tcp", addr) }
}

// Ready returns a listen function for an open listener.
func Ready(l net.Listener) func() (net.Listener, error) {
	return func() (net.Listener, error) { return l, nil }
}
