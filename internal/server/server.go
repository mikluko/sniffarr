// Package server assembles the sniffarr HTTP handler: the ingest API behind
// its bearer token, the search and browse APIs behind theirs, the Torznab
// API, and an unauthenticated /healthz.
package server

import (
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"go.opentelemetry.io/otel/metric"

	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/audio"
	"github.com/mikluko/sniffarr/internal/auth"
	"github.com/mikluko/sniffarr/internal/ingest"
	"github.com/mikluko/sniffarr/internal/rules"
	"github.com/mikluko/sniffarr/internal/search"
	"github.com/mikluko/sniffarr/internal/store"
	"github.com/mikluko/sniffarr/internal/torznab"
)

// MaxMessage is the largest request message, in bytes, either API accepts.
const MaxMessage = 32 << 20

// Config is what New needs. Both tokens must be set; the fetch fields are
// those of ingest.Service.
type Config struct {
	Store       *store.Store
	Queue       *store.Queue
	IngestToken string
	SearchToken string
	Lease       time.Duration
	Attempts    int
	Backoff     time.Duration
	Poll        time.Duration
	// Scripts are the writing systems a torrent's name may have letters of;
	// rules.DefaultScripts when nil.
	Scripts rules.Scripts
	// Languages get an audio entry in every stored torrent, and
	// BrowseService.Settings reports them; nil leaves the audio extension
	// off.
	Languages []audio.Language
	Log       *slog.Logger
	// Meter records metrics; the global MeterProvider when nil. Traces go to
	// the global TracerProvider.
	Meter metric.MeterProvider
	// Lazy, when set, has Search classify its candidates first.
	Lazy *search.Lazy
	// TorznabToken, when set, serves the Torznab API at /torznab/api to
	// requests with it as their apikey.
	TorznabToken string
	// DailyTokens is the classifier's daily token limit BrowseService
	// reports; 0 is none.
	DailyTokens int64
}

// New returns the handler serving the APIs and /healthz.
func New(c Config) (http.Handler, error) {
	var opts []otelconnect.Option
	if c.Meter != nil {
		opts = append(opts, otelconnect.WithMeterProvider(c.Meter))
	}
	otel, err := otelconnect.NewInterceptor(opts...)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle(sniffarrv1connect.NewIngestServiceHandler(
		&ingest.Service{Store: c.Store, Queue: c.Queue, Log: c.Log, Lease: c.Lease, Attempts: c.Attempts, Backoff: c.Backoff, Poll: c.Poll, Scripts: c.Scripts, Languages: c.Languages, Meter: c.Meter},
		connect.WithInterceptors(otel, auth.NewBearer(c.IngestToken)),
		connect.WithReadMaxBytes(MaxMessage),
	))
	searchService := &search.Service{Store: c.Store, Log: c.Log, Lazy: c.Lazy}
	mux.Handle(sniffarrv1connect.NewSearchServiceHandler(
		searchService,
		connect.WithInterceptors(otel, auth.NewBearer(c.SearchToken)),
		connect.WithReadMaxBytes(MaxMessage),
	))
	mux.Handle(sniffarrv1connect.NewBrowseServiceHandler(
		&search.Browser{Store: c.Store, Log: c.Log, DailyTokens: c.DailyTokens, Languages: audio.Tags(c.Languages)},
		connect.WithInterceptors(otel, auth.NewBearer(c.SearchToken)),
		connect.WithReadMaxBytes(MaxMessage),
	))
	if c.TorznabToken != "" {
		mux.Handle("GET /torznab/api", &torznab.Handler{Search: searchService, Token: c.TorznabToken, Log: c.Log})
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux, nil
}

// Protocols are the protocols the server speaks: HTTP/1.1 and HTTP/2
// without TLS.
func Protocols() *http.Protocols {
	var p http.Protocols
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	return &p
}
