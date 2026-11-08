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
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/metric"

	libversion "sarak.sh/mikluko/libarr/version"
	"sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"sarak.sh/mikluko/sniffarr/internal/arr"
	"sarak.sh/mikluko/sniffarr/internal/audio"
	"sarak.sh/mikluko/sniffarr/internal/ingest"
	"sarak.sh/mikluko/sniffarr/internal/orders"
	"sarak.sh/mikluko/sniffarr/internal/rules"
	"sarak.sh/mikluko/sniffarr/internal/search"
	"sarak.sh/mikluko/sniffarr/internal/store"
	"sarak.sh/mikluko/sniffarr/internal/torznab"
	"sarak.sh/mikluko/sniffarr/internal/version"
)

// ingestService returns c.Ingest, or else one built from the fetch fields.
func (c Config) ingestService() *ingest.Service {
	if c.Ingest != nil {
		return c.Ingest
	}
	return &ingest.Service{Store: c.Store, Fetches: c.Fetches, ProbeOrders: c.ProbeOrders, Probers: c.Probers, Log: c.Log, Attempts: c.Attempts, Backoff: c.Backoff, DeadFor: c.DeadFor, NoPeersFor: c.NoPeersFor, Poll: c.Poll, JetStream: c.JetStream,
		Scripts: c.Scripts, Languages: c.Languages, Probe: c.Probe, Probes: c.Probes, Meter: c.Meter}
}

// MaxMessage is the largest request message, in bytes, either API accepts.
const MaxMessage = 32 << 20

// Config is what New needs; the fetch fields are those of ingest.Service.
type Config struct {
	Store *store.Store
	// Fetches hands out the fetch orders and peer lookups, and orders those;
	// ProbeOrders likewise the probes, and Probers counts who asks for them.
	Fetches             *orders.Queue
	ProbeOrders         *orders.Queue
	Probers             *store.Probers
	Attempts            int
	Backoff             time.Duration
	DeadFor, NoPeersFor time.Duration
	Poll                time.Duration
	// JetStream is the connection IngestService.Sight publishes sightings
	// on, for the IngestService built from the fetch fields.
	JetStream jetstream.JetStream
	// Ingest, when set, is the IngestService served in place of one built
	// from the fetch fields.
	Ingest *ingest.Service
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
	// Probes has the APIs order and hand out probes and Torznab order those of
	// the releases its searches return; without it no probe is ordered.
	Probes bool
	// Classifier has BrowseService.Settings report the classifier on, and
	// ClassifierSetting is the setting it names for it.
	Classifier        bool
	ClassifierSetting string
	// JournalRetention is how long the journal keeps an entry, which
	// BrowseService.Settings reports.
	JournalRetention time.Duration
	// Releases, when set, has BrowseService.Settings report the latest
	// stable release it knows.
	Releases search.LatestRelease
	// ReleaseNotice is the policy BrowseService.Settings reports beside it.
	ReleaseNotice version.Policy
}

// New returns the handler serving the APIs and /healthz, every response
// naming the build in its Server header.
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
	ing := c.ingestService()
	mux.Handle(sniffarrv1connect.NewIngestServiceHandler(
		ing,
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
		&search.Browser{Store: c.Store, Fetches: c.Fetches, ProbeOrders: c.ProbeOrders, Probers: c.Probers, TMDB: c.TMDB, Matcher: c.Matcher, Log: c.Log, DailyTokens: c.DailyTokens, ProbeDailyBytes: c.Probe.DailyBytes, Probes: c.Probes, Languages: audio.Tags(c.Languages), Counts: c.Counts,
			Classifier: c.Classifier, ClassifierSetting: c.ClassifierSetting, JournalRetention: c.JournalRetention,
			Releases: c.Releases, ReleaseNotice: c.ReleaseNotice},
		connect.WithInterceptors(otel),
		connect.WithReadMaxBytes(MaxMessage),
	))
	if c.TorznabToken != "" {
		torznabSearch := &search.Service{Store: c.Store, Log: c.Log, Lazy: c.Lazy, NoWait: true}
		th := &torznab.Handler{Search: torznabSearch, Token: c.TorznabToken, Log: c.Log}
		if c.Probes {
			th.Probes = ing
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
	return libversion.Middleware("sniffarr", mux), nil
}

// Protocols are the protocols the server speaks: HTTP/1.1 and HTTP/2
// without TLS.
func Protocols() *http.Protocols {
	var p http.Protocols
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	return &p
}
