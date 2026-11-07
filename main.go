// Command sniffarr is a private classification database of BitTorrent
// torrents discovered on the DHT. Its components are switched on by flags:
// -api serves the store, -ui the web UI, -sniff walks the DHT and -fetch
// fetches metadata; the clients reach an API in the same process over
// memconn and one elsewhere at SNIFFARR_URL. -wait-api, alone, exits once the
// API at SNIFFARR_URL serves this release. It runs until SIGINT or SIGTERM
// and is configured by flags and environment variables; see the README.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"go.uber.org/fx"

	"github.com/mikluko/sniffarr/internal/app"
	"github.com/mikluko/sniffarr/internal/app/api"
	"github.com/mikluko/sniffarr/internal/app/client"
	"github.com/mikluko/sniffarr/internal/app/db"
	"github.com/mikluko/sniffarr/internal/app/dht"
	"github.com/mikluko/sniffarr/internal/app/fetch"
	"github.com/mikluko/sniffarr/internal/app/peers"
	"github.com/mikluko/sniffarr/internal/app/sniff"
	"github.com/mikluko/sniffarr/internal/app/ui"
	"github.com/mikluko/sniffarr/internal/cli"
	"github.com/mikluko/sniffarr/internal/version"
	"github.com/mikluko/sniffarr/internal/waitapi"
)

func main() {
	cli.Main(func() (config, error) { return load(os.Args[1:], os.LookupEnv, os.Stderr) }, run)
}

// options is the fx graph of the components cfg switches on.
func options(cfg config, log *slog.Logger) []fx.Option {
	opts := []fx.Option{fx.Supply(log), app.Logging, app.Telemetry}
	if cfg.on.api {
		opts = append(opts, fx.Supply(cfg.db, cfg.api), db.Module, api.Module)
	}
	if cfg.on.clients() {
		opts = append(opts, fx.Supply(cfg.client), client.Module)
	}
	if cfg.on.ui {
		opts = append(opts, fx.Supply(cfg.ui), ui.Module)
	}
	if cfg.on.dht() {
		opts = append(opts, fx.Supply(cfg.dht), dht.Module, peers.Module)
	}
	if cfg.on.sniff {
		opts = append(opts, fx.Supply(cfg.sniff), sniff.Module)
	}
	if cfg.on.fetch {
		opts = append(opts, fx.Supply(cfg.fetch), fetch.Module)
	}
	return opts
}

// run starts the components and stops them when ctx ends or one of them
// fails.
func run(ctx context.Context, cfg config, log *slog.Logger) error {
	build := version.Get()
	log.Info("sniffarr", "version", build.Version, "commit", build.Commit, "built", build.Built)
	if cfg.waitAPI {
		return waitapi.Run(ctx, &http.Client{Timeout: waitapi.Interval}, cfg.client.URL, build.Server(), waitapi.Interval, log)
	}
	a := fx.New(options(cfg, log)...)
	if err := a.Err(); err != nil {
		return err
	}
	start, cancel := context.WithTimeout(ctx, a.StartTimeout())
	defer cancel()
	if err := a.Start(start); err != nil {
		return err
	}
	var failed error
	select {
	case <-ctx.Done():
	case sig := <-a.Wait():
		if sig.ExitCode != 0 {
			failed = fmt.Errorf("a component failed: exit code %d", sig.ExitCode)
		}
	}
	stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.StopTimeout())
	defer cancel()
	if err := a.Stop(stop); err != nil {
		return err
	}
	return failed
}
