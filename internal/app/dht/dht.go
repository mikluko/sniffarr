// Package dht provides the BitTorrent client and DHT node the sniffer and
// the fetcher share.
package dht

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"

	"go.uber.org/fx"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/app"
	"github.com/mikluko/sniffarr/internal/sniff"
	"github.com/mikluko/sniffarr/internal/stun"
)

// Config is what Module needs.
type Config struct {
	// Network tunes the node; Passive must be set when nothing discovers.
	Network sniff.NetworkConfig
	// STUN is the server a node that is not passive asks for its public
	// address before it starts, for the BEP 42 prefix of its ID; empty asks
	// none and the ID is used as stored.
	STUN string
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

func start(p nodeParams) *sniff.Network {
	var sink sniff.PeerSink
	if p.Peers != nil {
		sink = p.Peers
	}
	n := sniff.New(p.Config.Network, p.Offer, sink)
	p.Lifecycle.Append(fx.StopHook(n.Close))
	app.Go(p.Lifecycle, p.Shutdowner, p.Log, "dht", func(ctx context.Context) error {
		var (
			id       [20]byte
			publicIP netip.Addr
		)
		if !p.Config.Network.Passive {
			var err error
			if id, err = sniff.ObtainNodeID(ctx, p.Ingest, sniff.DefaultBackoff, p.Log); err != nil {
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
	})
	return n
}
