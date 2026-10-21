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

// settingsAPI answers Settings with tags after failing the first failures
// calls, and counts the calls.
type settingsAPI struct {
	sniffarrv1connect.UnimplementedBrowseServiceHandler
	failures int
	tags     []string
	calls    int
}

func (a *settingsAPI) Settings(context.Context, *connect.Request[sniffarrv1.SettingsRequest]) (*connect.Response[sniffarrv1.SettingsResponse], error) {
	a.calls++
	if a.calls <= a.failures {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("not yet"))
	}
	return connect.NewResponse(&sniffarrv1.SettingsResponse{AudioLanguages: a.tags}), nil
}

func TestObtainLanguages(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	russian, err := audio.Parse("ru")
	require.NoError(t, err)

	api := &settingsAPI{failures: 2, tags: []string{"ru"}}
	langs, err := ObtainLanguages(ctx, api, time.Millisecond, log)
	require.NoError(t, err)
	require.Equal(t, russian, langs)
	require.Equal(t, 3, api.calls, "two failures, then the answer")

	api = &settingsAPI{}
	langs, err = ObtainLanguages(ctx, api, time.Millisecond, log)
	require.NoError(t, err)
	require.Empty(t, langs, "the extension is off")
	require.Equal(t, 1, api.calls)

	_, err = ObtainLanguages(ctx, &settingsAPI{tags: []string{"ru", "xx"}}, time.Millisecond, nil)
	require.EqualError(t, err, `the API's audio languages ["ru" "xx"]: unknown audio language "xx"`, "a tag this build does not know is not retried")

	ctx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = ObtainLanguages(ctx, &settingsAPI{failures: 1}, time.Hour, log)
	require.ErrorIs(t, err, context.Canceled, "the first failure's backoff ends with the context")
}
