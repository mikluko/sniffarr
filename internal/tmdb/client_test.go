package tmdb

import (
	"context"
	"encoding/json"
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

	"sarak.sh/mikluko/libarr/telemetry/telemetrytest"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

func TestFindTVDB(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("external_source") != "tvdb_id" || r.URL.Query().Get("api_key") != "secret" {
			http.Error(w, "{}", http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/3/find/81189":
			_, _ = w.Write([]byte(`{"movie_results":[],"tv_results":[{"id":1396,"name":"Breaking Bad"}]}`))
		default:
			_, _ = w.Write([]byte(`{"movie_results":[],"tv_results":[]}`))
		}
	}))
	t.Cleanup(srv.Close)
	c := NewClient("secret", 1000)
	c.URL, c.Backoff, c.Limiter = srv.URL, time.Millisecond, rate.NewLimiter(rate.Inf, 1)

	id, err := c.FindTVDB(context.Background(), 81189)
	require.NoError(t, err)
	require.Equal(t, 1396, id)
	id, err = c.FindTVDB(context.Background(), 1)
	require.NoError(t, err)
	require.Zero(t, id, "TMDB knows no series of the ID")
}

type result struct {
	ID            int      `json:"id"`
	Title         string   `json:"title,omitempty"`
	OriginalTitle string   `json:"original_title,omitempty"`
	ReleaseDate   string   `json:"release_date,omitempty"`
	Name          string   `json:"name,omitempty"`
	OriginalName  string   `json:"original_name,omitempty"`
	FirstAirDate  string   `json:"first_air_date,omitempty"`
	Overview      string   `json:"overview"`
	Popularity    float64  `json:"popularity,omitempty"`
	VoteCount     int      `json:"vote_count,omitempty"`
	Language      string   `json:"original_language,omitempty"`
	Countries     []string `json:"origin_country,omitempty"`
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
	Type: "movie", OriginalTitle: "Alien", Year: 1979, PosterPath: "/vfrQk5IPloGg1v9Rzbh2Eg3VGyM.jpg", Rating: 8.176, Votes: 16995, Runtime: 117,
	Countries: []string{"US"}, Language: "en",
	Texts: map[string]store.Text{
		"en": {Title: "Alien", Overview: "During its return to the earth, commercial spaceship Nostromo intercepts a distress signal from a distant planet. When a three-member team of the crew discovers a chamber containing thousands of eggs on the planet, a creature inside one of the eggs attacks an explorer. The entire crew is unaware of the impending nightmare set to descend upon them when the alien parasite planted inside its unfortunate host is birthed.", Genres: []string{"Horror", "Science Fiction"}, PosterPath: "/vfrQk5IPloGg1v9Rzbh2Eg3VGyM.jpg"},
		"ru": {Title: "Чужой", Overview: "В далеком будущем возвращающийся на Землю грузовой космический корабль перехватывает исходящий с неизвестной планеты сигнал. Экипаж, в соответствии с основными инструкциями, обязан найти и исследовать источник сигнала. Оказавшись на планете, астронавты повсюду обнаруживают предметы, по виду напоминающие гигантские коконы.", Genres: []string{"ужасы", "фантастика"}, PosterPath: "/zg4fcO8IuYRllK9wWVNqNlYpueL.jpg"},
	},
}

func (f *fakeTMDB) client() *Client {
	c := NewClient("secret", 1000)
	c.URL, c.Backoff = f.URL, time.Millisecond
	c.Limiter = rate.NewLimiter(rate.Inf, 1)
	return c
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

func TestTMDBErrorsHideKey(t *testing.T) {
	f := newTMDB(t)
	c := f.client()
	f.Close()
	_, err := c.Search(context.Background(), false, "Alien", 1979, "en-US")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret")
}

func TestClientTrace(t *testing.T) {
	f := newTMDB(t)
	reader := sdkmetric.NewManualReader()
	c := f.client()
	c.Meter = sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	telemetrytest.RequireClientChild(t, func(ctx context.Context) {
		_, err := c.Search(ctx, false, "Alien", 1979, "en-US")
		require.NoError(t, err)
	})
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	var names []string
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			names = append(names, m.Name)
		}
	}
	require.Contains(t, names, "http.client.request.duration", "the transport records into the client's Meter")
}
