package search

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/database"
	"github.com/mikluko/sniffarr/internal/rules"
	"github.com/mikluko/sniffarr/internal/store"
)

var now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func hash(b byte) store.Hash { return store.Hash{b} }

// browser holds a rules movie with a Russian marker and two files, a
// classified web series, a torrent of unknown kind seen a day later, and an
// adult one; two hashes are rejected as small and 250 tokens are spent today.
func browser(t *testing.T) *Browser {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	s, err := store.New(ctx, db, store.Options{Languages: []string{"ru"}})
	require.NoError(t, err)
	for _, tr := range []store.Torrent{
		{Hash: hash(1), Name: "Alien.1979.Rus.1080p", Size: 8 << 30, Discovery: "announce", SeenAt: now.Add(-time.Hour), Kind: "movie", KindLayer: store.LayerRules,
			Files: []rules.File{{Path: "Alien.mkv", Size: 8 << 30}, {Path: "Alien.srt", Size: 1 << 10}},
			Audio: []store.Audio{{Language: "ru", Marker: true, Probability: new(1.0), Layer: store.LayerRules}}},
		{Hash: hash(2), Name: "Severance S02", Size: 40 << 30, Discovery: "sample", SeenAt: now.Add(-30 * time.Hour), Audio: []store.Audio{{Language: "ru"}}},
		{Hash: hash(3), Name: "Чужой", Size: 2 << 30, Discovery: "sample", SeenAt: now.Add(-2 * time.Hour), Audio: []store.Audio{{Language: "ru", ScriptHint: true}}},
		{Hash: hash(5), Name: "Something", Size: 90 << 30, Discovery: "sample", SeenAt: now, Kind: "adult", KindLayer: store.LayerRules},
	} {
		_, _, err := s.Insert(ctx, tr)
		require.NoError(t, err)
	}
	r, err := s.Get(ctx, hash(2))
	require.NoError(t, err)
	_, err = s.Classify(ctx, r.ID, store.Classification{Kind: "series", KindConfidence: 0.9, Source: "web", SourceConfidence: 0.8, Audio: map[string]float64{"ru": 0.7}}, now)
	require.NoError(t, err)
	for _, h := range []store.Hash{hash(10), hash(11)} {
		_, err := s.Reject(ctx, h, rules.Small, now)
		require.NoError(t, err)
	}
	require.NoError(t, s.AddTokens(ctx, now.Format(time.DateOnly), 250))
	return &Browser{Store: s, Log: slog.New(slog.DiscardHandler), DailyTokens: 1000, Now: func() time.Time { return now }}
}

func names(rs []*sniffarrv1.Release) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.GetName())
	}
	return out
}

func TestBrowserPage(t *testing.T) {
	b := browser(t)
	ctx := context.Background()
	for _, tt := range []struct {
		name  string
		req   *sniffarrv1.PageRequest
		want  []string
		total int64
	}{
		{name: "latest first without adult", req: &sniffarrv1.PageRequest{}, want: []string{"Alien.1979.Rus.1080p", "Чужой", "Severance S02"}, total: 3},
		{name: "a page", req: &sniffarrv1.PageRequest{Offset: 1, Limit: 1}, want: []string{"Чужой"}, total: 3},
		{name: "text", req: &sniffarrv1.PageRequest{Query: "alien mkv"}, want: []string{"Alien.1979.Rus.1080p"}, total: 1},
		{name: "kind", req: &sniffarrv1.PageRequest{Kind: sniffarrv1.Kind_KIND_ADULT}, want: []string{"Something"}, total: 1},
		{name: "unknown kind", req: &sniffarrv1.PageRequest{UnknownKind: true}, want: []string{"Чужой"}, total: 1},
		{name: "marker", req: &sniffarrv1.PageRequest{AudioLanguage: new("ru"), AudioMarker: true}, want: []string{"Alien.1979.Rus.1080p"}, total: 1},
		{name: "probability", req: &sniffarrv1.PageRequest{AudioLanguage: new("ru"), MinAudioProbability: new(float32(0.5))}, want: []string{"Alien.1979.Rus.1080p", "Severance S02"}, total: 2},
		{name: "source", req: &sniffarrv1.PageRequest{Source: sniffarrv1.VideoSource_VIDEO_SOURCE_WEB}, want: []string{"Severance S02"}, total: 1},
		{name: "layer", req: &sniffarrv1.PageRequest{Layer: sniffarrv1.Layer_LAYER_CLASSIFIER}, want: []string{"Severance S02"}, total: 1},
		{name: "size", req: &sniffarrv1.PageRequest{MinSize: 5 << 30}, want: []string{"Alien.1979.Rus.1080p", "Severance S02"}, total: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res, err := b.Page(ctx, connect.NewRequest(tt.req))
			require.NoError(t, err)
			require.Equal(t, tt.want, names(res.Msg.GetReleases()))
			require.Equal(t, tt.total, res.Msg.GetTotal())
		})
	}
	for name, req := range map[string]*sniffarrv1.PageRequest{
		"negative limit":       {Limit: -1},
		"negative offset":      {Offset: -1},
		"unknown kind and one": {UnknownKind: true, Kind: sniffarrv1.Kind_KIND_MOVIE},
		"marker without lang":  {AudioMarker: true},
		"unknown source":       {Source: 99},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := b.Page(ctx, connect.NewRequest(req))
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		})
	}
}

