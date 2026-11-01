// Package fetch is the fetching component: it takes tasks from the ingest
// API, fetches their metadata from peers, and returns it, and, with a seed
// directory, probes stored releases for their tracks.
package fetch

import (
	"log/slog"
	"time"

	"go.uber.org/fx"

	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/app"
	"github.com/mikluko/sniffarr/internal/sniff"
)

// Config is what Module needs.
type Config struct {
	// Workers is how many fetches run at once.
	Workers int
	// Probe takes probe tasks too, with Probers probes at once, each
	// bounded by ProbeTimeout; the network needs a seed directory for it.
	Probe        bool
	Probers      int
	ProbeTimeout time.Duration
}

// Module runs the fetcher from start to stop.
var Module = fx.Module("fetch", fx.Invoke(run))

func run(lc fx.Lifecycle, sd fx.Shutdowner, log *slog.Logger, cfg Config, ingest sniffarrv1connect.IngestServiceClient, n *sniff.Network, p *sniff.Peers) {
	f := sniff.NewFetcher(sniff.FetcherConfig{Ingest: ingest, Workers: cfg.Workers, Peers: p, Log: log}, n)
	app.Go(lc, sd, log, "fetcher", f.Run)
	if cfg.Probe {
		pr := sniff.NewProber(sniff.ProberConfig{Ingest: ingest, Workers: cfg.Probers, Timeout: cfg.ProbeTimeout, Log: log}, n)
		app.Go(lc, sd, log, "prober", pr.Run)
	}
}
