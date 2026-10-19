// Command sniffdht walks the BitTorrent DHT for infohashes, fetches the
// metadata of those the sniffarr server has never seen, and submits it until
// SIGINT or SIGTERM. It is configured by environment variables and flags;
// see the README.
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
	"os/signal"
	"syscall"

	"connectrpc.com/connect"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1conn"
	"github.com/mikluko/sniffarr/internal/auth"
	"github.com/mikluko/sniffarr/internal/crawler"
	"github.com/mikluko/sniffarr/internal/server"
)

type config struct {
	server  *url.URL
	token   string
	network crawler.NetworkConfig
	walkers int
}

func main() {
	cfg, err := load(os.Args[1:], os.Getenv, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		os.Exit(0)
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, log); err != nil {
		log.Error("exit", "err", err)
		os.Exit(1)
	}
}

// load reads the configuration from args and getenv, writing flag usage and
// errors to output.
func load(args []string, getenv func(string) string, output io.Writer) (config, error) {
	var c config
	fs := flag.NewFlagSet("sniffdht", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.IntVar(&c.network.Port, "port", crawler.DefaultPort, "TCP and UDP port for peers and the DHT, forwarded by UPnP")
	fs.IntVar(&c.network.Fetchers, "fetchers", crawler.DefaultFetchers, "metadata fetches at once")
	fs.IntVar(&c.network.Rate, "rate", crawler.DefaultRate, "DHT queries, and new peer connections, a second")
	fs.DurationVar(&c.network.FetchTimeout, "fetch-timeout", crawler.DefaultFetchTimeout, "how long to wait for a torrent's metadata once it has peers")
	fs.DurationVar(&c.network.PeerWait, "peer-wait", crawler.DefaultPeerWait, "how long to look for a torrent's peers on the DHT")
	fs.IntVar(&c.walkers, "walkers", crawler.DefaultWalkers, "sample_infohashes queries at once")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	switch {
	case c.network.Port < 1 || c.network.Port > 65535:
		return c, errors.New("-port: want 1 to 65535")
	case c.network.Fetchers < 1, c.network.Rate < 1, c.walkers < 1:
		return c, errors.New("-fetchers, -rate and -walkers: want positive integers")
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
	ingest := sniffarrv1conn.NewIngestServiceClient(httpClient(cfg.server), cfg.server.String(),
		connect.WithInterceptors(auth.Client(cfg.token)),
		connect.WithSendMaxBytes(server.MaxMessage),
	)
	c := crawler.New(crawler.Config{Ingest: ingest, Fetchers: cfg.network.Fetchers, Log: log})
	n, err := crawler.Start(cfg.network, c.Offer)
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}
	defer n.Close()
	if err := n.Bootstrap(ctx); err != nil && ctx.Err() == nil {
		log.Warn("bootstrap", "err", err)
	}
	log.Info("dht up", "port", cfg.network.Port, "nodes", n.Nodes())

	go n.Sample(ctx, cfg.walkers, func(h crawler.Hash) { c.Offer(h, sniffarrv1.Source_SOURCE_SAMPLE) })
	if err := c.Run(ctx, n); !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// httpClient speaks HTTP/2 without TLS to an http:// server, so that Submit
// streams share one connection.
func httpClient(u *url.URL) *http.Client {
	if u.Scheme != "http" {
		return http.DefaultClient
	}
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: &http.Transport{Protocols: &p}}
}
