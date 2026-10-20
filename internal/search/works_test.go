package search

import (
	"context"
	"log/slog"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/database"
	titles "github.com/mikluko/sniffarr/internal/names"
	"github.com/mikluko/sniffarr/internal/rules"
	"github.com/mikluko/sniffarr/internal/sketch"
	"github.com/mikluko/sniffarr/internal/store"
)

func infohash(b byte) []byte {
	h := hash(b)
	return h[:]
}

// peers returns a sketch of n peers numbered from first.
func peers(k sketch.Key, first, n int) []byte {
	s := sketch.New()
	for i := first; i < first+n; i++ {
		s.Add(k.Hash(netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}), 6881)))
	}
	return s.Bytes()
}

// ranked holds two Dune releases, one with a Russian marker, Interstellar
// and two Severance episodes, with peers at now, and the tops computed.
func ranked(t *testing.T) *Browser {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	s, err := store.New(ctx, db, store.Options{Languages: []string{"ru"}})
	require.NoError(t, err)
	k, err := s.HashKey(ctx)
	require.NoError(t, err)
	add := func(b byte, name, kind string, ru store.Audio) {
		p := titles.Parse(name)
		ru.Language = "ru"
		_, _, err := s.Insert(ctx, store.Torrent{Hash: hash(b), Name: name, Size: int64(b) << 30, Discovery: "sample", SeenAt: now.Add(-time.Duration(b) * time.Hour),
			Files: []rules.File{{Path: name + ".mkv", Size: int64(b) << 30}}, Kind: kind, KindLayer: store.LayerRules,
			TitleKey: p.Key, WorkKey: p.Work, Year: p.Year, Season: p.Season, Audio: []store.Audio{ru}})
		require.NoError(t, err)
	}
	add(1, "Dune.Part.Two.2024.1080p.BluRay.x264", "movie", store.Audio{})
	add(2, "Dune.Part.Two.2024.2160p.BluRay.Rus", "movie", store.Audio{Marker: true, Probability: new(1.0), Layer: store.LayerRules})
	add(3, "Interstellar 2014 1080p BluRay x264", "movie", store.Audio{})
	add(5, "Severance.S02E01.1080p.WEB-DL", "series", store.Audio{})
	add(6, "Severance.S02E02.1080p.WEB-DL", "series", store.Audio{})
	_, _, err = s.MergePeers(ctx, []store.PeerSketch{
		{Hash: hash(1), Hour: now, Sketch: peers(k, 0, 100)},
		{Hash: hash(2), Hour: now, Sketch: peers(k, 100, 50)},
		{Hash: hash(3), Hour: now.Add(-2 * time.Hour), Sketch: peers(k, 0, 80)},
		{Hash: hash(3), Hour: now.Add(-30 * time.Hour), Sketch: peers(k, 1000, 40)},
		{Hash: hash(5), Hour: now, Sketch: peers(k, 0, 30)},
	})
	require.NoError(t, err)
	require.NoError(t, s.ComputeTop(ctx, now))
	return &Browser{Store: s, Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now }}
}

func TestBrowserHome(t *testing.T) {
	b := ranked(t)
	res, err := b.Home(context.Background(), connect.NewRequest(&sniffarrv1.HomeRequest{}))
	require.NoError(t, err)
	require.Equal(t, int64(5), res.Msg.GetTotal())
	require.Equal(t, int64(5), res.Msg.GetNewDay())
	require.Zero(t, res.Msg.GetPending())
}

