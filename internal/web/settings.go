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

// DefaultBackoff is the wait between attempts of ObtainLanguages.
const DefaultBackoff = 5 * time.Second

// ObtainLanguages asks the browse API for its audio languages, retrying
// after backoff until it answers or ctx is done. Every failed attempt after
// the first is logged as a warning. A tag this build does not know is an
// error at once: the UI cannot show a language it has no signs for.
func ObtainLanguages(ctx context.Context, browse sniffarrv1connect.BrowseServiceClient, backoff time.Duration, log *slog.Logger) ([]audio.Language, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	for attempt := 1; ; attempt++ {
		res, err := browse.Settings(ctx, connect.NewRequest(&sniffarrv1.SettingsRequest{}))
		if err == nil {
			tags := res.Msg.GetAudioLanguages()
			langs, err := audio.Parse(strings.Join(tags, ","))
			if err != nil {
				return nil, fmt.Errorf("the API's audio languages %q: %w", tags, err)
			}
			return langs, nil
		}
		if attempt > 1 {
			log.Warn("settings", "err", err, "attempt", attempt)
		} else {
			log.Debug("settings", "err", err, "attempt", attempt)
		}
		if err := sleep(ctx, backoff); err != nil {
			return nil, err
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
