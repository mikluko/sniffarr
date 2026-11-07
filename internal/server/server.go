// Package server assembles the sniffarr HTTP handler: the ingest, search and
// browse APIs, which authenticate no caller, the Torznab API, the Radarr and
// Sonarr webhook, and /healthz.
package server

import (
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/metric"

	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/arr"
	"github.com/mikluko/sniffarr/internal/audio"
	"github.com/mikluko/sniffarr/internal/ingest"
	"github.com/mikluko/sniffarr/internal/rules"
	"github.com/mikluko/sniffarr/internal/search"
	"github.com/mikluko/sniffarr/internal/store"
	"github.com/mikluko/sniffarr/internal/torznab"
)

// MaxMessage is the largest request message, in bytes, either API accepts.
const MaxMessage = 32 << 20

// Config is what New needs; the fetch fields are those of ingest.Service.
type Config struct {
	Store    *store.Store
	Tasks    *store.Tasks
	Lease    time.Duration
	Attempts int
	Backoff  time.Duration
	Poll     time.Duration
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
	// Lazy, when set, has SearchService.Search classify its candidates
	// first, and has a Torznab search start the same in the background
	// without waiting for them.
	Lazy *search.Lazy
	// TMDB, when set, has BrowseService.SearchTitles search TMDB too.
	TMDB search.TitleSearch
	// Matcher, when set, serves BrowseService.RequestMatch.
	Matcher search.TitleMatcher
	// TorznabToken, when set, serves the Torznab API at /torznab/api to
	// requests with it as their apikey.
	TorznabToken string
	// DailyTokens is the classifier's daily token limit BrowseService
	// reports; 0 is none.
	DailyTokens int64
	// Counts, when set, holds the counts BrowseService.Stats and Home serve;
	// nil computes them at every call.
	Counts *search.Counts
	// WebhookToken, when set with Arr, serves the Radarr and Sonarr webhook
	// at POST /webhooks/arr to requests with it as their Basic password.
	WebhookToken string
	Arr          *arr.Recorder
	// Probe tunes IngestService's probe tasks.
	Probe ingest.ProbeConfig
	// Probes has the APIs queue and lease probes and Torznab record the
	// releases of its searches for them; without it no probe is queued.
	Probes bool
	// Classifier has BrowseService.Settings report the classifier on.
	Classifier bool
	// Feeds are the names of the feeds read that BrowseService.Settings
	// reports.
	Feeds []string
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
	var hopts []otelhttp.Option
	if c.Meter != nil {
		hopts = append(hopts, otelhttp.WithMeterProvider(c.Meter))
	}
	mux := http.NewServeMux()
	traced := func(route string, h http.Handler) {
		mux.Handle(route, otelhttp.NewHandler(h, route, hopts...))
	}
	mux.Handle(sniffarrv1connect.NewIngestServiceHandler(
		&ingest.Service{Store: c.Store, Tasks: c.Tasks, Log: c.Log, Lease: c.Lease, Attempts: c.Attempts, Backoff: c.Backoff, Poll: c.Poll, Scripts: c.Scripts, Languages: c.Languages, Probe: c.Probe, Probes: c.Probes, Meter: c.Meter},
		connect.WithInterceptors(otel),
		connect.WithReadMaxBytes(MaxMessage),
	))
	searchService := &search.Service{Store: c.Store, Log: c.Log, Lazy: c.Lazy}
	mux.Handle(sniffarrv1connect.NewSearchServiceHandler(
		searchService,
		connect.WithInterceptors(otel),
		connect.WithReadMaxBytes(MaxMessage),
	))
	mux.Handle(sniffarrv1connect.NewBrowseServiceHandler(
		&search.Browser{Store: c.Store, Tasks: c.Tasks, TMDB: c.TMDB, Matcher: c.Matcher, Log: c.Log, DailyTokens: c.DailyTokens, ProbeDailyBytes: c.Probe.DailyBytes, Probes: c.Probes, Languages: audio.Tags(c.Languages), Counts: c.Counts,
			Classifier: c.Classifier, Feeds: c.Feeds},
		connect.WithInterceptors(otel),
		connect.WithReadMaxBytes(MaxMessage),
	))
	if c.TorznabToken != "" {
		torznabSearch := &search.Service{Store: c.Store, Log: c.Log, Lazy: c.Lazy, NoWait: true}
		th := &torznab.Handler{Search: torznabSearch, Token: c.TorznabToken, Log: c.Log}
		if c.Probes {
			th.Probes = c.Store
		}
		traced("GET /torznab/api", th)
	}
	if c.WebhookToken != "" && c.Arr != nil {
		traced("POST /webhooks/arr", &arr.Webhook{Recorder: c.Arr, Token: c.WebhookToken, Log: c.Log})
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if c.Counts != nil && !c.Counts.Ready() {
			http.Error(w, "counting", http.StatusServiceUnavailable)
			return
		}
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
