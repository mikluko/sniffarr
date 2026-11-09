package app

import (
	"log/slog"
	"net/http"
	"net/http/pprof"
	"time"

	"go.uber.org/fx"
)

// Profiling serves the net/http/pprof profiles under /debug/pprof/ on addr
// from start to stop.
func Profiling(addr string) fx.Option {
	return fx.Invoke(func(lc fx.Lifecycle, sd fx.Shutdowner, log *slog.Logger) {
		srv := &http.Server{Handler: profiles(), ReadHeaderTimeout: 10 * time.Second}
		Serve(lc, sd, log, "pprof", srv, TCP(addr))
	})
}

// profiles is the mux of the net/http/pprof handlers.
func profiles() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}
