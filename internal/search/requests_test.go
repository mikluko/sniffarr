package search

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/classify"
	"github.com/mikluko/sniffarr/internal/rules"
	"github.com/mikluko/sniffarr/internal/store"
)

func byHash(b byte) *sniffarrv1.Releases {
	return &sniffarrv1.Releases{Of: &sniffarrv1.Releases_Infohash{Infohash: infohash(b)}}
}

func byWork(id string) *sniffarrv1.Releases {
	return &sniffarrv1.Releases{Of: &sniffarrv1.Releases_Work{Work: id}}
}

func TestBrowserRefreshPeers(t *testing.T) {
	ctx := context.Background()
	b, db := rankedDB(t)
	res, err := b.RefreshPeers(ctx, connect.NewRequest(&sniffarrv1.RefreshPeersRequest{Releases: byHash(7)}))
	require.NoError(t, err)
	require.Equal(t, int32(1), res.Msg.GetReleases())
	res, err = b.RefreshPeers(ctx, connect.NewRequest(&sniffarrv1.RefreshPeersRequest{Releases: byWork("m693134")}))
	require.NoError(t, err)
	require.Equal(t, int32(3), res.Msg.GetReleases(), "every release of the work")
	q, err := store.NewTasks(ctx, db, store.TaskOptions{})
	require.NoError(t, err)
	got, _, err := q.Lease(ctx, 1, 10, now, time.Minute)
	require.NoError(t, err)
	require.ElementsMatch(t, []store.Task{{Kind: store.TaskPeers, Hash: hash(7)}, {Kind: store.TaskPeers, Hash: hash(1)},
		{Kind: store.TaskPeers, Hash: hash(2)}, {Kind: store.TaskPeers, Hash: hash(4)}}, got)

	for _, tt := range []struct {
		name string
		r    *sniffarrv1.Releases
		code connect.Code
	}{
		{"not stored", byHash(99), connect.CodeNotFound},
		{"no work", byWork("m1"), connect.CodeNotFound},
		{"bad work", byWork("x1"), connect.CodeInvalidArgument},
		{"short infohash", &sniffarrv1.Releases{Of: &sniffarrv1.Releases_Infohash{Infohash: []byte{1}}}, connect.CodeInvalidArgument},
		{"none", nil, connect.CodeInvalidArgument},
	} {
		_, err := b.RefreshPeers(ctx, connect.NewRequest(&sniffarrv1.RefreshPeersRequest{Releases: tt.r}))
		require.Equal(t, tt.code, connect.CodeOf(err), tt.name)
	}
}

