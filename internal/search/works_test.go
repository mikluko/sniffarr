package search

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
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

// fakeTitles answers SearchWorks by recording the hits of hits for the
// query and type, and fails while err is set.
type fakeTitles struct {
	store *store.Store
	hits  map[string][]store.Hit
	err   error
	mu    sync.Mutex
	asked []string
}

func (f *fakeTitles) SearchWorks(ctx context.Context, typ, query, lang string) ([]int64, error) {
	f.mu.Lock()
	f.asked = append(f.asked, typ+" "+query+" "+lang)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	q := strings.ToLower(query)
	return f.store.RecordSearch(ctx, typ, q, f.hits[typ+" "+q], now)
}

// ranked holds two Dune releases, one with a Russian marker, and a third
// under the Russian title, all matched to Dune: Part Two; Interstellar;
// two Severance episodes matched to Severance; and an unmatched torrent,
// with peers at now, and the tops computed.
func ranked(t *testing.T) *Browser {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	s, err := store.New(ctx, db, store.Options{Languages: []string{"ru"}, TMDB: true})
	require.NoError(t, err)
	k, err := s.HashKey(ctx)
	require.NoError(t, err)
	add := func(b byte, name, kind string, ru store.Audio) {
		p := titles.Parse(name)
		ru.Language = "ru"
		_, _, err := s.Insert(ctx, store.Torrent{Hash: hash(b), Name: name, Size: int64(b) << 30, Discovery: "sample", SeenAt: now.Add(-time.Duration(b) * time.Hour),
			Files: []rules.File{{Path: name + ".mkv", Size: int64(b) << 30}}, Kind: kind, KindLayer: store.LayerRules,
			TitleKey: p.Key, Year: p.Year, Season: p.Season, Episode: p.Episode, Quality: p.Quality, Audio: []store.Audio{ru}})
		require.NoError(t, err)
	}
	add(1, "Dune.Part.Two.2024.1080p.BluRay.x264", "movie", store.Audio{})
	add(2, "Dune.Part.Two.2024.2160p.BluRay.Rus", "movie", store.Audio{Marker: true, Mark: "Rus", Probability: new(1.0), Layer: store.LayerRules})
	add(3, "Interstellar 2014 1080p BluRay x264", "movie", store.Audio{Probability: new(0.6), Layer: store.LayerJev})
	add(4, "Дюна: Часть вторая (2024) WEB-DL 720p", "movie", store.Audio{})
	add(5, "Severance.S02E01.1080p.WEB-DL", "series", store.Audio{})
	add(6, "Severance.S01E02.720p.WEB-DL", "series", store.Audio{})
	add(7, "Unmatched.2020.1080p.WEB-DL", "movie", store.Audio{})
	match := func(key, typ string, id int, en, ru string, pop float64) {
		w, _, err := s.RecordMatch(ctx, key, store.Found{Hit: &store.Hit{Type: typ, TMDBID: id, Lang: "en", Title: en, OriginalTitle: en, Popularity: pop},
			Confidence: 0.9, Layer: store.LayerTMDB}, now.Add(-time.Hour))
		require.NoError(t, err)
		require.NoError(t, s.RecordDetails(ctx, w, store.Details{Type: typ, OriginalTitle: en, PosterPath: "/" + en + ".jpg",
			Texts: map[string]store.Text{"en": {Title: en}, "ru": {Title: ru}}}, []store.ExternalID{{Provider: store.ProviderIMDb, ID: "tt" + en}}, now))
	}
	match("y:dune part two 2024", store.TypeMovie, 693134, "Dune: Part Two", "Дюна: Часть вторая", 50)
	match("y:дюна часть вторая 2024", store.TypeMovie, 693134, "Dune: Part Two", "Дюна: Часть вторая", 50)
	match("y:interstellar 2014", store.TypeMovie, 157336, "Interstellar", "Интерстеллар", 40)
	match("s:severance", store.TypeTV, 95396, "Severance", "Разделение", 30)
	_, _, err = s.MergePeers(ctx, []store.PeerSketch{
		{Hash: hash(1), Hour: now, Sketch: peers(k, 0, 100)},
		{Hash: hash(2), Hour: now, Sketch: peers(k, 100, 50)},
		{Hash: hash(3), Hour: now.Add(-2 * time.Hour), Sketch: peers(k, 0, 80)},
		{Hash: hash(3), Hour: now.Add(-30 * time.Hour), Sketch: peers(k, 1000, 40)},
		{Hash: hash(4), Hour: now, Sketch: peers(k, 2000, 10)},
		{Hash: hash(5), Hour: now, Sketch: peers(k, 0, 30)},
	})
	require.NoError(t, err)
	require.NoError(t, s.ComputeTop(ctx, now))
	return &Browser{Store: s, Log: slog.New(slog.DiscardHandler), Languages: []string{"ru"}, Now: func() time.Time { return now }}
}

