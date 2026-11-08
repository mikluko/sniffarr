// Package sniff is the discovery component: it walks the DHT and reports the
// infohashes it reveals, to the sightings stream when there is a NATS
// connection, else to the ingest API's Sight.
package sniff

import (
	"context"
	"log/slog"

	"go.uber.org/fx"

	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"sarak.sh/mikluko/sniffarr/internal/app"
	"sarak.sh/mikluko/sniffarr/internal/app/dht"
	"sarak.sh/mikluko/sniffarr/internal/app/natsconn"
	sniffer "sarak.sh/mikluko/sniffarr/internal/sniff"
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

func newDiscoverer(ingest sniffarrv1connect.IngestServiceClient, p *sniffer.Peers, log *slog.Logger) *sniffer.Discoverer {
	return sniffer.NewDiscoverer(sniffer.DiscovererConfig{Ingest: ingest, Stored: p.Admit, Log: log})
}

func offer(d *sniffer.Discoverer) dht.Offer { return d.Offer }

func run(lc fx.Lifecycle, sd fx.Shutdowner, log *slog.Logger, cfg Config, d *sniffer.Discoverer, n *sniffer.Network, nc *natsconn.Conn) {
	app.Go(lc, sd, log, "discoverer", func(ctx context.Context) error {
		if c := nc.NATS(); c != nil {
			return d.Publish(ctx, c)
		}
		return d.Run(ctx)
	})
	app.Go(lc, sd, log, "dht walk", func(ctx context.Context) error {
		n.Sample(ctx, cfg.Walkers, func(h sniffer.Hash) { d.Offer(h, sniffarrv1.Source_SOURCE_SAMPLE) })
		return nil
	})
}
