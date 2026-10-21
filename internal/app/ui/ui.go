// Package ui is the web UI component, a client of the browse API.
package ui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"go.uber.org/fx"

	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/app"
	"github.com/mikluko/sniffarr/internal/userinfo"
	"github.com/mikluko/sniffarr/internal/web"
)

// Config is what Module needs.
type Config struct {
	// Addr is the listen address.
	Addr string
	// OIDCIssuer is the OpenID Connect issuer whose userinfo gives a
	// person's locale; empty asks none.
	OIDCIssuer string
}

// Module serves the UI on Config.Addr from start to stop. It listens only
// once the browse API has told it its settings, so a readiness probe on the
// port waits for the API too.
var Module = fx.Module("ui", fx.Invoke(serve))

// localeTTL is how long a person's locale is kept before the provider is
// asked again.
const localeTTL = 10 * time.Minute

func serve(lc fx.Lifecycle, sd fx.Shutdowner, log *slog.Logger, cfg Config, browse sniffarrv1connect.BrowseServiceClient) {
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second}
	app.Go(lc, sd, log, "ui", func(ctx context.Context) error {
		langs, err := web.ObtainLanguages(ctx, browse, web.DefaultBackoff, log)
		if err != nil {
			return err
		}
		wc := web.Config{Browse: browse, Languages: langs, Log: log}
		if cfg.OIDCIssuer != "" {
			wc.Locales = userinfo.New(cfg.OIDCIssuer, &http.Client{Timeout: 10 * time.Second}, localeTTL)
		}
		if srv.Handler, err = web.New(wc); err != nil {
			return err
		}
		l, err := net.Listen("tcp", cfg.Addr)
		if err != nil {
			return fmt.Errorf("ui: %w", err)
		}
		log.Info("ui", "addr", l.Addr().String())
		if err := srv.Serve(l); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	lc.Append(fx.StopHook(srv.Shutdown))
}