func TestBrowserTop(t *testing.T) {
	ctx := context.Background()
	b := ranked(t)
	res, err := b.Top(ctx, connect.NewRequest(&sniffarrv1.TopRequest{Kind: sniffarrv1.Kind_KIND_MOVIE, Window: sniffarrv1.Window_WINDOW_DAY}))
	require.NoError(t, err)
	require.Equal(t, int64(2), res.Msg.GetTotal())
	require.Equal(t, now, res.Msg.GetComputedAt().AsTime())
	require.Len(t, res.Msg.GetEntries(), 2)
	dune, inter := res.Msg.GetEntries()[0], res.Msg.GetEntries()[1]
	require.Equal(t, int32(1), dune.GetRank())
	require.Equal(t, "y:dune part two 2024", dune.GetWork().GetRef().GetKey())
	require.Equal(t, sniffarrv1.Kind_KIND_MOVIE, dune.GetWork().GetRef().GetKind())
	require.Equal(t, "Dune Part Two", dune.GetWork().GetRef().GetTitle())
	require.Equal(t, int32(2024), dune.GetWork().GetRef().GetYear())
	require.Equal(t, int32(1), dune.GetWork().GetRef().GetRank())
	require.Equal(t, int64(2), dune.GetWork().GetReleases())
	require.InDelta(t, 150, dune.GetPeers(), 15)
	require.Nil(t, dune.Delta)
	require.Len(t, dune.GetWork().GetAudio(), 1)
	require.True(t, dune.GetWork().GetAudio()[0].GetMarker())
	require.Equal(t, now.Add(-2*time.Hour), dune.GetWork().GetFirstSeen().AsTime())
	require.Equal(t, int32(2), inter.GetRank())
	require.NotNil(t, inter.Delta)
	require.InDelta(t, 1.0, inter.GetDelta(), 0.25)

	res, err = b.Top(ctx, connect.NewRequest(&sniffarrv1.TopRequest{Kind: sniffarrv1.Kind_KIND_MOVIE, Voice: sniffarrv1.Voice_VOICE_MARKER}))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetEntries(), 1, "the voice admits the marked release only")
	require.InDelta(t, 50, res.Msg.GetEntries()[0].GetPeers(), 5)
	require.Equal(t, int64(1), res.Msg.GetEntries()[0].GetWork().GetReleases())

	res, err = b.Top(ctx, connect.NewRequest(&sniffarrv1.TopRequest{Kind: sniffarrv1.Kind_KIND_SERIES, Window: sniffarrv1.Window_WINDOW_MONTH, Limit: 1, Offset: 1}))
	require.NoError(t, err)
	require.Equal(t, int64(1), res.Msg.GetTotal())
	require.Empty(t, res.Msg.GetEntries(), "offset past the end")
	res, err = b.Top(ctx, connect.NewRequest(&sniffarrv1.TopRequest{Kind: sniffarrv1.Kind_KIND_MUSIC}))
	require.NoError(t, err)
	require.Empty(t, res.Msg.GetEntries())
	require.Nil(t, res.Msg.ComputedAt, "no music was ever ranked")

	for _, req := range []*sniffarrv1.TopRequest{
		{Kind: sniffarrv1.Kind_KIND_UNSPECIFIED},
		{Kind: sniffarrv1.Kind_KIND_BOOK},
		{Kind: sniffarrv1.Kind_KIND_MOVIE, Window: 9},
		{Kind: sniffarrv1.Kind_KIND_MOVIE, Voice: 9},
		{Kind: sniffarrv1.Kind_KIND_MOVIE, Limit: -1},
	} {
		_, err := b.Top(ctx, connect.NewRequest(req))
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "%v", req)
	}
}

