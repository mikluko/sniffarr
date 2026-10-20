// Package fetch is the fetching component: it takes tasks from the ingest
// API, fetches their metadata from peers, and returns it.
package fetch

import (
	"log/slog"

	"go.uber.org/fx"

	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/app"
	"github.com/mikluko/sniffarr/internal/sniff"
)

// Config is what Module needs.
type Config struct {
	// Workers is how many fetches run at once.
	Workers int
}

// Module runs the fetcher from start to stop.
var Module = fx.Module("fetch", fx.Invoke(run))

func run(lc fx.Lifecycle, sd fx.Shutdowner, log *slog.Logger, cfg Config, ingest sniffarrv1connect.IngestServiceClient, n *sniff.Network) {
	f := sniff.NewFetcher(sniff.FetcherConfig{Ingest: ingest, Workers: cfg.Workers, Log: log}, n)
	app.Go(lc, sd, log, "fetcher", f.Run)
}
