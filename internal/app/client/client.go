// Package client provides the Connect clients of the sniffarr API that the
// UI, the sniffer and the fetcher use, over memconn to an API in the same
// process and over the network otherwise.
package client

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"go.uber.org/fx"

	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/auth"
	"github.com/mikluko/sniffarr/internal/memconn"
	"github.com/mikluko/sniffarr/internal/server"
)

// Config is what Module needs.
type Config struct {
	// URL is the base URL of the API; nil when the API runs in this process.
	URL *url.URL
	// IngestToken is the bearer token of IngestService.
	IngestToken string
	// SearchToken is the bearer token of BrowseService.
	SearchToken string
}

// Module provides sniffarrv1connect.IngestServiceClient and
// sniffarrv1connect.BrowseServiceClient.
var Module = fx.Module("client", fx.Provide(newEndpoint, newIngest, newBrowse))

// browseTimeout bounds one request of the browse client.
const browseTimeout = 30 * time.Second

// endpoint is where the API is reached and how.
type endpoint struct {
	client *http.Client
	base   string
}

// endpointParams takes the in-process API when there is one.
type endpointParams struct {
	fx.In
	Config Config
	// Mem is the listener of the API in this process; nil without one.
	Mem *memconn.Listener `optional:"true"`
}

// newEndpoint reaches the API over memconn when the process serves one,
// else at Config.URL.
func newEndpoint(p endpointParams) (endpoint, error) {
	if p.Mem != nil {
		return endpoint{client: p.Mem.Client(), base: memconn.URL}, nil
	}
	if p.Config.URL == nil {
		return endpoint{}, errors.New("no API to reach: neither in this process nor at SNIFFARR_URL")
	}
	return endpoint{client: httpClient(p.Config.URL), base: p.Config.URL.String()}, nil
}

// httpClient speaks HTTP/2 to the API, which Fetch needs: without TLS to an
// http:// URL, negotiated over TLS to an https:// one.
func httpClient(u *url.URL) *http.Client {
	if u.Scheme != "http" {
		return &http.Client{}
	}
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: &http.Transport{Protocols: &p}}
}

func newIngest(e endpoint, cfg Config) (sniffarrv1connect.IngestServiceClient, error) {
	otel, err := otelconnect.NewInterceptor()
	if err != nil {
		return nil, err
	}
	return sniffarrv1connect.NewIngestServiceClient(e.client, e.base,
		connect.WithInterceptors(otel, auth.Client(cfg.IngestToken)),
		connect.WithSendMaxBytes(server.MaxMessage),
	), nil
}

func newBrowse(e endpoint, cfg Config) (sniffarrv1connect.BrowseServiceClient, error) {
	otel, err := otelconnect.NewInterceptor()
	if err != nil {
		return nil, err
	}
	hc := *e.client
	hc.Timeout = browseTimeout
	return sniffarrv1connect.NewBrowseServiceClient(&hc, e.base,
		connect.WithInterceptors(otel, auth.Client(cfg.SearchToken)),
	), nil
}
