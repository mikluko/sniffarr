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
	titles "github.com/mikluko/sniffarr/internal/names"
	"github.com/mikluko/sniffarr/internal/store"
)

// matched holds Alien matched with details in both languages, Severance
// looked up and not found, and Heat never looked up, with the tops computed.
func matched(t *testing.T) *Browser {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	s, err := store.New(ctx, db, store.Options{TMDB: true})
	require.NoError(t, err)
	for b, x := range map[byte]struct{ name, kind string }{1: {"Alien.1979.1080p.BluRay", "movie"}, 2: {"Severance.S02E01.1080p", "series"}, 3: {"Heat.1995.1080p", "movie"}} {
		p := titles.Parse(x.name)
		_, _, err := s.Insert(ctx, store.Torrent{Hash: hash(b), Name: x.name, Size: int64(b) << 30, Discovery: "sample", SeenAt: now.Add(-time.Duration(b) * time.Hour),
			Kind: x.kind, KindLayer: store.LayerRules, TitleKey: p.Key, WorkKey: p.Work, Year: p.Year, Season: p.Season})
		require.NoError(t, err)
	}
	require.NoError(t, s.RecordTitle(ctx, "y:alien 1979", store.Title{Year: 1979, Confidence: 0.8, Layer: store.LayerJev,
		IDs: []store.ExternalID{{Provider: store.ProviderTMDB, ID: "348"}, {Provider: store.ProviderIMDb, ID: "tt0078748"}},
		Details: &store.Details{Type: "movie", OriginalTitle: "Alien", PosterPath: "/p.jpg", Rating: 8.2, Votes: 16995, Runtime: 117, Countries: []string{"US"}, Language: "en",
			Texts: map[string]store.Text{"en": {Title: "Alien", Overview: "Nostromo.", Genres: []string{"Horror"}}, "ru": {Title: "Чужой", Genres: []string{"ужасы"}}}}}, now.Add(-time.Hour)))
	require.NoError(t, s.RecordTitle(ctx, "s:severance", store.Title{}, now.Add(-2*time.Hour)))
	k, err := s.HashKey(ctx)
	require.NoError(t, err)
	_, _, err = s.MergePeers(ctx, []store.PeerSketch{{Hash: hash(1), Hour: now, Sketch: peers(k, 0, 10)}, {Hash: hash(2), Hour: now, Sketch: peers(k, 0, 5)}})
	require.NoError(t, err)
	require.NoError(t, s.ComputeTop(ctx, now))
	return &Browser{Store: s, Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now }}
}

