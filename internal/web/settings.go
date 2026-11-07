package web

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/audio"
)

// DefaultBackoff is the wait between attempts of ObtainSettings.
const DefaultBackoff = 5 * time.Second

// Settings is what the browse API is configured with that the pages show.
type Settings struct {
	// Languages are its audio languages in its order.
	Languages []audio.Language
	// Probes is whether it probes releases.
	Probes bool
	// Classifier is whether it runs the Jev classifier.
	Classifier bool
	// Feeds is whether it reads tracker feeds.
	Feeds bool
}

// ObtainSettings asks the browse API for its settings, retrying after
// backoff until it answers or ctx is done. Every failed attempt after the
// first is logged as a warning. An audio language tag this build does not
// know is an error at once: the UI cannot show a language it has no signs
// for.
func ObtainSettings(ctx context.Context, browse sniffarrv1connect.BrowseServiceClient, backoff time.Duration, log *slog.Logger) (Settings, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	for attempt := 1; ; attempt++ {
		res, err := browse.Settings(ctx, connect.NewRequest(&sniffarrv1.SettingsRequest{}))
		if err == nil {
			tags := res.Msg.GetAudioLanguages()
			langs, err := audio.Parse(strings.Join(tags, ","))
			if err != nil {
				return Settings{}, fmt.Errorf("the API's audio languages %q: %w", tags, err)
			}
			out := Settings{Languages: langs, Probes: res.Msg.GetProbes()}
			for _, f := range res.Msg.GetFeatures() {
				switch f.GetFeature() {
				case sniffarrv1.Feature_FEATURE_CLASSIFIER:
					out.Classifier = f.GetOn()
				case sniffarrv1.Feature_FEATURE_FEEDS:
					out.Feeds = f.GetOn()
				}
			}
			return out, nil
		}
		if attempt > 1 {
			log.Warn("settings", "err", err, "attempt", attempt)
		} else {
			log.Debug("settings", "err", err, "attempt", attempt)
		}
		if err := sleep(ctx, backoff); err != nil {
			return Settings{}, err
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
