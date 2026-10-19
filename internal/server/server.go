// Package server assembles the sniffarr HTTP handler: the ingest and search
// APIs, each behind its own bearer token, and an unauthenticated /healthz.
package server

import (
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"

	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/auth"
	"github.com/mikluko/sniffarr/internal/ingest"
	"github.com/mikluko/sniffarr/internal/queue"
	"github.com/mikluko/sniffarr/internal/search"
	"github.com/mikluko/sniffarr/internal/store"
)

// MaxMessage is the largest request message, in bytes, either API accepts.
const MaxMessage = 32 << 20

// Config is what New needs. Both tokens must be set; the fetch fields are
// those of ingest.Service.
type Config struct {
	Store       *store.Store
	Queue       *queue.Queue
	IngestToken string
	SearchToken string
	Lease       time.Duration
	Attempts    int
	Backoff     time.Duration
	Poll        time.Duration
	Log         *slog.Logger
}

// New returns the handler serving both APIs and /healthz.
func New(c Config) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(sniffarrv1connect.NewIngestServiceHandler(
		&ingest.Service{Store: c.Store, Queue: c.Queue, Log: c.Log, Lease: c.Lease, Attempts: c.Attempts, Backoff: c.Backoff, Poll: c.Poll},
		connect.WithInterceptors(auth.NewBearer(c.IngestToken)),
		connect.WithReadMaxBytes(MaxMessage),
	))
	mux.Handle(sniffarrv1connect.NewSearchServiceHandler(
		&search.Service{Store: c.Store, Log: c.Log},
		connect.WithInterceptors(auth.NewBearer(c.SearchToken)),
		connect.WithReadMaxBytes(MaxMessage),
	))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}

// Protocols are the protocols the server speaks: HTTP/1.1 and HTTP/2
// without TLS.
func Protocols() *http.Protocols {
	var p http.Protocols
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	return &p
}