func TestBrowserCard(t *testing.T) {
	b := browser(t)
	ctx := context.Background()
	h := hash(1)
	res, err := b.Card(ctx, connect.NewRequest(&sniffarrv1.CardRequest{Infohash: h[:]}))
	require.NoError(t, err)
	require.Equal(t, "Alien.1979.Rus.1080p", res.Msg.GetRelease().GetName())
	require.Equal(t, sniffarrv1.Source_SOURCE_ANNOUNCE, res.Msg.GetDiscovery())
	require.Len(t, res.Msg.GetFiles(), 2)
	require.Equal(t, "Alien.mkv", res.Msg.GetFiles()[0].GetPath())
	require.True(t, res.Msg.GetRelease().GetAudio()[0].GetMarker())

	h = hash(9)
	_, err = b.Card(ctx, connect.NewRequest(&sniffarrv1.CardRequest{Infohash: h[:]}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = b.Card(ctx, connect.NewRequest(&sniffarrv1.CardRequest{Infohash: []byte{1}}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestBrowserStats(t *testing.T) {
	b := browser(t)
	ctx := context.Background()
	res, err := b.Stats(ctx, connect.NewRequest(&sniffarrv1.StatsRequest{Days: 3}))
	require.NoError(t, err)
	m := res.Msg
	require.EqualValues(t, 4, m.GetTotal())
	require.Equal(t, []*sniffarrv1.KindCount{
		{Kind: sniffarrv1.Kind_KIND_UNSPECIFIED, Count: 1}, {Kind: sniffarrv1.Kind_KIND_ADULT, Count: 1},
		{Kind: sniffarrv1.Kind_KIND_MOVIE, Count: 1}, {Kind: sniffarrv1.Kind_KIND_SERIES, Count: 1},
	}, m.GetKinds())
	require.Equal(t, []*sniffarrv1.LayerCount{
		{Layer: sniffarrv1.Layer_LAYER_UNSPECIFIED, Count: 1}, {Layer: sniffarrv1.Layer_LAYER_CLASSIFIER, Count: 1}, {Layer: sniffarrv1.Layer_LAYER_RULES, Count: 2},
	}, m.GetLayers())
	require.Equal(t, []*sniffarrv1.SourceCount{
		{Source: sniffarrv1.VideoSource_VIDEO_SOURCE_UNSPECIFIED, Count: 3}, {Source: sniffarrv1.VideoSource_VIDEO_SOURCE_WEB, Count: 1},
	}, m.GetSources())
	require.Equal(t, []*sniffarrv1.AudioCounts{{Language: "ru", Marker: 1, Likely: 1}}, m.GetAudio())
	require.Equal(t, []*sniffarrv1.RejectionCount{{Reason: sniffarrv1.Rejection_REJECTION_SMALL, Count: 2}}, m.GetRejected())
	require.EqualValues(t, 1, m.GetPending(), "the script-hinted torrent of unknown kind")
	require.Equal(t, []int64{3, 1, 0}, m.GetDays())
	require.EqualValues(t, 250, m.GetTokensUsedDay())
	require.EqualValues(t, 1000, m.GetTokensLimitDay())

	_, err = b.Stats(ctx, connect.NewRequest(&sniffarrv1.StatsRequest{Days: MaxStatsDays + 1}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}
