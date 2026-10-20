package titles

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
// language, and external IDs from ids, keyed by path.
type fakeTMDB struct {
	*httptest.Server
	mu       sync.Mutex
	results  map[string][]result
	ids      map[string]map[string]any
	searches []string
	// throttle is how many 429 answers come first.
	throttle atomic.Int64
	requests atomic.Int64
}

func newTMDB(t *testing.T) *fakeTMDB {
	f := &fakeTMDB{results: map[string][]result{}, ids: map[string]map[string]any{}}
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
		ids, ok := f.ids[r.URL.Path]
		if !ok {
			http.Error(w, "{}", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(ids)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeTMDB) client() *TMDB {
	c := NewTMDB("secret", 1000)
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

func setup(t *testing.T, torrents ...store.Torrent) *store.Store {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	s, err := store.New(ctx, db, store.Options{Titles: true})
	require.NoError(t, err)
	for i, tr := range torrents {
		tr.Hash = store.Hash{byte(i + 1)}
		tr.Size, tr.Discovery, tr.SeenAt, tr.KindLayer = 1<<30, "sample", time.Now(), store.LayerRules
		_, _, err := s.Insert(ctx, tr)
		require.NoError(t, err)
	}
	return s
}

func title(t *testing.T, s *store.Store, b byte) store.Title {
	t.Helper()
	r, err := s.Get(context.Background(), store.Hash{b})
	require.NoError(t, err)
	return r.Title
}

func TestMatchOneCandidate(t *testing.T) {
	ctx := context.Background()
	f := newTMDB(t)
	f.results["/3/search/movie|Alien|en-US|1979"] = []result{
		{ID: 348, Title: "Alien", OriginalTitle: "Alien", ReleaseDate: "1979-05-25"},
		{ID: 999, Title: "Alien Sex Fiend", OriginalTitle: "Alien Sex Fiend", ReleaseDate: "1979-01-01"},
	}
	f.ids["/3/movie/348/external_ids"] = map[string]any{"imdb_id": "tt0078748", "wikidata_id": "Q103569", "tvdb_id": nil}
	f.throttle.Store(2)
	s := setup(t, store.Torrent{Name: "Alien.1979.1080p.BluRay", Kind: "movie", TitleKey: "y:alien 1979"})
	m := &Matcher{TMDB: f.client(), Store: s, Log: slog.New(slog.DiscardHandler)}

	keys, err := m.Unmatched(ctx, nil, 10)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.NoError(t, m.Match(ctx, keys[0]))
	require.Equal(t, store.Title{Year: 1979, Confidence: 1, Layer: store.LayerTMDB, IDs: []store.ExternalID{
		{Provider: "imdb", ID: "tt0078748"}, {Provider: "tmdb", ID: "348"}, {Provider: "wikidata", ID: "Q103569"},
	}}, title(t, s, 1))
	require.EqualValues(t, 4, f.requests.Load(), "two 429 answers are retried")
	keys, err = m.Unmatched(ctx, nil, 10)
	require.NoError(t, err)
	require.Empty(t, keys)
}

func TestMatchBothTitles(t *testing.T) {
	ctx := context.Background()
	f := newTMDB(t)
	f.results["/3/search/tv|Фоллаут|ru-RU|"] = []result{{ID: 106379, Name: "Фоллаут", OriginalName: "Fallout", FirstAirDate: "2024-04-10"}}
	f.results["/3/search/tv|Fallout|en-US|"] = []result{{ID: 106379, Name: "Fallout", OriginalName: "Fallout", FirstAirDate: "2024-04-10"}}
	f.ids["/3/tv/106379/external_ids"] = map[string]any{"imdb_id": "tt12637874", "tvdb_id": 416744}
	s := setup(t, store.Torrent{Name: "Фоллаут / Fallout / Сезон: 1 / Серии: 1-8 из 8 [2024, WEB-DL 1080p] MVO", Kind: "series", TitleKey: "s:фоллаут"})
	m := &Matcher{TMDB: f.client(), Store: s, Log: slog.New(slog.DiscardHandler)}

	keys, err := m.Unmatched(ctx, nil, 10)
	require.NoError(t, err)
	require.NoError(t, m.Match(ctx, keys[0]))
	require.Equal(t, store.Title{Year: 2024, Confidence: 0.9, Layer: store.LayerTMDB, IDs: []store.ExternalID{
		{Provider: "imdb", ID: "tt12637874"}, {Provider: "tmdb", ID: "106379"}, {Provider: "tvdb", ID: "416744"},
	}}, title(t, s, 1), "one work under both titles; a series key has no year")
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
	f.ids["/3/movie/438631/external_ids"] = map[string]any{"imdb_id": "tt1160419"}
	for _, tt := range []struct {
		name    string
		chooser *fakeChooser
		want    store.Title
	}{
		{"picked", &fakeChooser{choice: "1"}, store.Title{Year: 2021, Confidence: 0.7, Layer: store.LayerJev,
			IDs: []store.ExternalID{{Provider: "imdb", ID: "tt1160419"}, {Provider: "tmdb", ID: "438631"}}}},
		{"none of these", &fakeChooser{choice: "none"}, store.Title{}},
		{"no chooser", nil, store.Title{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := setup(t, store.Torrent{Name: "Dune.2021.2160p.WEB-DL", Kind: "movie", TitleKey: "y:dune 2021"})
			m := &Matcher{TMDB: f.client(), Store: s, Log: slog.New(slog.DiscardHandler)}
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
	now := time.Now()
	m := &Matcher{TMDB: f.client(), Store: s, Retry: time.Hour, Now: func() time.Time { return now }, Log: slog.New(slog.DiscardHandler)}
	keys, err := m.Unmatched(ctx, nil, 10)
	require.NoError(t, err)
	require.Len(t, keys, 2)
	for _, k := range keys {
		require.NoError(t, m.Match(ctx, k))
	}
	require.Equal(t, store.Title{}, title(t, s, 1), "no title fits")
	require.Equal(t, store.Title{Error: "tmdb /3/movie/8195/external_ids: 404 Not Found"}, title(t, s, 2), "a refusal is a failed match")

	keys, err = m.Unmatched(ctx, nil, 10)
	require.NoError(t, err)
	require.Empty(t, keys)
	now = now.Add(time.Hour + time.Second)
	keys, err = m.Unmatched(ctx, nil, 10)
	require.NoError(t, err)
	require.Len(t, keys, 2, "both are looked up again after Retry")
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
	f := newTMDB(t)
	f.results["/3/search/movie|Alien|en-US|1979"] = []result{{ID: 348, Title: "Alien", OriginalTitle: "Alien", ReleaseDate: "1979-05-25"}}
	f.ids["/3/movie/348/external_ids"] = map[string]any{"imdb_id": "tt0078748"}
	s := setup(t,
		store.Torrent{Name: "Alien.1979.1080p.BluRay", Kind: "movie", TitleKey: "y:alien 1979"},
		store.Torrent{Name: "Severance.S02E01", Kind: "series", TitleKey: "s:severance"},
		store.Torrent{Name: "Moon.Safari.1998.FLAC", Kind: "music", TitleKey: "y:moon safari 1998"},
	)
	m := &Matcher{TMDB: f.client(), Store: s, Log: slog.New(slog.DiscardHandler)}
	w := &Worker{Matcher: m, Concurrency: 2, Poll: 10 * time.Millisecond, Log: slog.New(slog.DiscardHandler)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- w.Run(ctx) }()
	require.Eventually(t, func() bool {
		keys, err := m.Unmatched(ctx, nil, 10)
		return err == nil && len(keys) == 0
	}, 5*time.Second, 10*time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.Equal(t, store.LayerTMDB, title(t, s, 1).Layer)
	require.Equal(t, store.Title{}, title(t, s, 2))
}

func TestMetrics(t *testing.T) {
	ctx := context.Background()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	f := newTMDB(t)
	f.results["/3/search/movie|Alien|en-US|1979"] = []result{{ID: 348, Title: "Alien", OriginalTitle: "Alien", ReleaseDate: "1979-05-25"}}
	f.ids["/3/movie/348/external_ids"] = map[string]any{}
	f.throttle.Store(1)
	s := setup(t,
		store.Torrent{Name: "Alien.1979.1080p.BluRay", Kind: "movie", TitleKey: "y:alien 1979"},
		store.Torrent{Name: "Heat.1995.1080p.BluRay", Kind: "movie", TitleKey: "y:heat 1995"},
	)
	c := f.client()
	c.Meter = mp
	m := &Matcher{TMDB: c, Store: s, Meter: mp, Log: slog.New(slog.DiscardHandler)}
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
		"sniffarr.titles.matches tmdb": 1,
		"sniffarr.titles.matches none": 1,
		"sniffarr.titles.requests 200": 3,
		"sniffarr.titles.requests 429": 1,
	}, got)
}