func TestBrowserWork(t *testing.T) {
	ctx := context.Background()
	b := ranked(t)
	res, err := b.Work(ctx, connect.NewRequest(&sniffarrv1.WorkRequest{Key: "y:dune part two 2024", Window: sniffarrv1.Window_WINDOW_DAY}))
	require.NoError(t, err)
	require.Equal(t, "Dune Part Two", res.Msg.GetWork().GetRef().GetTitle())
	require.InDelta(t, 150, res.Msg.GetPeers(), 15)
	require.Equal(t, int32(1), res.Msg.GetRank(), "first in the day's top")
	require.Nil(t, res.Msg.Delta)
	require.Len(t, res.Msg.GetReleases(), 2)
	require.Equal(t, infohash(1), res.Msg.GetReleases()[0].GetRelease().GetInfohash(), "more peers first")
	require.InDelta(t, 100, res.Msg.GetReleases()[0].GetPeers(), 10)
	require.InDelta(t, 100, res.Msg.GetReleases()[0].GetRelease().GetPeersWeek(), 10, "a release carries its week's peers")
	require.NotEmpty(t, res.Msg.GetReleases()[0].GetRelease().GetMagnet())

	res, err = b.Work(ctx, connect.NewRequest(&sniffarrv1.WorkRequest{Key: "y:interstellar 2014", Window: sniffarrv1.Window_WINDOW_DAY}))
	require.NoError(t, err)
	require.Equal(t, int32(2), res.Msg.GetRank())
	require.NotNil(t, res.Msg.Delta)
	require.InDelta(t, 1.0, res.Msg.GetDelta(), 0.25)
	res, err = b.Work(ctx, connect.NewRequest(&sniffarrv1.WorkRequest{Key: "y:interstellar 2014", Voice: sniffarrv1.Voice_VOICE_MARKER}))
	require.NoError(t, err)
	require.Zero(t, res.Msg.GetRank(), "outside the top of that voice")
	require.Nil(t, res.Msg.Delta)

	res, err = b.Work(ctx, connect.NewRequest(&sniffarrv1.WorkRequest{Key: "s:severance/2"}))
	require.NoError(t, err)
	require.Equal(t, int32(2), res.Msg.GetWork().GetRef().GetSeason())
	require.Len(t, res.Msg.GetReleases(), 2)
	require.Zero(t, res.Msg.GetReleases()[1].GetPeers(), "an episode nobody shares is listed last")

	_, err = b.Work(ctx, connect.NewRequest(&sniffarrv1.WorkRequest{Key: "y:nothing 1999"}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = b.Work(ctx, connect.NewRequest(&sniffarrv1.WorkRequest{}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestBrowserPageSortAndWorks(t *testing.T) {
	ctx := context.Background()
	b := ranked(t)
	res, err := b.Page(ctx, connect.NewRequest(&sniffarrv1.PageRequest{Sort: sniffarrv1.Sort_SORT_PEERS, Limit: 2}))
	require.NoError(t, err)
	require.Equal(t, []string{"Interstellar 2014 1080p BluRay x264", "Dune.Part.Two.2024.1080p.BluRay.x264"}, names(res.Msg.GetReleases()), "120 then 100 peers over the week")
	require.InDelta(t, 120, res.Msg.GetReleases()[0].GetPeersWeek(), 12, "the page shows what it sorts by")
	require.InDelta(t, 100, res.Msg.GetReleases()[1].GetPeersWeek(), 10)
	require.Empty(t, res.Msg.GetWorks(), "no works without a query")
	res, err = b.Page(ctx, connect.NewRequest(&sniffarrv1.PageRequest{Sort: sniffarrv1.Sort_SORT_SIZE, Limit: 1}))
	require.NoError(t, err)
	require.Equal(t, []string{"Severance.S02E02.1080p.WEB-DL"}, names(res.Msg.GetReleases()))

	res, err = b.Page(ctx, connect.NewRequest(&sniffarrv1.PageRequest{Query: "1080p"}))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetWorks(), 3)
	require.Equal(t, "y:dune part two 2024", res.Msg.GetWorks()[0].GetWork().GetKey())
	require.InDelta(t, 150, res.Msg.GetWorks()[0].GetPeers(), 15, "the work's peers, not the matched release's")
	require.Equal(t, int64(2), res.Msg.GetWorks()[0].GetReleases())
	require.Equal(t, int32(1), res.Msg.GetWorks()[0].GetWork().GetRank())
	res, err = b.Page(ctx, connect.NewRequest(&sniffarrv1.PageRequest{Query: "1080p", Offset: 1}))
	require.NoError(t, err)
	require.Empty(t, res.Msg.GetWorks(), "the first page only")
	_, err = b.Page(ctx, connect.NewRequest(&sniffarrv1.PageRequest{Sort: 9}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestBrowserCardPeersAndWork(t *testing.T) {
	ctx := context.Background()
	b := ranked(t)
	res, err := b.Card(ctx, connect.NewRequest(&sniffarrv1.CardRequest{Infohash: infohash(2)}))
	require.NoError(t, err)
	require.InDelta(t, 50, res.Msg.GetPeers().GetDay(), 5)
	require.InDelta(t, 50, res.Msg.GetPeers().GetWeek(), 5)
	require.InDelta(t, 50, res.Msg.GetPeers().GetMonth(), 5)
	require.Equal(t, "y:dune part two 2024", res.Msg.GetWork().GetKey())
	require.Equal(t, int32(1), res.Msg.GetWork().GetRank())
	require.Equal(t, int64(2), res.Msg.GetReleases())
	require.Equal(t, int32(2), res.Msg.GetReleaseRank(), "fewer peers than the other Dune")

	res, err = b.Card(ctx, connect.NewRequest(&sniffarrv1.CardRequest{Infohash: infohash(6)}))
	require.NoError(t, err)
	require.Zero(t, res.Msg.GetPeers().GetMonth())
	require.Equal(t, "s:severance/2", res.Msg.GetWork().GetKey())
	require.Equal(t, int32(2), res.Msg.GetReleaseRank())
}
