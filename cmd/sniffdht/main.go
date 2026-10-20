// Command sniffdht is a sniffarr replica: it reports the infohashes it sights
// on the BitTorrent DHT to the server, fetches metadata for the tasks the
// server hands out, or both, until SIGINT or SIGTERM. It is configured by
// environment variables and flags; see the README.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"golang.org/x/sync/errgroup"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/auth"
	"github.com/mikluko/sniffarr/internal/cli"
	"github.com/mikluko/sniffarr/internal/server"
	"github.com/mikluko/sniffarr/internal/sniff"
	"github.com/mikluko/sniffarr/internal/telemetry"
)

type config struct {
	server          *url.URL
	token           string
	network         sniff.NetworkConfig
	walkers         int
	discover, fetch bool
	logLevel        slog.Level
}

func (c config) LogLevel() slog.Level { return c.logLevel }

func main() {
	cli.Main(func() (config, error) { return load(os.Args[1:], os.Getenv, os.Stderr) }, run)
}

// load reads the configuration from args and getenv, writing flag usage and
// errors to output.
func load(args []string, getenv func(string) string, output io.Writer) (config, error) {
	var c config
	fs := flag.NewFlagSet("sniffdht", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.BoolVar(&c.discover, "discover", true, "walk the DHT and report the infohashes it reveals")
	fs.BoolVar(&c.fetch, "fetch", true, "take fetch tasks from the server and return their metadata")
	fs.IntVar(&c.network.Port, "port", sniff.DefaultPort, "TCP and UDP port for peers and the DHT, forwarded by UPnP")
	fs.IntVar(&c.network.Fetchers, "fetchers", sniff.DefaultFetchers, "metadata fetches at once")
	fs.IntVar(&c.network.Rate, "rate", sniff.DefaultRate, "DHT queries, and new peer connections, a second")
	fs.DurationVar(&c.network.FetchTimeout, "fetch-timeout", sniff.DefaultFetchTimeout, "how long to wait for a torrent's metadata once it has peers")
	fs.DurationVar(&c.network.PeerWait, "peer-wait", sniff.DefaultPeerWait, "how long to look for a torrent's peers on the DHT")
	fs.IntVar(&c.network.MinPeers, "min-peers", sniff.DefaultMinPeers, "peers a torrent needs within -peer-wait to have its metadata fetched")
	fs.IntVar(&c.walkers, "walkers", sniff.DefaultWalkers, "sample_infohashes queries at once")
	fs.TextVar(&c.logLevel, "log-level", slog.LevelInfo, "level to log at: debug, info, warn or error")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	switch {
	case !c.discover && !c.fetch:
		return c, errors.New("-discover and -fetch are both off: nothing to do")
	case c.network.Port < 1 || c.network.Port > 65535:
		return c, errors.New("-port: want 1 to 65535")
	case c.network.Fetchers < 1, c.network.Rate < 1, c.walkers < 1, c.network.MinPeers < 1:
		return c, errors.New("-fetchers, -rate, -walkers and -min-peers: want positive integers")
	case c.network.FetchTimeout <= 0, c.network.PeerWait <= 0:
		return c, errors.New("-fetch-timeout and -peer-wait: want positive durations")
	}
	for _, name := range []string{"SNIFFARR_URL", "SNIFFARR_INGEST_TOKEN"} {
		if getenv(name) == "" {
			return c, fmt.Errorf("%s is not set", name)
		}
	}
	u, err := url.Parse(getenv("SNIFFARR_URL"))
	if err != nil {
		return c, fmt.Errorf("SNIFFARR_URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return c, errors.New("SNIFFARR_URL: want an http or https URL")
	}
	c.server, c.token = u, getenv("SNIFFARR_INGEST_TOKEN")
	return c, nil
}

func run(ctx context.Context, cfg config, log *slog.Logger) error {
	shutdown, err := telemetry.Setup(ctx, "sniffdht", log)
	if err != nil {
		return fmt.Errorf("telemetry: %w", err)
	}
	defer telemetry.Flush(ctx, shutdown, log)
	otel, err := otelconnect.NewInterceptor()
	if err != nil {
		return err
	}
	ingest := sniffarrv1connect.NewIngestServiceClient(httpClient(cfg.server), cfg.server.String(),
		connect.WithInterceptors(otel, auth.Client(cfg.token)),
		connect.WithSendMaxBytes(server.MaxMessage),
	)
	var d *sniff.Discoverer
	var offer func(sniff.Hash, sniffarrv1.Source)
	if cfg.discover {
		d = sniff.NewDiscoverer(sniff.DiscovererConfig{Ingest: ingest, Log: log})
		offer = d.Offer
	}
	n, err := sniff.Start(cfg.network, offer)
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}
	defer n.Close()
	if err := n.Bootstrap(ctx); err != nil && ctx.Err() == nil {
		log.Warn("bootstrap", "err", err)
	}
	log.Info("dht up", "port", cfg.network.Port, "nodes", n.Nodes(), "discover", cfg.discover, "fetch", cfg.fetch)

	g, ctx := errgroup.WithContext(ctx)
	if d != nil {
		g.Go(func() error {
			n.Sample(ctx, cfg.walkers, func(h sniff.Hash) { d.Offer(h, sniffarrv1.Source_SOURCE_SAMPLE) })
			return nil
		})
		g.Go(func() error { return d.Run(ctx) })
	}
	if cfg.fetch {
		f := sniff.NewFetcher(sniff.FetcherConfig{Ingest: ingest, Workers: cfg.network.Fetchers, Log: log}, n)
		g.Go(func() error { return f.Run(ctx) })
	}
	if err := g.Wait(); !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// httpClient speaks HTTP/2 to the server, which Fetch needs: without TLS to
// an http:// URL, negotiated over TLS to an https:// one.
func httpClient(u *url.URL) *http.Client {
	if u.Scheme != "http" {
		return http.DefaultClient
	}
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: &http.Transport{Protocols: &p}}
}
