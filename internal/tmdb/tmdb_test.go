package tmdb

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"golang.org/x/time/rate"

	"github.com/mikluko/sniffarr/internal/classify"
	"github.com/mikluko/sniffarr/internal/database"
	"github.com/mikluko/sniffarr/internal/store"
)

type result struct {
	ID            int    `json:"id"`
	Title         string `json:"title,omitempty"`
	OriginalTitle string `json:"original_title,omitempty"`
	ReleaseDate   string `json:"release_date,omitempty"`
	Name          string `json:"name,omitempty"`
	OriginalName  string `json:"original_name,omitempty"`
	FirstAirDate  string `json:"first_air_date,omitempty"`
	Overview      string `json:"overview"`
}

// fakeTMDB answers searches from results, keyed by path, query and
// language, and details from details, keyed by path and language: recorded
// bytes, a value to encode, or an int status to fail with.
type fakeTMDB struct {
	*httptest.Server
	mu       sync.Mutex
	results  map[string][]result
	details  map[string]any
	searches []string
	// throttle is how many 429 answers come first.
	throttle atomic.Int64
	requests atomic.Int64
}

func newTMDB(t *testing.T) *fakeTMDB {
	f := &fakeTMDB{results: map[string][]result{}, details: map[string]any{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if r.URL.Query().Get("api_key") != "secret" {
			http.Error(w, "{}", http.StatusUnauthorized)
			return
		}
		if f.throttle.Add(-1) >= 0 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "{}", http.StatusTooManyRequests)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		q := r.URL.Query()
		if strings.HasPrefix(r.URL.Path, "/3/search/") {
			k := r.URL.Path + "|" + q.Get("query") + "|" + q.Get("language") + "|" + q.Get("year")
			f.searches = append(f.searches, k)
			_ = json.NewEncoder(w).Encode(map[string]any{"results": f.results[k]})
			return
		}
		if q.Get("append_to_response") != "external_ids" {
			http.Error(w, "{}", http.StatusBadRequest)
			return
		}
		d, ok := f.details[r.URL.Path+"|"+q.Get("language")]
		if !ok {
			http.Error(w, "{}", http.StatusNotFound)
			return
		}
		switch d := d.(type) {
		case int:
			http.Error(w, "{}", d)
		case []byte:
			_, _ = w.Write(d)
		default:
			_ = json.NewEncoder(w).Encode(d)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// fixture serves the recorded answer in testdata for the path in the language.
func (f *fakeTMDB) fixture(t *testing.T, path, language, name string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	f.details[path+"|"+language] = b
}

// alien serves the recorded details of Alien.
func (f *fakeTMDB) alien(t *testing.T) {
	f.fixture(t, "/3/movie/348", "en-US", "movie_en.json")
	f.fixture(t, "/3/movie/348", "ru-RU", "movie_ru.json")
}

// alienDetails are the details the recorded answers hold.
var alienDetails = &store.Details{
	Type: "movie", OriginalTitle: "Alien", PosterPath: "/vfrQk5IPloGg1v9Rzbh2Eg3VGyM.jpg", Rating: 8.176, Votes: 16995, Runtime: 117,
	Countries: []string{"US"}, Language: "en",
	Texts: map[string]store.Text{
		"en": {Title: "Alien", Overview: "During its return to the earth, commercial spaceship Nostromo intercepts a distress signal from a distant planet. When a three-member team of the crew discovers a chamber containing thousands of eggs on the planet, a creature inside one of the eggs attacks an explorer. The entire crew is unaware of the impending nightmare set to descend upon them when the alien parasite planted inside its unfortunate host is birthed.", Genres: []string{"Horror", "Science Fiction"}},
		"ru": {Title: "Чужой", Overview: "В далеком будущем возвращающийся на Землю грузовой космический корабль перехватывает исходящий с неизвестной планеты сигнал. Экипаж, в соответствии с основными инструкциями, обязан найти и исследовать источник сигнала. Оказавшись на планете, астронавты повсюду обнаруживают предметы, по виду напоминающие гигантские коконы.", Genres: []string{"ужасы", "фантастика"}},
	},
}

func (f *fakeTMDB) client() *Client {
	c := NewClient("secret", 1000)
	c.URL, c.Backoff = f.URL, time.Millisecond
	c.Limiter = rate.NewLimiter(rate.Inf, 1)
	return c
}

// fakeChooser picks choice and remembers the options it was shown.
type fakeChooser struct {
	choice  string
	options map[string]string
}

func (c *fakeChooser) Choose(_ context.Context, in classify.ChoiceInput) (classify.ChoiceResult, error) {
	c.options = in.Options
	return classify.ChoiceResult{Choice: c.choice, Confidence: 0.7}, nil
}

var now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func setup(t *testing.T, torrents ...store.Torrent) *store.Store {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	s, err := store.New(ctx, db, store.Options{TMDB: true})
	require.NoError(t, err)
	for i, tr := range torrents {
		tr.Hash = store.Hash{byte(i + 1)}
		tr.Size, tr.Discovery, tr.SeenAt, tr.KindLayer = 1<<30, "sample", time.Now(), store.LayerRules
		_, _, err := s.Insert(ctx, tr)
		require.NoError(t, err)
	}
	return s
}

func matcher(f *fakeTMDB, s *store.Store) *Matcher {
	return &Matcher{Client: f.client(), Store: s, Now: func() time.Time { return now }, Log: slog.New(slog.DiscardHandler)}
}

func title(t *testing.T, s *store.Store, b byte) store.Title {
	t.Helper()
	r, err := s.Get(context.Background(), store.Hash{b})
	require.NoError(t, err)
	return r.Title
}

func TestDetails(t *testing.T) {
	ctx := context.Background()
	f := newTMDB(t)
	f.alien(t)
	f.fixture(t, "/3/tv/106379", "en-US", "tv_en.json")
	f.fixture(t, "/3/tv/106379", "ru-RU", "tv_ru.json")
	c := f.client()

	d, err := c.Details(ctx, false, 348, "en-US")
	require.NoError(t, err)
	require.Equal(t, Details{ID: 348, Title: "Alien", OriginalTitle: "Alien", Year: 1979, PosterPath: "/vfrQk5IPloGg1v9Rzbh2Eg3VGyM.jpg",
		Overview: alienDetails.Texts["en"].Overview, Genres: []string{"Horror", "Science Fiction"}, Rating: 8.176, Votes: 16995, Runtime: 117,
		Countries: []string{"US"}, Language: "en", IDs: map[string]string{"imdb": "tt0078748", "wikidata": "Q103569"}}, d,
		"a movie: its runtime, no status or seasons, the origin country over the production countries")
	d, err = c.Details(ctx, false, 348, "ru-RU")
	require.NoError(t, err)
	require.Equal(t, "Чужой", d.Title)
	require.Equal(t, "Alien", d.OriginalTitle)
	require.Equal(t, []string{"ужасы", "фантастика"}, d.Genres)
	require.Equal(t, "/zg4fcO8IuYRllK9wWVNqNlYpueL.jpg", d.PosterPath, "the poster of the language")

	d, err = c.Details(ctx, true, 106379, "en-US")
	require.NoError(t, err)
	require.Equal(t, Details{ID: 106379, Title: "Fallout", OriginalTitle: "Fallout", Year: 2024, PosterPath: "/c15BtJxCXMrISLVmysdsnZUPQft.jpg",
		Overview: d.Overview, Genres: []string{"Action & Adventure", "Sci-Fi & Fantasy", "Drama"}, Rating: 8.104, Votes: 3190,
		Status: "Returning Series", Seasons: 2, Countries: []string{"US"}, Language: "en",
		IDs: map[string]string{"imdb": "tt12637874", "tvdb": "416744", "wikidata": "Q113127312"}}, d,
		"a series: its status, seasons and TVDB ID; no runtime when TMDB lists no episode runtime")
	require.Contains(t, d.Overview, "haves and have-nots")
	d, err = c.Details(ctx, true, 106379, "ru-RU")
	require.NoError(t, err)
	require.Equal(t, "Фоллаут", d.Title)

	f.details["/3/tv/7|en-US"] = map[string]any{"id": 7, "name": "Short", "episode_run_time": []int{45, 60}, "poster_path": nil,
		"production_countries": []map[string]string{{"iso_3166_1": "GB"}}, "external_ids": map[string]any{"imdb_id": nil, "tvdb_id": nil}}
	d, err = c.Details(ctx, true, 7, "en-US")
	require.NoError(t, err)
	require.Equal(t, Details{ID: 7, Title: "Short", Runtime: 45, Countries: []string{"GB"}, IDs: map[string]string{}}, d,
		"the first episode runtime, the production countries without an origin, nulls as absent")

	_, err = c.Details(ctx, false, 9, "en-US")
	var tErr *Error
	require.ErrorAs(t, err, &tErr)
	require.True(t, tErr.Permanent())
	require.Equal(t, "tmdb /3/movie/9: 404 Not Found", err.Error())
}

func TestMatchOneCandidate(t *testing.T) {
	ctx := context.Background()
	f := newTMDB(t)
	f.results["/3/search/movie|Alien|en-US|1979"] = []result{
		{ID: 348, Title: "Alien", OriginalTitle: "Alien", ReleaseDate: "1979-05-25"},
		{ID: 999, Title: "Alien Sex Fiend", OriginalTitle: "Alien Sex Fiend", ReleaseDate: "1979-01-01"},
	}
	f.alien(t)
	f.throttle.Store(2)
	s := setup(t, store.Torrent{Name: "Alien.1979.1080p.BluRay", Kind: "movie", TitleKey: "y:alien 1979"})
	m := matcher(f, s)

	keys, err := m.Unmatched(ctx, nil, 10)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.NoError(t, m.Match(ctx, keys[0]))
	require.Equal(t, store.Title{Year: 1979, Confidence: 1, Layer: store.LayerTMDB, IDs: []store.ExternalID{
		{Provider: "imdb", ID: "tt0078748"}, {Provider: "tmdb", ID: "348"}, {Provider: "wikidata", ID: "Q103569"},
	}, MatchedAt: now, Details: alienDetails}, title(t, s, 1), "the match, its IDs from the details, and the details in both languages")
	require.EqualValues(t, 5, f.requests.Load(), "two 429 answers are retried; one search, two details")
	keys, err = m.Unmatched(ctx, nil, 10)
	require.NoError(t, err)
	require.Empty(t, keys)
	due, err := s.WithoutDetails(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, due)
}

func TestMatchBothTitles(t *testing.T) {
	ctx := context.Background()
	f := newTMDB(t)
	f.results["/3/search/tv|Фоллаут|ru-RU|"] = []result{{ID: 106379, Name: "Фоллаут", OriginalName: "Fallout", FirstAirDate: "2024-04-10"}}
	f.results["/3/search/tv|Fallout|en-US|"] = []result{{ID: 106379, Name: "Fallout", OriginalName: "Fallout", FirstAirDate: "2024-04-10"}}
	f.fixture(t, "/3/tv/106379", "en-US", "tv_en.json")
	f.fixture(t, "/3/tv/106379", "ru-RU", "tv_ru.json")
	s := setup(t, store.Torrent{Name: "Фоллаут / Fallout / Сезон: 1 / Серии: 1-8 из 8 [2024, WEB-DL 1080p] MVO", Kind: "series", TitleKey: "s:фоллаут"})
	m := matcher(f, s)

	keys, err := m.Unmatched(ctx, nil, 10)
	require.NoError(t, err)
	require.NoError(t, m.Match(ctx, keys[0]))
	got := title(t, s, 1)
	require.Equal(t, []store.ExternalID{
		{Provider: "imdb", ID: "tt12637874"}, {Provider: "tmdb", ID: "106379"}, {Provider: "tvdb", ID: "416744"}, {Provider: "wikidata", ID: "Q113127312"},
	}, got.IDs, "one work under both titles")
	require.Equal(t, 2024, got.Year)
	require.Equal(t, 0.9, got.Confidence, "a series key has no year")
	require.Equal(t, "tv", got.Details.Type)
	require.Equal(t, "Returning Series", got.Details.Status)
	require.Equal(t, 2, got.Details.Seasons)
	require.Equal(t, "Фоллаут", got.Details.Text("ru").Title)
	require.Equal(t, []string{"/3/search/tv|Фоллаут|en-US|", "/3/search/tv|Фоллаут|ru-RU|", "/3/search/tv|Fallout|en-US|"}, f.searches,
		"each title, in Russian too when Cyrillic")
}

func TestMatchSeveral(t *testing.T) {
	ctx := context.Background()
	f := newTMDB(t)
	f.results["/3/search/movie|Dune|en-US|2021"] = []result{
		{ID: 438631, Title: "Dune", OriginalTitle: "Dune", ReleaseDate: "2021-09-15", Overview: "Paul Atreides travels. He meets worms."},
		{ID: 7, Title: "Dune", OriginalTitle: "Dune", ReleaseDate: "2021-01-01", Overview: "A documentary."},
	}
	for _, lang := range []string{"en-US", "ru-RU"} {
		f.details["/3/movie/438631|"+lang] = map[string]any{"id": 438631, "title": "Dune", "original_title": "Dune", "external_ids": map[string]any{"imdb_id": "tt1160419"}}
	}
	for _, tt := range []struct {
		name    string
		chooser *fakeChooser
		want    store.Title
	}{
		{"picked", &fakeChooser{choice: "1"}, store.Title{Year: 2021, Confidence: 0.7, Layer: store.LayerJev, MatchedAt: now,
			IDs:     []store.ExternalID{{Provider: "imdb", ID: "tt1160419"}, {Provider: "tmdb", ID: "438631"}},
			Details: &store.Details{Type: "movie", OriginalTitle: "Dune", Texts: map[string]store.Text{"en": {Title: "Dune"}, "ru": {Title: "Dune"}}}}},
		{"none of these", &fakeChooser{choice: "none"}, store.Title{MatchedAt: now}},
		{"no chooser", nil, store.Title{MatchedAt: now}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := setup(t, store.Torrent{Name: "Dune.2021.2160p.WEB-DL", Kind: "movie", TitleKey: "y:dune 2021"})
			m := matcher(f, s)
			if tt.chooser != nil {
				m.Chooser = tt.chooser
			}
			keys, err := m.Unmatched(ctx, nil, 10)
			require.NoError(t, err)
			require.NoError(t, m.Match(ctx, keys[0]))
			require.Equal(t, tt.want, title(t, s, 1))
			keys, err = m.Unmatched(ctx, nil, 10)
			require.NoError(t, err)
			require.Empty(t, keys, "recorded either way")
			if tt.chooser != nil {
				require.Equal(t, map[string]string{
					"1":    "Dune, 2021: Paul Atreides travels",
					"2":    "Dune, 2021: A documentary.",
					"none": "None of these works, or not enough to tell",
				}, tt.chooser.options)
			}
		})
	}
}

func TestMatchNone(t *testing.T) {
	ctx := context.Background()
	f := newTMDB(t)
	f.results["/3/search/movie|Heat|en-US|1995"] = []result{{ID: 1, Title: "Heatwave", OriginalTitle: "Heatwave", ReleaseDate: "1995-01-01"}}
	f.results["/3/search/movie|Ronin|en-US|1998"] = []result{{ID: 8195, Title: "Ronin", OriginalTitle: "Ronin", ReleaseDate: "1998-09-12"}}
	s := setup(t,
		store.Torrent{Name: "Heat.1995.1080p.BluRay", Kind: "movie", TitleKey: "y:heat 1995"},
		store.Torrent{Name: "Ronin.1998.1080p.BluRay", Kind: "movie", TitleKey: "y:ronin 1998"},
	)
	at := now
	m := matcher(f, s)
	m.Retry, m.Now = time.Hour, func() time.Time { return at }
	keys, err := m.Unmatched(ctx, nil, 10)
	require.NoError(t, err)
	require.Len(t, keys, 2)
	for _, k := range keys {
		require.NoError(t, m.Match(ctx, k))
	}
	require.Equal(t, store.Title{MatchedAt: now}, title(t, s, 1), "no title fits")
	require.Equal(t, store.Title{Year: 1998, Confidence: 1, Layer: store.LayerTMDB, IDs: []store.ExternalID{{Provider: "tmdb", ID: "8195"}}, MatchedAt: now, Details: &store.Details{}},
		title(t, s, 2), "a work TMDB refuses to tell of is matched with empty details")
	due, err := s.WithoutDetails(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, due, "and is not asked about again")

	keys, err = m.Unmatched(ctx, nil, 10)
	require.NoError(t, err)
	require.Empty(t, keys)
	at = at.Add(time.Hour + time.Second)
	keys, err = m.Unmatched(ctx, nil, 10)
	require.NoError(t, err)
	require.Equal(t, []store.TitleKey{{Key: "y:heat 1995", Kind: "movie", Name: "Heat.1995.1080p.BluRay"}}, keys, "the failed match is looked up again after Retry")
}

func TestMatchDetailsLater(t *testing.T) {
	ctx := context.Background()
	f := newTMDB(t)
	f.results["/3/search/movie|Alien|en-US|1979"] = []result{{ID: 348, Title: "Alien", OriginalTitle: "Alien", ReleaseDate: "1979-05-25"}}
	s := setup(t, store.Torrent{Name: "Alien.1979.1080p.BluRay", Kind: "movie", TitleKey: "y:alien 1979"})
	m := matcher(f, s)
	m.Client.Retries = 0
	keys, err := m.Unmatched(ctx, nil, 10)
	require.NoError(t, err)
	f.fixture(t, "/3/movie/348", "en-US", "movie_en.json")
	f.details["/3/movie/348|ru-RU"] = http.StatusServiceUnavailable
	require.NoError(t, m.Match(ctx, keys[0]), "the match is recorded though its details failed")
	require.Equal(t, store.Title{Year: 1979, Confidence: 1, Layer: store.LayerTMDB, IDs: []store.ExternalID{{Provider: "tmdb", ID: "348"}}, MatchedAt: now}, title(t, s, 1))
	due, err := s.WithoutDetails(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, []store.Matched{{Key: "y:alien 1979", Kind: "movie", ID: 348}}, due, "the details are due")

	require.Error(t, m.Details(ctx, due[0]), "the Russian answer is missing")
	require.Nil(t, title(t, s, 1).Details)
	f.fixture(t, "/3/movie/348", "ru-RU", "movie_ru.json")
	require.NoError(t, m.Details(ctx, due[0]))
	got := title(t, s, 1)
	require.Equal(t, alienDetails, got.Details)
	require.Equal(t, []store.ExternalID{{Provider: "imdb", ID: "tt0078748"}, {Provider: "tmdb", ID: "348"}, {Provider: "wikidata", ID: "Q103569"}}, got.IDs,
		"the IDs come with the details")
}

func TestTMDBErrorsHideKey(t *testing.T) {
	f := newTMDB(t)
	c := f.client()
	f.Close()
	_, err := c.Search(context.Background(), false, "Alien", 1979, "en-US")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret")
}

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
	tmdbID := func(id string) []store.ExternalID { return []store.ExternalID{{Provider: store.ProviderTMDB, ID: id}} }
	require.NoError(t, s.RecordTitle(ctx, "s:fallout", store.Title{Year: 2024, Confidence: 0.9, Layer: store.LayerTMDB, IDs: tmdbID("106379")}, now.Add(-time.Hour)))
	require.NoError(t, s.RecordTitle(ctx, "y:ronin 1998", store.Title{Year: 1998, Confidence: 1, Layer: store.LayerTMDB, IDs: tmdbID("8195")}, now.Add(-2*time.Hour)))
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
	require.Equal(t, store.LayerTMDB, title(t, s, 1).Layer)
	require.Equal(t, alienDetails, title(t, s, 1).Details, "matched with its details")
	require.Equal(t, store.Title{MatchedAt: now}, title(t, s, 2))
	fallout := title(t, s, 4)
	require.Equal(t, "Фоллаут", fallout.Details.Text("ru").Title, "a match from before got its details")
	require.Equal(t, []store.ExternalID{{Provider: "imdb", ID: "tt12637874"}, {Provider: "tmdb", ID: "106379"}, {Provider: "tvdb", ID: "416744"}, {Provider: "wikidata", ID: "Q113127312"}},
		fallout.IDs, "and its IDs")
	require.Equal(t, &store.Details{}, title(t, s, 5).Details, "a work TMDB refuses to tell of left the backfill with empty details")
}

func TestMetrics(t *testing.T) {
	ctx := context.Background()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	f := newTMDB(t)
	f.results["/3/search/movie|Alien|en-US|1979"] = []result{{ID: 348, Title: "Alien", OriginalTitle: "Alien", ReleaseDate: "1979-05-25"}}
	f.alien(t)
	f.throttle.Store(1)
	s := setup(t,
		store.Torrent{Name: "Alien.1979.1080p.BluRay", Kind: "movie", TitleKey: "y:alien 1979"},
		store.Torrent{Name: "Heat.1995.1080p.BluRay", Kind: "movie", TitleKey: "y:heat 1995"},
	)
	m := matcher(f, s)
	m.Client.Meter, m.Meter = mp, mp
	keys, err := m.Unmatched(ctx, nil, 10)
	require.NoError(t, err)
	for _, k := range keys {
		require.NoError(t, m.Match(ctx, k))
	}

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))
	got := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, x := range sm.Metrics {
			for _, p := range x.Data.(metricdata.Sum[int64]).DataPoints {
				for _, kv := range p.Attributes.ToSlice() {
					got[x.Name+" "+kv.Value.Emit()] += p.Value
				}
			}
		}
	}
	require.Equal(t, map[string]int64{
		"sniffarr.tmdb.matches tmdb": 1,
		"sniffarr.tmdb.matches none": 1,
		"sniffarr.tmdb.requests 200": 4,
		"sniffarr.tmdb.requests 429": 1,
	}, got, "two searches and two details answered, one search throttled first")
}