func TestBrowserHome(t *testing.T) {
	b := ranked(t)
	res, err := b.Home(context.Background(), connect.NewRequest(&sniffarrv1.HomeRequest{}))
	require.NoError(t, err)
	require.Equal(t, int64(7), res.Msg.GetTotal())
	require.Equal(t, int64(7), res.Msg.GetNewDay())
	require.Zero(t, res.Msg.GetPending())
}

func TestBrowserTop(t *testing.T) {
	ctx := context.Background()
	b := ranked(t)
	res, err := b.Top(ctx, connect.NewRequest(&sniffarrv1.TopRequest{Kind: sniffarrv1.Kind_KIND_MOVIE, Window: sniffarrv1.Window_WINDOW_DAY, Lang: "ru"}))
	require.NoError(t, err)
	require.Equal(t, int64(2), res.Msg.GetTotal())
	require.Equal(t, now, res.Msg.GetComputedAt().AsTime())
	require.Len(t, res.Msg.GetEntries(), 2)
	dune, inter := res.Msg.GetEntries()[0], res.Msg.GetEntries()[1]
	require.Equal(t, int32(1), dune.GetRank())
	w := dune.GetWork()
	require.Equal(t, "m693134", w.GetId())
	require.Equal(t, sniffarrv1.Kind_KIND_MOVIE, w.GetKind())
	require.Equal(t, "Дюна: Часть вторая", w.GetTmdb().GetTitle(), "in the request's language")
	require.Equal(t, "tt"+"Dune: Part Two", w.GetTmdb().GetImdbId())
	require.Equal(t, int32(1), w.GetRank())
	require.Equal(t, int64(3), w.GetReleases(), "both title keys' releases")
	require.InDelta(t, 160, w.GetPeers(), 16, "the work's 7-day peers")
	require.InDelta(t, 160, dune.GetPeers(), 16)
	require.Nil(t, dune.Delta)
	require.Equal(t, []*sniffarrv1.WorkAudio{{Language: "ru", Marked: 1, Marks: []string{"Rus"}}}, dune.GetAudio())
	require.Equal(t, now.Add(-4*time.Hour), dune.GetFirstSeen().AsTime())
	require.Equal(t, int32(2), inter.GetRank())
	require.NotNil(t, inter.Delta)
	require.InDelta(t, 1.0, inter.GetDelta(), 0.25)

	res, err = b.Top(ctx, connect.NewRequest(&sniffarrv1.TopRequest{Kind: sniffarrv1.Kind_KIND_MOVIE, Voice: sniffarrv1.Voice_VOICE_MARKER}))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetEntries(), 1, "the voice admits the marked release only")
	require.InDelta(t, 50, res.Msg.GetEntries()[0].GetPeers(), 5)
	require.Equal(t, int64(1), res.Msg.GetEntries()[0].GetWork().GetReleases())
	res, err = b.Top(ctx, connect.NewRequest(&sniffarrv1.TopRequest{Kind: sniffarrv1.Kind_KIND_MOVIE, Voice: sniffarrv1.Voice_VOICE_LIKELY}))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetEntries(), 2, "p50 admits the marker and the likely track")
	res, err = b.Top(ctx, connect.NewRequest(&sniffarrv1.TopRequest{Kind: sniffarrv1.Kind_KIND_MOVIE, Voice: sniffarrv1.Voice_VOICE_LIKELY, VoiceLanguage: "uk"}))
	require.NoError(t, err)
	require.Empty(t, res.Msg.GetEntries(), "a language the server lacks")

	res, err = b.Top(ctx, connect.NewRequest(&sniffarrv1.TopRequest{Kind: sniffarrv1.Kind_KIND_SERIES, Window: sniffarrv1.Window_WINDOW_MONTH, Limit: 1, Offset: 1}))
	require.NoError(t, err)
	require.Equal(t, int64(1), res.Msg.GetTotal())
	require.Empty(t, res.Msg.GetEntries(), "offset past the end")

	for _, req := range []*sniffarrv1.TopRequest{
		{Kind: sniffarrv1.Kind_KIND_UNSPECIFIED},
		{Kind: sniffarrv1.Kind_KIND_MUSIC},
		{Kind: sniffarrv1.Kind_KIND_MOVIE, Window: 9},
		{Kind: sniffarrv1.Kind_KIND_MOVIE, Voice: 9},
		{Kind: sniffarrv1.Kind_KIND_MOVIE, Limit: -1},
		{Kind: sniffarrv1.Kind_KIND_MOVIE, Lang: "xx"},
	} {
		_, err := b.Top(ctx, connect.NewRequest(req))
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "%v", req)
	}
}

