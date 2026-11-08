// Package dht provides the BitTorrent client and DHT node the sniffer and
// the fetcher share.
package dht

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os"

	"go.uber.org/fx"

	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"sarak.sh/mikluko/sniffarr/internal/app"
	"sarak.sh/mikluko/sniffarr/internal/sniff"
	"sarak.sh/mikluko/sniffarr/internal/stun"
)

// Config is what Module needs.
type Config struct {
	// Network tunes the node; Passive must be set when nothing discovers.
	Network sniff.NetworkConfig
	// STUN is the server a node that is not passive asks for its public
	// address before it starts, for the BEP 42 prefix of its ID; empty asks
	// none and the ID is used as stored.
	STUN string
	// TempSeed has the node seed from a fresh directory from os.MkdirTemp,
	// removed at stop, in place of Network.Seed.Dir.
	TempSeed bool
}

// Offer takes an infohash announced to the node, with its source.
type Offer func(sniff.Hash, sniffarrv1.Source)

// Module provides *sniff.Network, started and bootstrapped in the
// background from start and closed at stop. A node that is not passive
// starts once the ingest API has given it its node ID.
var Module = fx.Module("dht", fx.Provide(start))

// nodeParams takes the discoverer's Offer when there is one.
type nodeParams struct {
	fx.In
	Lifecycle  fx.Lifecycle
	Shutdowner fx.Shutdowner
	Log        *slog.Logger
	Config     Config
	Ingest     sniffarrv1connect.IngestServiceClient
	// Offer receives the infohashes announced to the node; nil when nothing
	// discovers, and the node must then be passive.
	Offer Offer `optional:"true"`
	// Peers counts the peers that announce stored torrents to the node; nil
	// counts none.
	Peers *sniff.Peers `optional:"true"`
}

func start(p nodeParams) (*sniff.Network, error) {
	var sink sniff.PeerSink
	if p.Peers != nil {
		sink = p.Peers
	}
	cfg, err := network(p)
	if err != nil {
		return nil, err
	}
	n := sniff.New(cfg, p.Offer, sink)
	p.Lifecycle.Append(fx.StopHook(n.Close))
	app.Go(p.Lifecycle, p.Shutdowner, p.Log, "dht", func(ctx context.Context) error { return boot(ctx, p, n) })
	return n, nil
}

// boot starts n, under the node ID the ingest API gives a node that is not
// passive, and bootstraps it.
func boot(ctx context.Context, p nodeParams, n *sniff.Network) error {
	var (
		id       [20]byte
		publicIP netip.Addr
	)
	if !p.Config.Network.Passive {
		var err error
		if id, err = sniff.ObtainNodeID(ctx, p.Ingest, p.Config.Network.Port, sniff.DefaultBackoff, p.Log); err != nil {
			return err
		}
		if p.Config.STUN != "" {
			if publicIP, err = stun.Addr(ctx, p.Config.STUN); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				p.Log.Warn("public address unknown, the node ID is used as stored", "err", err)
			} else {
				p.Log.Info("public address", "addr", publicIP)
			}
		}
	}
	if err := n.Start(id, publicIP); err != nil {
		return fmt.Errorf("dht: %w", err)
	}
	if err := n.Bootstrap(ctx); err != nil && ctx.Err() == nil {
		p.Log.Warn("bootstrap", "err", err)
	}
	if ctx.Err() == nil {
		p.Log.Info("dht up", "port", n.Port(), "nodes", n.Nodes(), "passive", p.Config.Network.Passive)
	}
	return nil
}

// network is the node's configuration, its seeder logging to p.Log and
// reporting to p.Ingest unless it names others.
func network(p nodeParams) (sniff.NetworkConfig, error) {
	cfg := p.Config.Network
	if p.Config.TempSeed {
		dir, err := tempSeed(p.Lifecycle)
		if err != nil {
			return cfg, err
		}
		cfg.Seed.Dir = dir
	}
	if cfg.Seed.Log == nil {
		cfg.Seed.Log = p.Log
	}
	if cfg.Seed.Ingest == nil {
		cfg.Seed.Ingest = p.Ingest
	}
	return cfg, nil
}

// tempSeed returns a fresh directory from os.MkdirTemp that lc removes at
// stop, after every stop hook appended later.
func tempSeed(lc fx.Lifecycle) (string, error) {
	dir, err := os.MkdirTemp("", "sniffarr-probe-*")
	if err != nil {
		return "", fmt.Errorf("probe directory: %w", err)
	}
	lc.Append(fx.StopHook(func() error { return os.RemoveAll(dir) }))
	return dir, nil
}
