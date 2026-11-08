package tmdb

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"sarak.sh/mikluko/sniffarr/internal/store"
)

func TestSearchWorks(t *testing.T) {
	ctx := context.Background()
	f := newTMDB(t)
	f.results["/3/search/movie|alien|ru-RU|"] = []result{
		{ID: 348, Title: "Чужой", OriginalTitle: "Alien", ReleaseDate: "1979-05-25", Popularity: 40},
		{ID: 679, Title: "Чужие", OriginalTitle: "Aliens", ReleaseDate: "1986-07-18", Popularity: 30},
	}
	f.results["/3/search/tv|alien|ru-RU|"] = []result{{ID: 1, Name: "Чужой: Земля", OriginalName: "Alien: Earth", FirstAirDate: "2025-08-12", Popularity: 90}}
	s := setup(t)
	m := matcher(f, s)

	ids, err := m.SearchWorks(ctx, store.TypeMovie, "  Alien ", "ru")
	require.NoError(t, err)
	require.Len(t, ids, 2)
	ws, err := s.Works(ctx, ids)
	require.NoError(t, err)
	alien := ws[ids[0]]
	require.Equal(t, 348, alien.TMDBID, "in TMDB's order")
	require.Equal(t, "Чужой", alien.Texts["ru"].Title, "the texts of the language asked in")
	require.Equal(t, "Alien", alien.OriginalTitle)
	require.Equal(t, 1979, alien.Year)
	require.Equal(t, 40.0, alien.Popularity)
	require.True(t, alien.DetailsAt.IsZero(), "the details are left to the backfill")
	due, err := s.WithoutDetails(ctx, 10)
	require.NoError(t, err)
	require.Len(t, due, 2)

	again, err := m.SearchWorks(ctx, store.TypeMovie, "alien", "en")
	require.NoError(t, err)
	require.Equal(t, ids, again)
	require.Equal(t, []string{"/3/search/movie|alien|ru-RU|"}, f.searches, "a second search within the TTL asks nothing")
	tv, err := m.SearchWorks(ctx, store.TypeTV, "alien", "ru")
	require.NoError(t, err)
	require.Len(t, tv, 1)
	require.Len(t, f.searches, 2, "each type is a search of its own")

	m.Now = func() time.Time { return now.Add(store.SearchTTL + time.Second) }
	_, err = m.SearchWorks(ctx, store.TypeMovie, "alien", "ru")
	require.NoError(t, err)
	require.Len(t, f.searches, 3, "past the TTL TMDB is asked again")

	none, err := m.SearchWorks(ctx, store.TypeMovie, " ", "ru")
	require.NoError(t, err)
	require.Empty(t, none)
	require.Len(t, f.searches, 3, "an empty query asks nothing")
	f.Close()
	_, err = m.SearchWorks(ctx, store.TypeMovie, "heat", "ru")
	require.Error(t, err)
}
