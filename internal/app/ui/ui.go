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
	// SeerrURL is the base URL of a Seerr the cards link titles to; empty
	// links none.
	SeerrURL string
	// RadarrURL and SonarrURL are the root URLs of the Radarr and Sonarr
	// the cards link movies and series to; empty links none.
	RadarrURL, SonarrURL string
}

// Module serves the UI on Config.Addr from start to stop. It listens only
// once the browse API has told it its settings, so a readiness probe on the
// port waits for the API too, and asks again every settingsInterval.
var Module = fx.Module("ui", fx.Invoke(serve))

// settingsInterval is how often the UI asks the API for its settings again.
const settingsInterval = time.Minute

// localeTTL is how long a person's locale is kept before the provider is
// asked again.
const localeTTL = 10 * time.Minute

func serve(lc fx.Lifecycle, sd fx.Shutdowner, log *slog.Logger, cfg Config, browse sniffarrv1connect.BrowseServiceClient) {
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second}
	app.Go(lc, sd, log, "ui", func(ctx context.Context) error {
		settings, err := web.ObtainSettings(ctx, browse, web.DefaultBackoff, log)
		if err != nil {
			return err
		}
		wc := web.Config{Browse: browse, Languages: settings.Languages, Probes: settings.Probes, Classifier: settings.Classifier, Feeds: settings.Feeds, JournalRetention: settings.JournalRetention, Latest: settings.Latest,
			SeerrURL: cfg.SeerrURL, RadarrURL: cfg.RadarrURL, SonarrURL: cfg.SonarrURL, Log: log}
		if cfg.OIDCIssuer != "" {
			wc.Locales = userinfo.New(cfg.OIDCIssuer, &http.Client{Timeout: 10 * time.Second}, localeTTL)
		}
		h, err := web.New(wc)
		if err != nil {
			return err
		}
		srv.Handler = h
		watch, stop := context.WithCancel(ctx)
		defer stop()
		go h.Watch(watch, settingsInterval)
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