func TestBrowserRequestProbe(t *testing.T) {
	ctx := context.Background()
	b := ranked(t)
	b.Probes = true
	require.NoError(t, b.Store.ProbeDone(ctx, hash(1), store.Probe{Outcome: store.ProbeUnknown}, now))
	res, err := b.RequestProbe(ctx, connect.NewRequest(&sniffarrv1.RequestProbeRequest{Releases: byWork("m693134")}))
	require.NoError(t, err)
	require.Equal(t, int32(3), res.Msg.GetReleases())
	c, ok, err := b.Store.LeaseProbe(ctx, 1, now, time.Minute, false, 0)
	require.NoError(t, err)
	require.True(t, ok)
	require.Contains(t, []store.Hash{hash(1), hash(2), hash(4)}, c.Hash, "a probed release too")
	require.True(t, c.Wanted)

	_, err = b.RequestProbe(ctx, connect.NewRequest(&sniffarrv1.RequestProbeRequest{Releases: byHash(99)}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	_, _, err = b.Store.Insert(ctx, store.Torrent{Hash: hash(42), Name: "Heat.1995.DVDRip", Size: 1 << 30, Discovery: "sample", SeenAt: now,
		Files: []rules.File{{Path: "Heat.1995.DVDRip.avi", Size: 1 << 30}}, Kind: "movie", KindLayer: store.LayerRules})
	require.NoError(t, err)
	res, err = b.RequestProbe(ctx, connect.NewRequest(&sniffarrv1.RequestProbeRequest{Releases: byHash(42)}))
	require.NoError(t, err, "a stored release a probe cannot read is found")
	require.Zero(t, res.Msg.GetReleases(), "and not to be probed")
}

func TestBrowserRequestProbeOff(t *testing.T) {
	ctx := context.Background()
	b := ranked(t)
	_, err := b.RequestProbe(ctx, connect.NewRequest(&sniffarrv1.RequestProbeRequest{Releases: byWork("m693134")}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.ErrorContains(t, err, "probes are off")
	_, ok, err := b.Store.LeaseProbe(ctx, 1, now, time.Minute, false, 0)
	require.NoError(t, err)
	require.False(t, ok, "nothing queued")
}

func TestBrowserStatsQueue(t *testing.T) {
	ctx := context.Background()
	b, db := rankedDB(t)
	res, err := b.Stats(ctx, connect.NewRequest(&sniffarrv1.StatsRequest{}))
	require.NoError(t, err)
	require.Nil(t, res.Msg.GetQueueProbes(), "no queue without Tasks")

	b.Tasks, err = store.NewTasks(ctx, db, store.TaskOptions{})
	require.NoError(t, err)
	b.Probes = true
	_, err = b.RequestProbe(ctx, connect.NewRequest(&sniffarrv1.RequestProbeRequest{Releases: byHash(1)}))
	require.NoError(t, err)
	res, err = b.Stats(ctx, connect.NewRequest(&sniffarrv1.StatsRequest{}))
	require.NoError(t, err)
	q := res.Msg.GetQueueProbes().GetLevels()
	require.Len(t, q, 5)
	require.Equal(t, int32(0), q[0].GetPriority())
	require.Equal(t, int64(1), q[0].GetTasks())
	require.Nil(t, q[0].GetEta(), "no pace yet")
	require.Len(t, res.Msg.GetQueueFetches().GetLevels(), 5)
	require.NotNil(t, res.Msg.GetQueueFetches().GetLevels()[0].GetEta(), "nothing to wait for")

	b.Tasks.Finished(store.TaskProbe, now.Add(-time.Minute))
	b.Tasks.Finished(store.TaskProbe, now.Add(-time.Minute))
	res, err = b.Stats(ctx, connect.NewRequest(&sniffarrv1.StatsRequest{}))
	require.NoError(t, err)
	require.InDelta(t, 2.0, res.Msg.GetQueueProbes().GetPerMinute(), 1e-9)
	require.Equal(t, 30*time.Second, res.Msg.GetQueueProbes().GetLevels()[4].GetEta().AsDuration())
	require.Zero(t, res.Msg.GetQueueLookups().GetPerMinute())
}

// fakeMatcher matches a key to the TMDB ID in ids, or to nothing, recording
// the trigger, or fails with err.
type fakeMatcher struct {
	store    *store.Store
	ids      map[string]int
	err      error
	triggers []string
}

func (f *fakeMatcher) Matchable(ctx context.Context, keys []string) ([]store.TitleKey, error) {
	return f.store.UnmatchedTitles(ctx, keys, now.Add(time.Second), len(keys))
}

func (f *fakeMatcher) Match(ctx context.Context, k store.TitleKey, trigger string) error {
	if f.err != nil {
		return f.err
	}
	f.triggers = append(f.triggers, trigger)
	found := store.Found{Trigger: trigger}
	if id, ok := f.ids[k.Key]; ok {
		found.Hit, found.Confidence, found.Layer = &store.Hit{Type: store.TypeOf(k.Kind), TMDBID: id, Lang: "en", Title: k.Key}, 1, store.LayerTMDB
	}
	_, _, err := f.store.RecordMatch(ctx, k.Key, found, now)
	return err
}

func TestBrowserRequestMatch(t *testing.T) {
	ctx := context.Background()
	b := ranked(t)
	_, err := b.RequestMatch(ctx, connect.NewRequest(&sniffarrv1.RequestMatchRequest{Releases: byHash(7)}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "without TMDB")

	m := &fakeMatcher{store: b.Store}
	b.Matcher = m
	res, err := b.RequestMatch(ctx, connect.NewRequest(&sniffarrv1.RequestMatchRequest{Releases: byHash(7)}))
	require.NoError(t, err)
	require.Equal(t, int32(1), res.Msg.GetTitles())
	require.Empty(t, res.Msg.GetWorks(), "still nothing")
	res, err = b.RequestMatch(ctx, connect.NewRequest(&sniffarrv1.RequestMatchRequest{Releases: byHash(7)}))
	require.NoError(t, err)
	require.Equal(t, int32(1), res.Msg.GetTitles(), "nothing found is looked up again at once")

	m.ids = map[string]int{"y:unmatched 2020": 4242}
	res, err = b.RequestMatch(ctx, connect.NewRequest(&sniffarrv1.RequestMatchRequest{Releases: byHash(7), Lang: "en"}))
	require.NoError(t, err)
	require.Equal(t, int32(1), res.Msg.GetTitles())
	require.Len(t, res.Msg.GetWorks(), 1)
	require.Equal(t, "m4242", res.Msg.GetWorks()[0].GetId())
	require.Equal(t, []string{store.TriggerRequest, store.TriggerRequest, store.TriggerRequest}, m.triggers)

	res, err = b.RequestMatch(ctx, connect.NewRequest(&sniffarrv1.RequestMatchRequest{Releases: byWork("m693134"), Lang: "ru"}))
	require.NoError(t, err)
	require.Zero(t, res.Msg.GetTitles(), "a matched key is not looked up")
	require.Len(t, res.Msg.GetWorks(), 1)
	require.Equal(t, "Дюна: Часть вторая", res.Msg.GetWorks()[0].GetTmdb().GetTitle())

	_, _, err = b.Store.Insert(ctx, store.Torrent{Hash: hash(43), Name: "Heat.1995.1080p", Size: 1 << 30, Discovery: "sample", SeenAt: now,
		Kind: "movie", KindLayer: store.LayerRules, TitleKey: "y:heat 1995"})
	require.NoError(t, err)
	m.err = classify.ErrBudget
	_, err = b.RequestMatch(ctx, connect.NewRequest(&sniffarrv1.RequestMatchRequest{Releases: byHash(43)}))
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	m.err = errors.New("tmdb: 503")
	_, err = b.RequestMatch(ctx, connect.NewRequest(&sniffarrv1.RequestMatchRequest{Releases: byHash(43)}))
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))

	for _, tt := range []struct {
		name string
		r    *sniffarrv1.Releases
		lang string
		code connect.Code
	}{
		{"not stored", byHash(99), "", connect.CodeNotFound},
		{"no work", byWork("m1"), "", connect.CodeNotFound},
		{"none", nil, "", connect.CodeInvalidArgument},
		{"bad lang", byHash(7), "de", connect.CodeInvalidArgument},
	} {
		_, err := b.RequestMatch(ctx, connect.NewRequest(&sniffarrv1.RequestMatchRequest{Releases: tt.r, Lang: tt.lang}))
		require.Equal(t, tt.code, connect.CodeOf(err), tt.name)
	}
}
