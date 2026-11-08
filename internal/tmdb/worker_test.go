package tmdb

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"sarak.sh/mikluko/sniffarr/internal/store"
)

func TestWorker(t *testing.T) {
	ctx := context.Background()
	f := newTMDB(t)
	f.results["/3/search/movie|Alien|en-US|1979"] = []result{{ID: 348, Title: "Alien", OriginalTitle: "Alien", ReleaseDate: "1979-05-25"}}
	f.alien(t)
	f.fixture(t, "/3/tv/106379", "en-US", "tv_en.json")
	f.fixture(t, "/3/tv/106379", "ru-RU", "tv_ru.json")
	s := setup(t,
		store.Torrent{Name: "Alien.1979.1080p.BluRay", Kind: "movie", TitleKey: "y:alien 1979"},
		store.Torrent{Name: "Severance.S02E01", Kind: "series", TitleKey: "s:severance"},
		store.Torrent{Name: "Moon.Safari.1998.FLAC", Kind: "music", TitleKey: "y:moon safari 1998"},
		store.Torrent{Name: "Fallout.S01.2160p", Kind: "series", TitleKey: "s:fallout"},
		store.Torrent{Name: "Ronin.1998.1080p", Kind: "movie", TitleKey: "y:ronin 1998"},
	)
	_, _, err := s.RecordMatch(ctx, "s:fallout", store.Found{Hit: &store.Hit{Type: store.TypeTV, TMDBID: 106379}, Confidence: 0.9, Layer: store.LayerTMDB}, now.Add(-time.Hour))
	require.NoError(t, err)
	_, _, err = s.RecordMatch(ctx, "y:ronin 1998", store.Found{Hit: &store.Hit{Type: store.TypeMovie, TMDBID: 8195}, Confidence: 1, Layer: store.LayerTMDB}, now.Add(-2*time.Hour))
	require.NoError(t, err)
	m := matcher(f, s)
	w := &Worker{Matcher: m, Concurrency: 2, Poll: 10 * time.Millisecond, Log: slog.New(slog.DiscardHandler)}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan error)
	go func() { done <- w.Run(ctx) }()
	require.Eventually(t, func() bool {
		keys, err := m.Unmatched(ctx, nil, 10)
		if err != nil || len(keys) > 0 {
			return false
		}
		due, err := s.WithoutDetails(ctx, 10)
		return err == nil && len(due) == 0
	}, 5*time.Second, 10*time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.Equal(t, *alienDetails, outcomeOf(t, s, 1).Details, "matched with its details")
	require.Equal(t, store.Match{MatchedAt: now}, match(t, s, 2))
	fallout := match(t, s, 4)
	require.Equal(t, "Фоллаут", fallout.Work.Text("ru").Title, "a match from before got its details")
	require.Equal(t, []store.ExternalID{{Provider: "imdb", ID: "tt12637874"}, {Provider: "tmdb", ID: "106379"}, {Provider: "tvdb", ID: "416744"}, {Provider: "wikidata", ID: "Q113127312"}},
		fallout.Work.ExternalIDs(), "and its IDs")
	require.False(t, match(t, s, 5).Work.DetailsAt.IsZero(), "a work TMDB refuses to tell of left the backfill")
}
