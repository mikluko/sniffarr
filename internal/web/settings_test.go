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
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"sarak.sh/mikluko/sniffarr/internal/audio"
	"sarak.sh/mikluko/sniffarr/internal/latest"
	"sarak.sh/mikluko/sniffarr/internal/version"
)

func TestSettingsOfLatestRelease(t *testing.T) {
	published := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	url := "https://sarak.sh/mikluko/sniffarr/releases/tag/v0.46.0"
	got, err := settingsOf(&sniffarrv1.SettingsResponse{LatestRelease: &sniffarrv1.ProjectRelease{
		Version: "0.46.0", Published: timestamppb.New(published), Url: url}})
	require.NoError(t, err)
	require.Equal(t, latest.Release{Version: "0.46.0", Published: published, URL: url}, got.Latest)
	require.Equal(t, version.Lazy, got.Notice)

	got, err = settingsOf(&sniffarrv1.SettingsResponse{ReleaseNotice: sniffarrv1.ReleaseNotice_RELEASE_NOTICE_EAGER})
	require.NoError(t, err)
	require.Equal(t, version.Eager, got.Notice)

	got, err = settingsOf(&sniffarrv1.SettingsResponse{})
	require.NoError(t, err)
	require.Zero(t, got.Latest, "an API that knows no release announces none")
}

// settingsAPI answers Settings with tags and probes after failing the first
// failures calls, and counts the calls.
type settingsAPI struct {
	sniffarrv1connect.UnimplementedBrowseServiceHandler
	failures  int
	tags      []string
	probes    bool
	features  []*sniffarrv1.FeatureState
	retention time.Duration
	calls     int
}

func (a *settingsAPI) Settings(context.Context, *connect.Request[sniffarrv1.SettingsRequest]) (*connect.Response[sniffarrv1.SettingsResponse], error) {
	a.calls++
	if a.calls <= a.failures {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("not yet"))
	}
	res := &sniffarrv1.SettingsResponse{AudioLanguages: a.tags, Probes: a.probes, Features: a.features}
	if a.retention > 0 {
		res.JournalRetention = durationpb.New(a.retention)
	}
	return connect.NewResponse(res), nil
}

func TestObtainSettings(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	russian, err := audio.Parse("ru")
	require.NoError(t, err)

	api := &settingsAPI{failures: 2, tags: []string{"ru"}, probes: true, retention: time.Hour, features: []*sniffarrv1.FeatureState{
		{Feature: sniffarrv1.Feature_FEATURE_CLASSIFIER, On: true},
	}}
	got, err := ObtainSettings(ctx, api, time.Millisecond, log)
	require.NoError(t, err)
	require.Equal(t, Settings{Languages: russian, Probes: true, Classifier: true, JournalRetention: time.Hour}, got, "on, whether working or not")
	require.Equal(t, 3, api.calls, "two failures, then the answer")

	api = &settingsAPI{}
	got, err = ObtainSettings(ctx, api, time.Millisecond, log)
	require.NoError(t, err)
	require.Empty(t, got.Languages, "the extension is off")
	require.False(t, got.Probes, "probes are off")
	require.False(t, got.Classifier, "an API that names no feature has them off")
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

	api.set(&sniffarrv1.SettingsResponse{AudioLanguages: []string{"ru"}, Probes: true, JournalRetention: durationpb.New(time.Hour)}, nil)
	require.Eventually(t, probe, 5*time.Second, time.Millisecond, "an API that started later probes")
	require.Equal(t, time.Hour, h.s.settings().JournalRetention, "the journal's retention comes with the settings")

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
