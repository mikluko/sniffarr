package web

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"connectrpc.com/connect"

	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"sarak.sh/mikluko/sniffarr/internal/audio"
	"sarak.sh/mikluko/sniffarr/internal/latest"
	"sarak.sh/mikluko/sniffarr/internal/version"
)

// DefaultBackoff is the wait between attempts of ObtainSettings.
const DefaultBackoff = 5 * time.Second

// Settings is what the browse API is configured with that the pages show.
type Settings struct {
	// Languages are its audio languages in its order.
	Languages []audio.Language
	// Probes is whether it probes releases.
	Probes bool
	// Classifier is whether it runs the classifier.
	Classifier bool
	// Feeds is whether it reads tracker feeds.
	Feeds bool
	// JournalRetention is how long its journal keeps an entry; 0 when it
	// does not say.
	JournalRetention time.Duration
	// Latest is the latest stable release it knows; zero when none.
	Latest latest.Release
	// Notice is how far behind Latest a stable build is before it is
	// announced.
	Notice version.Policy
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
			return settingsOf(res.Msg)
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

func settingsOf(msg *sniffarrv1.SettingsResponse) (Settings, error) {
	tags := msg.GetAudioLanguages()
	langs, err := audio.Parse(strings.Join(tags, ","))
	if err != nil {
		return Settings{}, fmt.Errorf("the API's audio languages %q: %w", tags, err)
	}
	out := Settings{Languages: langs, Probes: msg.GetProbes(), JournalRetention: msg.GetJournalRetention().AsDuration()}
	if r := msg.GetLatestRelease(); r != nil {
		out.Latest = latest.Release{Version: r.GetVersion(), Published: r.GetPublished().AsTime(), URL: r.GetUrl()}
	}
	if msg.GetReleaseNotice() == sniffarrv1.ReleaseNotice_RELEASE_NOTICE_EAGER {
		out.Notice = version.Eager
	}
	for _, f := range msg.GetFeatures() {
		switch f.GetFeature() {
		case sniffarrv1.Feature_FEATURE_CLASSIFIER:
			out.Classifier = f.GetOn()
		case sniffarrv1.Feature_FEATURE_FEEDS:
			out.Feeds = f.GetOn()
		}
	}
	return out, nil
}

// Watch asks the browse API for its settings every interval until ctx is
// done and applies each answer that differs from the one in use. A failed
// call or an answer this build cannot show is logged and the settings in
// use stay.
func (h *Handler) Watch(ctx context.Context, interval time.Duration) {
	log := h.s.cfg.Log
	for sleep(ctx, interval) == nil {
		res, err := h.s.cfg.Browse.Settings(ctx, connect.NewRequest(&sniffarrv1.SettingsRequest{}))
		if err != nil {
			log.WarnContext(ctx, "settings", "err", err)
			continue
		}
		next, err := settingsOf(res.Msg)
		if err != nil {
			log.WarnContext(ctx, "settings", "err", err)
			continue
		}
		if !reflect.DeepEqual(next, h.s.settings().Settings) {
			h.Apply(next)
			log.InfoContext(ctx, "settings changed", "languages", audio.Tags(next.Languages), "probes", next.Probes, "classifier", next.Classifier, "feeds", next.Feeds, "journal_retention", next.JournalRetention, "latest", next.Latest.Version, "notice", next.Notice)
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
