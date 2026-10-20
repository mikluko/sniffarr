// Package sniff is the discovery component: it walks the DHT and reports the
// infohashes it reveals to the ingest API.
package sniff

import (
	"context"
	"log/slog"

	"go.uber.org/fx"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/app"
	"github.com/mikluko/sniffarr/internal/app/dht"
	sniffer "github.com/mikluko/sniffarr/internal/sniff"
)

// Config is what Module needs.
type Config struct {
	// Walkers is how many sample_infohashes queries run at once.
	Walkers int
}

// Module provides dht.Offer and runs the discoverer and the DHT walk from
// start to stop.
var Module = fx.Module("sniff",
	fx.Provide(newDiscoverer, offer),
	fx.Invoke(run),
)

func newDiscoverer(ingest sniffarrv1connect.IngestServiceClient, log *slog.Logger) *sniffer.Discoverer {
	return sniffer.NewDiscoverer(sniffer.DiscovererConfig{Ingest: ingest, Log: log})
}

func offer(d *sniffer.Discoverer) dht.Offer { return d.Offer }

func run(lc fx.Lifecycle, sd fx.Shutdowner, log *slog.Logger, cfg Config, d *sniffer.Discoverer, n *sniffer.Network) {
	app.Go(lc, sd, log, "discoverer", d.Run)
	app.Go(lc, sd, log, "dht walk", func(ctx context.Context) error {
		n.Sample(ctx, cfg.Walkers, func(h sniffer.Hash) { d.Offer(h, sniffarrv1.Source_SOURCE_SAMPLE) })
		return nil
	})
}
