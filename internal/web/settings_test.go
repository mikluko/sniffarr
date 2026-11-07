package web

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
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
	features []*sniffarrv1.FeatureState
	calls    int
}

func (a *settingsAPI) Settings(context.Context, *connect.Request[sniffarrv1.SettingsRequest]) (*connect.Response[sniffarrv1.SettingsResponse], error) {
	a.calls++
	if a.calls <= a.failures {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("not yet"))
	}
	return connect.NewResponse(&sniffarrv1.SettingsResponse{AudioLanguages: a.tags, Probes: a.probes, Features: a.features}), nil
}

func TestObtainSettings(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	russian, err := audio.Parse("ru")
	require.NoError(t, err)

	api := &settingsAPI{failures: 2, tags: []string{"ru"}, probes: true, features: []*sniffarrv1.FeatureState{
		{Feature: sniffarrv1.Feature_FEATURE_CLASSIFIER, On: true},
		{Feature: sniffarrv1.Feature_FEATURE_FEEDS, On: true, Works: true, Values: []string{"rutor"}},
	}}
	got, err := ObtainSettings(ctx, api, time.Millisecond, log)
	require.NoError(t, err)
	require.Equal(t, Settings{Languages: russian, Probes: true, Classifier: true, Feeds: true}, got, "on, whether working or not")
	require.Equal(t, 3, api.calls, "two failures, then the answer")

	api = &settingsAPI{}
	got, err = ObtainSettings(ctx, api, time.Millisecond, log)
	require.NoError(t, err)
	require.Empty(t, got.Languages, "the extension is off")
	require.False(t, got.Probes, "probes are off")
	require.False(t, got.Classifier || got.Feeds, "an API that names no feature has them off")
	require.Equal(t, 1, api.calls)

	_, err = ObtainSettings(ctx, &settingsAPI{tags: []string{"ru", "xx"}}, time.Millisecond, nil)
	require.EqualError(t, err, `the API's audio languages ["ru" "xx"]: unknown audio language "xx"`, "a tag this build does not know is not retried")

	ctx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = ObtainSettings(ctx, &settingsAPI{failures: 1}, time.Hour, log)
	require.ErrorIs(t, err, context.Canceled, "the first failure's backoff ends with the context")
}

// switching answers Settings with what was last set, or its error, and
// counts the calls; every other call goes to the embedded client.
type switching struct {
	sniffarrv1connect.BrowseServiceClient
	mu    sync.Mutex
	res   *sniffarrv1.SettingsResponse
	err   error
	calls int
}

func (s *switching) set(res *sniffarrv1.SettingsResponse, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.res, s.err = res, err
}

func (s *switching) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *switching) Settings(context.Context, *connect.Request[sniffarrv1.SettingsRequest]) (*connect.Response[sniffarrv1.SettingsResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return connect.NewResponse(s.res), nil
}

func TestWatch(t *testing.T) {
	h, _ := handlerTasks(t, "ru", Config{})
	api := &switching{BrowseServiceClient: h.s.cfg.Browse}
	h.s.cfg.Browse = api
	api.set(&sniffarrv1.SettingsResponse{AudioLanguages: []string{"ru"}}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Watch(ctx, time.Millisecond)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	probe := func() bool {
		return strings.Contains(page(t, get(t, h, "/t/"+hexOf(1), "")), `act-probe`)
	}
	voice := func() bool {
		return strings.Contains(page(t, get(t, h, "/torrents", "")), `name="vl"`)
	}
	require.False(t, probe(), "the API the UI started with does not probe")
	require.True(t, voice())

	api.set(&sniffarrv1.SettingsResponse{AudioLanguages: []string{"ru"}, Probes: true}, nil)
	require.Eventually(t, probe, 5*time.Second, time.Millisecond, "an API that started later probes")

	failing := api.count()
	api.set(nil, connect.NewError(connect.CodeUnavailable, errors.New("restarting")))
	require.Eventually(t, func() bool { return api.count() >= failing+3 }, 5*time.Second, time.Millisecond)
	require.True(t, probe(), "a failed call leaves the settings")

	api.set(&sniffarrv1.SettingsResponse{AudioLanguages: []string{"ru", "xx"}}, nil)
	unknown := api.count()
	require.Eventually(t, func() bool { return api.count() >= unknown+3 }, 5*time.Second, time.Millisecond)
	require.True(t, probe(), "a language this build does not know leaves the settings")

	api.set(&sniffarrv1.SettingsResponse{}, nil)
	require.Eventually(t, func() bool { return !probe() && !voice() }, 5*time.Second, time.Millisecond, "probes and languages off")
}