func TestTMDBWork(t *testing.T) {
	ctx := context.Background()
	b := matched(t)
	res, err := b.Card(ctx, connect.NewRequest(&sniffarrv1.CardRequest{Infohash: infohash(1), Lang: "ru"}))
	require.NoError(t, err)
	want := &sniffarrv1.TMDBWork{Id: 348, Type: "movie", ImdbId: "tt0078748", Title: "Чужой", OriginalTitle: "Alien", Year: 1979, PosterPath: "/p.jpg",
		Overview: "Nostromo.", Genres: []string{"ужасы"}, Rating: 8.2, Votes: 16995, Runtime: 117, Countries: []string{"US"}, Language: "en",
		Layer: sniffarrv1.TMDBLayer_TMDB_LAYER_CLASSIFIER, Confidence: 0.8, CheckedAt: res.Msg.GetRelease().GetTmdb().GetCheckedAt()}
	require.Equal(t, want.String(), res.Msg.GetRelease().GetTmdb().String(), "matched: Russian texts, the overview from English where Russian has none")
	require.Equal(t, now.Add(-time.Hour), res.Msg.GetRelease().GetTmdb().GetCheckedAt().AsTime())

	res, err = b.Card(ctx, connect.NewRequest(&sniffarrv1.CardRequest{Infohash: infohash(1)}))
	require.NoError(t, err)
	require.Equal(t, "Alien", res.Msg.GetRelease().GetTmdb().GetTitle(), "English by default")
	require.Equal(t, []string{"Horror"}, res.Msg.GetRelease().GetTmdb().GetGenres())

	res, err = b.Card(ctx, connect.NewRequest(&sniffarrv1.CardRequest{Infohash: infohash(2)}))
	require.NoError(t, err)
	unmatched := res.Msg.GetRelease().GetTmdb()
	require.NotNil(t, unmatched, "looked up")
	require.Zero(t, unmatched.GetId(), "nothing matched")
	require.Equal(t, now.Add(-2*time.Hour), unmatched.GetCheckedAt().AsTime())
	require.Empty(t, unmatched.GetTitle())

	res, err = b.Card(ctx, connect.NewRequest(&sniffarrv1.CardRequest{Infohash: infohash(3)}))
	require.NoError(t, err)
	require.Nil(t, res.Msg.GetRelease().GetTmdb(), "never looked up")

	_, err = b.Card(ctx, connect.NewRequest(&sniffarrv1.CardRequest{Infohash: infohash(1), Lang: "de"}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	top, err := b.Top(ctx, connect.NewRequest(&sniffarrv1.TopRequest{Kind: sniffarrv1.Kind_KIND_MOVIE, Lang: "ru"}))
	require.NoError(t, err)
	require.Len(t, top.Msg.GetEntries(), 1)
	require.Equal(t, "Чужой", top.Msg.GetEntries()[0].GetWork().GetTmdb().GetTitle(), "a top entry carries its work's match")
	top, err = b.Top(ctx, connect.NewRequest(&sniffarrv1.TopRequest{Kind: sniffarrv1.Kind_KIND_SERIES}))
	require.NoError(t, err)
	require.Len(t, top.Msg.GetEntries(), 1)
	require.Zero(t, top.Msg.GetEntries()[0].GetWork().GetTmdb().GetId())
	require.NotNil(t, top.Msg.GetEntries()[0].GetWork().GetTmdb())

	wk, err := b.Work(ctx, connect.NewRequest(&sniffarrv1.WorkRequest{Key: "y:alien 1979", Lang: "ru"}))
	require.NoError(t, err)
	require.Equal(t, "Чужой", wk.Msg.GetWork().GetTmdb().GetTitle())
	require.Equal(t, "Чужой", wk.Msg.GetReleases()[0].GetRelease().GetTmdb().GetTitle(), "and so do its releases")
	wk, err = b.Work(ctx, connect.NewRequest(&sniffarrv1.WorkRequest{Key: "y:heat 1995"}))
	require.NoError(t, err)
	require.Nil(t, wk.Msg.GetWork().GetTmdb())

	page, err := b.Page(ctx, connect.NewRequest(&sniffarrv1.PageRequest{Query: "1080p", Lang: "ru"}))
	require.NoError(t, err)
	byKey := map[string]*sniffarrv1.TMDBWork{}
	for _, w := range page.Msg.GetWorks() {
		byKey[w.GetWork().GetKey()] = w.GetTmdb()
	}
	require.Equal(t, "Чужой", byKey["y:alien 1979"].GetTitle(), "the works of a page carry their matches")
	require.Zero(t, byKey["s:severance/2"].GetId())
	require.Contains(t, byKey, "y:heat 1995")
	require.Nil(t, byKey["y:heat 1995"])
	for _, r := range page.Msg.GetReleases() {
		if r.GetName() == "Alien.1979.1080p.BluRay" {
			require.Equal(t, "Чужой", r.GetTmdb().GetTitle(), "a page's releases carry theirs")
		}
	}
	for _, req := range []any{&sniffarrv1.PageRequest{Lang: "xx"}, &sniffarrv1.TopRequest{Kind: sniffarrv1.Kind_KIND_MOVIE, Lang: "xx"}, &sniffarrv1.WorkRequest{Key: "y:alien 1979", Lang: "xx"}} {
		var err error
		switch m := req.(type) {
		case *sniffarrv1.PageRequest:
			_, err = b.Page(ctx, connect.NewRequest(m))
		case *sniffarrv1.TopRequest:
			_, err = b.Top(ctx, connect.NewRequest(m))
		case *sniffarrv1.WorkRequest:
			_, err = b.Work(ctx, connect.NewRequest(m))
		}
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "%v", req)
	}

	svc := &Service{Store: b.Store, Log: b.Log}
	got, err := svc.Get(ctx, connect.NewRequest(&sniffarrv1.GetRequest{Infohash: infohash(1), Lang: "ru"}))
	require.NoError(t, err)
	require.Equal(t, "Чужой", got.Msg.GetRelease().GetTmdb().GetTitle())
	found, err := svc.Search(ctx, connect.NewRequest(&sniffarrv1.SearchRequest{Query: "alien"}))
	require.NoError(t, err)
	require.Equal(t, "Alien", found.Msg.GetReleases()[0].GetTmdb().GetTitle())
	_, err = svc.Search(ctx, connect.NewRequest(&sniffarrv1.SearchRequest{Lang: "xx"}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	require.Equal(t, "tv", tmdbWork(store.Title{Layer: store.LayerTMDB, MatchedAt: now, IDs: []store.ExternalID{{Provider: "tmdb", ID: "7"}}}, "series", "en").GetType(),
		"a match without details is typed by its kind")
}
