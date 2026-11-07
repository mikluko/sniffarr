package web

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/audio"
)

// settingsAPI answers Settings with tags and probes after failing the first
// failures calls, and counts the calls.
type settingsAPI struct {
	sniffarrv1connect.UnimplementedBrowseServiceHandler
	failures int
	tags     []string
	probes   bool
	calls    int
}

func (a *settingsAPI) Settings(context.Context, *connect.Request[sniffarrv1.SettingsRequest]) (*connect.Response[sniffarrv1.SettingsResponse], error) {
	a.calls++
	if a.calls <= a.failures {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("not yet"))
	}
	return connect.NewResponse(&sniffarrv1.SettingsResponse{AudioLanguages: a.tags, Probes: a.probes}), nil
}

func TestObtainSettings(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	russian, err := audio.Parse("ru")
	require.NoError(t, err)

	api := &settingsAPI{failures: 2, tags: []string{"ru"}, probes: true}
	got, err := ObtainSettings(ctx, api, time.Millisecond, log)
	require.NoError(t, err)
	require.Equal(t, Settings{Languages: russian, Probes: true}, got)
	require.Equal(t, 3, api.calls, "two failures, then the answer")

	api = &settingsAPI{}
	got, err = ObtainSettings(ctx, api, time.Millisecond, log)
	require.NoError(t, err)
	require.Empty(t, got.Languages, "the extension is off")
	require.False(t, got.Probes, "probes are off")
	require.Equal(t, 1, api.calls)

	_, err = ObtainSettings(ctx, &settingsAPI{tags: []string{"ru", "xx"}}, time.Millisecond, nil)
	require.EqualError(t, err, `the API's audio languages ["ru" "xx"]: unknown audio language "xx"`, "a tag this build does not know is not retried")

	ctx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = ObtainSettings(ctx, &settingsAPI{failures: 1}, time.Hour, log)
	require.ErrorIs(t, err, context.Canceled, "the first failure's backoff ends with the context")
}