func TestBrowserTitle(t *testing.T) {
	ctx := context.Background()
	b := ranked(t)
	res, err := b.Title(ctx, connect.NewRequest(&sniffarrv1.TitleRequest{Id: "m693134"}))
	require.NoError(t, err)
	require.Equal(t, "Dune: Part Two", res.Msg.GetWork().GetTmdb().GetTitle())
	require.Equal(t, int64(3), res.Msg.GetWork().GetReleases())
	require.Equal(t, &sniffarrv1.WindowRanks{Day: 1, Week: 1, Month: 1}, res.Msg.GetRanks())
	require.InDelta(t, 160, res.Msg.GetPeers().GetDay(), 16)
	require.InDelta(t, 160, res.Msg.GetPeers().GetMonth(), 16)
	require.Equal(t, []*sniffarrv1.WorkAudio{{Language: "ru", Marked: 1, Marks: []string{"Rus"}}}, res.Msg.GetAudio())

	res, err = b.Title(ctx, connect.NewRequest(&sniffarrv1.TitleRequest{Id: "tv95396", Lang: "ru"}))
	require.NoError(t, err)
	require.Equal(t, "Разделение", res.Msg.GetWork().GetTmdb().GetTitle())
	require.Equal(t, sniffarrv1.Kind_KIND_SERIES, res.Msg.GetWork().GetKind())
	require.Equal(t, int32(1), res.Msg.GetRanks().GetWeek())

	_, err = b.Title(ctx, connect.NewRequest(&sniffarrv1.TitleRequest{Id: "tv693134"}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err), "movies and series are numbered apart")
	for _, id := range []string{"", "693134", "m", "m-1", "m01", "x1"} {
		_, err = b.Title(ctx, connect.NewRequest(&sniffarrv1.TitleRequest{Id: id}))
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), id)
	}
}

