// Package ui is the web UI component, a client of the browse API.
package ui

import (
	"log/slog"
	"net/http"
	"time"

	"go.uber.org/fx"

	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/app"
	"github.com/mikluko/sniffarr/internal/audio"
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
	// Languages are the API's audio languages, in its order.
	Languages []audio.Language
}

// Module serves the UI on Config.Addr from start to stop.
var Module = fx.Module("ui", fx.Invoke(serve))

// localeTTL is how long a person's locale is kept before the provider is
// asked again.
const localeTTL = 10 * time.Minute

func serve(lc fx.Lifecycle, sd fx.Shutdowner, log *slog.Logger, cfg Config, browse sniffarrv1connect.BrowseServiceClient) error {
	wc := web.Config{Browse: browse, Languages: cfg.Languages, Log: log}
	if cfg.OIDCIssuer != "" {
		wc.Locales = userinfo.New(cfg.OIDCIssuer, &http.Client{Timeout: 10 * time.Second}, localeTTL)
	}
	h, err := web.New(wc)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	app.Serve(lc, sd, log, "ui", srv, app.TCP(cfg.Addr))
	return nil
}
