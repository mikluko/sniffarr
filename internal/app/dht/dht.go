// Package dht provides the BitTorrent client and DHT node the sniffer and
// the fetcher share.
package dht

import (
	"context"
	"fmt"
	"log/slog"

	"go.uber.org/fx"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/app"
	"github.com/mikluko/sniffarr/internal/sniff"
)

// Config is what Module needs.
type Config struct {
	// Network tunes the node; Passive must be set when nothing discovers.
	Network sniff.NetworkConfig
}

// Offer takes an infohash announced to the node, with its source.
type Offer func(sniff.Hash, sniffarrv1.Source)

// Module provides *sniff.Network, bootstrapped in the background from start
// and closed at stop.
var Module = fx.Module("dht", fx.Provide(start))

// nodeParams takes the discoverer's Offer when there is one.
type nodeParams struct {
	fx.In
	Lifecycle  fx.Lifecycle
	Shutdowner fx.Shutdowner
	Log        *slog.Logger
	Config     Config
	// Offer receives the infohashes announced to the node; nil when nothing
	// discovers, and the node must then be passive.
	Offer Offer `optional:"true"`
	// Peers counts the peers that announce to the node; nil counts none.
	Peers *sniff.Peers `optional:"true"`
}

func start(p nodeParams) (*sniff.Network, error) {
	var sink sniff.PeerSink
	if p.Peers != nil {
		sink = p.Peers
	}
	n, err := sniff.Start(p.Config.Network, p.Offer, sink)
	if err != nil {
		return nil, fmt.Errorf("dht: %w", err)
	}
	p.Lifecycle.Append(fx.StopHook(n.Close))
	app.Go(p.Lifecycle, p.Shutdowner, p.Log, "dht bootstrap", func(ctx context.Context) error {
		if err := n.Bootstrap(ctx); err != nil && ctx.Err() == nil {
			p.Log.Warn("bootstrap", "err", err)
		}
		if ctx.Err() == nil {
			p.Log.Info("dht up", "port", n.Port(), "nodes", n.Nodes(), "passive", p.Config.Network.Passive)
		}
		return nil
	})
	return n, nil
}
