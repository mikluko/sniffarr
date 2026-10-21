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
// looked up and not found, and Heat never looked up.
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
			Kind: x.kind, KindLayer: store.LayerRules, TitleKey: p.Key, Year: p.Year, Season: p.Season})
		require.NoError(t, err)
	}
	w, _, err := s.RecordMatch(ctx, "y:alien 1979", store.Found{Hit: &store.Hit{Type: store.TypeMovie, TMDBID: 348}, Confidence: 0.8, Layer: store.LayerJev}, now.Add(-time.Hour))
	require.NoError(t, err)
	require.NoError(t, s.RecordDetails(ctx, w, store.Details{Type: "movie", OriginalTitle: "Alien", Year: 1979, PosterPath: "/p.jpg", Rating: 8.2, Votes: 16995,
		Runtime: 117, Countries: []string{"US"}, Language: "en",
		Texts: map[string]store.Text{"en": {Title: "Alien", Overview: "Nostromo.", Genres: []string{"Horror"}, PosterPath: "/p.jpg"}, "ru": {Title: "Чужой", Genres: []string{"ужасы"}, PosterPath: "/r.jpg"}}},
		[]store.ExternalID{{Provider: store.ProviderIMDb, ID: "tt0078748"}}, now))
	_, _, err = s.RecordMatch(ctx, "s:severance", store.Found{}, now.Add(-2*time.Hour))
	require.NoError(t, err)
	return &Browser{Store: s, Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now }}
}

func TestTMDBWork(t *testing.T) {
	ctx := context.Background()
	b := matched(t)
	res, err := b.Card(ctx, connect.NewRequest(&sniffarrv1.CardRequest{Infohash: infohash(1), Lang: "ru"}))
	require.NoError(t, err)
	want := &sniffarrv1.TMDBWork{Id: 348, Type: "movie", ImdbId: "tt0078748", Title: "Чужой", OriginalTitle: "Alien", Year: 1979, PosterPath: "/r.jpg",
		Overview: "Nostromo.", Genres: []string{"ужасы"}, Rating: 8.2, Votes: 16995, Runtime: 117, Countries: []string{"US"}, Language: "en",
		Layer: sniffarrv1.TMDBLayer_TMDB_LAYER_CLASSIFIER, Confidence: 0.8, CheckedAt: res.Msg.GetRelease().GetTmdb().GetCheckedAt()}
	require.Equal(t, want.String(), res.Msg.GetRelease().GetTmdb().String(), "matched: Russian texts and poster, the overview from English where Russian has none")
	require.Equal(t, now.Add(-time.Hour), res.Msg.GetRelease().GetTmdb().GetCheckedAt().AsTime())
	require.Equal(t, "m348", res.Msg.GetRelease().GetWork())

	res, err = b.Card(ctx, connect.NewRequest(&sniffarrv1.CardRequest{Infohash: infohash(1)}))
	require.NoError(t, err)
	require.Equal(t, "Alien", res.Msg.GetRelease().GetTmdb().GetTitle(), "English by default")
	require.Equal(t, "/p.jpg", res.Msg.GetRelease().GetTmdb().GetPosterPath(), "the English poster")

	res, err = b.Card(ctx, connect.NewRequest(&sniffarrv1.CardRequest{Infohash: infohash(2)}))
	require.NoError(t, err)
	unmatched := res.Msg.GetRelease().GetTmdb()
	require.NotNil(t, unmatched, "looked up")
	require.Zero(t, unmatched.GetId(), "nothing matched")
	require.Equal(t, now.Add(-2*time.Hour), unmatched.GetCheckedAt().AsTime())
	require.Empty(t, res.Msg.GetRelease().GetWork())

	res, err = b.Card(ctx, connect.NewRequest(&sniffarrv1.CardRequest{Infohash: infohash(3)}))
	require.NoError(t, err)
	require.Nil(t, res.Msg.GetRelease().GetTmdb(), "never looked up")

	_, err = b.Card(ctx, connect.NewRequest(&sniffarrv1.CardRequest{Infohash: infohash(1), Lang: "de"}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	svc := &Service{Store: b.Store, Log: b.Log}
	got, err := svc.Get(ctx, connect.NewRequest(&sniffarrv1.GetRequest{Infohash: infohash(1), Lang: "ru"}))
	require.NoError(t, err)
	require.Equal(t, "Чужой", got.Msg.GetRelease().GetTmdb().GetTitle())
	require.Equal(t, []*sniffarrv1.ExternalId{{Provider: "imdb", Id: "tt0078748"}, {Provider: "tmdb", Id: "348"}}, got.Msg.GetRelease().GetIds())
	found, err := svc.Search(ctx, connect.NewRequest(&sniffarrv1.SearchRequest{Query: "alien"}))
	require.NoError(t, err)
	require.Equal(t, "Alien", found.Msg.GetReleases()[0].GetTmdb().GetTitle())
	require.Equal(t, "m348", found.Msg.GetReleases()[0].GetWork())
	_, err = svc.Search(ctx, connect.NewRequest(&sniffarrv1.SearchRequest{Lang: "xx"}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestWorkID(t *testing.T) {
	require.Equal(t, "m348", WorkID(store.Work{TMDBID: 348, Details: store.Details{Type: store.TypeMovie}}))
	require.Equal(t, "tv1399", WorkID(store.Work{TMDBID: 1399, Details: store.Details{Type: store.TypeTV}}))
	for id, want := range map[string]struct {
		typ string
		n   int
		ok  bool
	}{
		"m348": {store.TypeMovie, 348, true}, "tv1399": {store.TypeTV, 1399, true},
		"348": {}, "m": {}, "tv": {}, "m0": {}, "m-1": {}, "m+1": {}, "m01": {}, "x348": {},
	} {
		typ, n, ok := ParseWorkID(id)
		require.Equal(t, want, struct {
			typ string
			n   int
			ok  bool
		}{typ, n, ok}, id)
	}
}