func TestBrowserTitleReleases(t *testing.T) {
	ctx := context.Background()
	b := ranked(t)
	get := func(req *sniffarrv1.TitleReleasesRequest) *sniffarrv1.TitleReleasesResponse {
		t.Helper()
		res, err := b.TitleReleases(ctx, connect.NewRequest(req))
		require.NoError(t, err)
		return res.Msg
	}
	res := get(&sniffarrv1.TitleReleasesRequest{Id: "m693134"})
	require.Equal(t, []string{"Dune.Part.Two.2024.1080p.BluRay.x264", "Dune.Part.Two.2024.2160p.BluRay.Rus", "Дюна: Часть вторая (2024) WEB-DL 720p"},
		relNames(res.GetReleases()), "most peers first, under both title keys")
	require.Equal(t, int64(3), res.GetTotal())
	require.Equal(t, int64(3), res.GetMatched())
	require.Empty(t, res.GetSeasons())
	require.Equal(t, sniffarrv1.Quality_QUALITY_1080P, res.GetReleases()[0].GetQuality())
	require.InDelta(t, 100, res.GetReleases()[0].GetPeersWeek(), 10)
	require.Nil(t, res.GetReleases()[0].GetTmdb())

	res = get(&sniffarrv1.TitleReleasesRequest{Id: "m693134", Quality: sniffarrv1.Quality_QUALITY_2160P})
	require.Equal(t, []string{"Dune.Part.Two.2024.2160p.BluRay.Rus"}, relNames(res.GetReleases()))
	require.Equal(t, int64(3), res.GetTotal())
	require.Equal(t, int64(1), res.GetMatched())
	res = get(&sniffarrv1.TitleReleasesRequest{Id: "m693134", AudioLanguage: new("ru"), Voice: sniffarrv1.Voice_VOICE_MARKER})
	require.Equal(t, []string{"Dune.Part.Two.2024.2160p.BluRay.Rus"}, relNames(res.GetReleases()))
	require.Equal(t, "Rus", res.GetReleases()[0].GetAudio()[0].GetMark())
	res = get(&sniffarrv1.TitleReleasesRequest{Id: "m693134", MinSize: 2 << 30, MaxSize: 3 << 30})
	require.Equal(t, []string{"Dune.Part.Two.2024.2160p.BluRay.Rus"}, relNames(res.GetReleases()))
	res = get(&sniffarrv1.TitleReleasesRequest{Id: "m693134", Sort: sniffarrv1.Sort_SORT_SIZE, Limit: 1, Offset: 1})
	require.Equal(t, []string{"Dune.Part.Two.2024.2160p.BluRay.Rus"}, relNames(res.GetReleases()), "the second largest")
	res = get(&sniffarrv1.TitleReleasesRequest{Id: "m693134", Sort: sniffarrv1.Sort_SORT_FIRST_SEEN, Limit: 1})
	require.Equal(t, []string{"Dune.Part.Two.2024.1080p.BluRay.x264"}, relNames(res.GetReleases()), "the newest")

	res = get(&sniffarrv1.TitleReleasesRequest{Id: "tv95396"})
	require.Equal(t, []int32{1, 2}, res.GetSeasons())
	res = get(&sniffarrv1.TitleReleasesRequest{Id: "tv95396", Season: 2, Episode: 1})
	require.Equal(t, []string{"Severance.S02E01.1080p.WEB-DL"}, relNames(res.GetReleases()))
	require.Equal(t, int32(2), res.GetReleases()[0].GetSeason())
	require.Equal(t, int32(1), res.GetReleases()[0].GetEpisode())

	_, err := b.TitleReleases(ctx, connect.NewRequest(&sniffarrv1.TitleReleasesRequest{Id: "m1"}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	for _, req := range []*sniffarrv1.TitleReleasesRequest{
		{Id: "x"},
		{Id: "m693134", Sort: sniffarrv1.Sort_SORT_LAST_SEEN},
		{Id: "m693134", Voice: sniffarrv1.Voice_VOICE_LIKELY},
		{Id: "m693134", Season: -1},
		{Id: "m693134", Limit: -1},
		{Id: "m693134", Quality: 9},
	} {
		_, err := b.TitleReleases(ctx, connect.NewRequest(req))
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "%v", req)
	}
}

func TestBrowserSearchTitles(t *testing.T) {
	ctx := context.Background()
	b := ranked(t)
	f := &fakeTitles{store: b.Store, hits: map[string][]store.Hit{
		"movie dune": {
			{Type: store.TypeMovie, TMDBID: 693134, Lang: "ru", Title: "Дюна: Часть вторая", Popularity: 60},
			{Type: store.TypeMovie, TMDBID: 438631, Lang: "ru", Title: "Дюна", OriginalTitle: "Dune", Year: 2021, Popularity: 80},
			{Type: store.TypeMovie, TMDBID: 841, Lang: "ru", Title: "Дюна", OriginalTitle: "Dune", Year: 1984, Popularity: 20},
		},
		"tv dune": {{Type: store.TypeTV, TMDBID: 90228, Lang: "ru", Title: "Дюна: Пророчество", Popularity: 70}},
	}}
	b.TMDB = f
	ids := func(res *sniffarrv1.SearchTitlesResponse) []string {
		var out []string
		for _, w := range res.GetWorks() {
			out = append(out, w.GetId())
		}
		return out
	}
	res, err := b.SearchTitles(ctx, connect.NewRequest(&sniffarrv1.SearchTitlesRequest{Query: "Dune", Lang: "ru"}))
	require.NoError(t, err)
	require.Equal(t, []string{"m693134", "m438631", "tv90228", "m841"}, ids(res.Msg), "ours by peers, then TMDB's by popularity")
	require.EqualValues(t, 1, res.Msg.GetOurs())
	require.EqualValues(t, 3, res.Msg.GetTmdbOnly())
	ours := res.Msg.GetWorks()[0]
	require.Equal(t, int64(3), ours.GetReleases())
	require.Equal(t, int32(1), ours.GetRank())
	require.Equal(t, "Дюна: Часть вторая", ours.GetTmdb().GetTitle())
	only := res.Msg.GetWorks()[1]
	require.Zero(t, only.GetReleases())
	require.Equal(t, "Дюна", only.GetTmdb().GetTitle())
	require.Equal(t, "Dune", only.GetTmdb().GetOriginalTitle())
	require.Equal(t, int32(2021), only.GetTmdb().GetYear())
	require.ElementsMatch(t, []string{"movie Dune ru", "tv Dune ru"}, f.asked, "both types, in the request's language")

	f.asked = nil
	res, err = b.SearchTitles(ctx, connect.NewRequest(&sniffarrv1.SearchTitlesRequest{Query: "dune", Kind: sniffarrv1.Kind_KIND_SERIES}))
	require.NoError(t, err)
	require.Equal(t, []string{"tv90228"}, ids(res.Msg), "series only")
	require.Equal(t, []string{"tv dune en"}, f.asked)

	res, err = b.SearchTitles(ctx, connect.NewRequest(&sniffarrv1.SearchTitlesRequest{Query: "разделение"}))
	require.NoError(t, err)
	require.Equal(t, []string{"tv95396"}, ids(res.Msg), "by a title in another language")
	res, err = b.SearchTitles(ctx, connect.NewRequest(&sniffarrv1.SearchTitlesRequest{Query: "1080p"}))
	require.NoError(t, err)
	require.Equal(t, []string{"m693134", "m157336", "tv95396"}, ids(res.Msg), "by release names; an unmatched release is no work")

	f.err = errors.New("tmdb down")
	res, err = b.SearchTitles(ctx, connect.NewRequest(&sniffarrv1.SearchTitlesRequest{Query: "interstellar"}))
	require.NoError(t, err, "a failing TMDB leaves the answer to the store")
	require.Equal(t, []string{"m157336"}, ids(res.Msg))
	b.TMDB = nil
	res, err = b.SearchTitles(ctx, connect.NewRequest(&sniffarrv1.SearchTitlesRequest{Query: "  "}))
	require.NoError(t, err)
	require.Empty(t, res.Msg.GetWorks())
	for _, req := range []*sniffarrv1.SearchTitlesRequest{{Query: "x", Kind: sniffarrv1.Kind_KIND_MUSIC}, {Query: "x", Lang: "de"}} {
		_, err := b.SearchTitles(ctx, connect.NewRequest(req))
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "%v", req)
	}
}

