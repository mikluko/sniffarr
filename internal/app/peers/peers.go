// Package peers provides the peer counter the sniffer and the fetcher feed:
// it sketches the peers they see per infohash and sends the sketches to the
// ingest API.
package peers

import (
	"log/slog"

	"go.uber.org/fx"

	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/app"
	"github.com/mikluko/sniffarr/internal/sniff"
)

// Module provides *sniff.Peers and runs it from start to stop.
var Module = fx.Module("peers", fx.Provide(newPeers), fx.Invoke(run))

func newPeers(ingest sniffarrv1connect.IngestServiceClient, log *slog.Logger) *sniff.Peers {
	return sniff.NewPeers(sniff.PeersConfig{Ingest: ingest, Log: log})
}

func run(lc fx.Lifecycle, sd fx.Shutdowner, log *slog.Logger, p *sniff.Peers) {
	app.Go(lc, sd, log, "peers", p.Run)
}