func TestBrowserMatchedRows(t *testing.T) {
	ctx := context.Background()
	b := ranked(t)
	res, err := b.Card(ctx, connect.NewRequest(&sniffarrv1.CardRequest{Infohash: infohash(2), Lang: "ru"}))
	require.NoError(t, err)
	require.InDelta(t, 50, res.Msg.GetPeers().GetWeek(), 5)
	w := res.Msg.GetWork()
	require.Equal(t, "m693134", w.GetId())
	require.Equal(t, int64(3), w.GetReleases())
	require.Equal(t, "Дюна: Часть вторая", w.GetTmdb().GetTitle())
	require.Equal(t, sniffarrv1.TMDBLayer_TMDB_LAYER_SEARCH, w.GetTmdb().GetLayer(), "the release's match")
	require.InDelta(t, 0.9, w.GetTmdb().GetConfidence(), 1e-6)
	require.Equal(t, now.Add(-time.Hour), w.GetTmdb().GetCheckedAt().AsTime())
	require.Equal(t, int32(2), res.Msg.GetRankInWork(), "fewer peers than the other Dune")
	require.Equal(t, "m693134", res.Msg.GetRelease().GetWork())

	res, err = b.Card(ctx, connect.NewRequest(&sniffarrv1.CardRequest{Infohash: infohash(7)}))
	require.NoError(t, err)
	require.Nil(t, res.Msg.GetWork())
	require.Nil(t, res.Msg.GetRelease().GetTmdb(), "never looked up")

	rows, err := b.Torrents(ctx, connect.NewRequest(&sniffarrv1.TorrentsRequest{Matched: new(true), Sort: sniffarrv1.Sort_SORT_PEERS}))
	require.NoError(t, err)
	require.Equal(t, int64(6), rows.Msg.GetTotal())
	first := rows.Msg.GetRows()[0]
	require.Equal(t, "Interstellar 2014 1080p BluRay x264", first.GetRelease().GetName())
	require.Equal(t, "m157336", first.GetWork().GetId(), "a row carries its work")
	rows, err = b.Torrents(ctx, connect.NewRequest(&sniffarrv1.TorrentsRequest{Matched: new(false)}))
	require.NoError(t, err)
	require.Equal(t, []string{"Unmatched.2020.1080p.WEB-DL"}, rowNames(rows.Msg.GetRows()))
}
