package tmdb

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"sarak.sh/mikluko/libarr/database"
	"sarak.sh/mikluko/sniffarr/internal/classify"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

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
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{Service: "sniffarr"})
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

func keyNames(keys []store.TitleKey) []string {
	var out []string
	for _, k := range keys {
		out = append(out, k.Key)
	}
	return out
}

// match is the match of the title key of the torrent b.
func match(t *testing.T, s *store.Store, b byte) store.Match {
	t.Helper()
	r, err := s.Get(context.Background(), store.Hash{b})
	require.NoError(t, err)
	return r.Match
}

// outcome is what a test asserts of a match: its work's TMDB ID, IDs and
// details, and how it was made.
type outcome struct {
	TMDBID     int
	IDs        []store.ExternalID
	Details    store.Details
	Confidence float64
	Layer      string
}

func outcomeOf(t *testing.T, s *store.Store, b byte) outcome {
	t.Helper()
	m := match(t, s, b)
	require.NotNil(t, m.Work, "matched")
	require.Equal(t, now, m.MatchedAt)
	return outcome{TMDBID: m.Work.TMDBID, IDs: m.Work.ExternalIDs(), Details: m.Work.Details, Confidence: m.Confidence, Layer: m.Layer}
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
	require.NoError(t, m.Match(ctx, keys[0], store.TriggerWorker))
	require.Equal(t, outcome{TMDBID: 348, IDs: []store.ExternalID{{Provider: "imdb", ID: "tt0078748"}, {Provider: "tmdb", ID: "348"}, {Provider: "wikidata", ID: "Q103569"}},
		Details: *alienDetails, Confidence: 1, Layer: store.LayerTMDB}, outcomeOf(t, s, 1), "the match, its IDs from the details, and the details in both languages")
	require.EqualValues(t, 5, f.requests.Load(), "two 429 answers are retried; one search, two details")
	keys, err = m.Unmatched(ctx, nil, 10)
	require.NoError(t, err)
	require.Empty(t, keys)
	due, err := s.WithoutDetails(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, due)
}

func TestMatchSharedWork(t *testing.T) {
	ctx := context.Background()
	f := newTMDB(t)
	f.results["/3/search/movie|Alien|en-US|1979"] = []result{{ID: 348, Title: "Alien", OriginalTitle: "Alien", ReleaseDate: "1979-05-25"}}
	f.results["/3/search/movie|Чужой|en-US|1979"] = []result{{ID: 348, Title: "Alien", OriginalTitle: "Alien", ReleaseDate: "1979-05-25"}}
	f.results["/3/search/movie|Чужой|ru-RU|1979"] = []result{{ID: 348, Title: "Чужой", OriginalTitle: "Alien", ReleaseDate: "1979-05-25"}}
	f.alien(t)
	s := setup(t,
		store.Torrent{Name: "Alien.1979.1080p.BluRay", Kind: "movie", TitleKey: "y:alien 1979"},
		store.Torrent{Name: "Чужой (1979) BDRip 1080p", Kind: "movie", TitleKey: "y:чужой 1979"},
	)
	m := matcher(f, s)
	keys, err := m.Unmatched(ctx, nil, 10)
	require.NoError(t, err)
	require.Len(t, keys, 2)
	for _, k := range keys {
		require.NoError(t, m.Match(ctx, k, store.TriggerWorker))
	}
	require.Equal(t, match(t, s, 1).Work.ID, match(t, s, 2).Work.ID, "two title keys, one work")
	require.EqualValues(t, 1+2+2, f.requests.Load(), "the second key's searches, then one search and the details once")
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
	require.NoError(t, m.Match(ctx, keys[0], store.TriggerWorker))
	got := outcomeOf(t, s, 1)
	require.Equal(t, []store.ExternalID{
		{Provider: "imdb", ID: "tt12637874"}, {Provider: "tmdb", ID: "106379"}, {Provider: "tvdb", ID: "416744"}, {Provider: "wikidata", ID: "Q113127312"},
	}, got.IDs, "one work under both titles")
	require.Equal(t, 2024, got.Details.Year)
	require.Equal(t, 0.9, got.Confidence, "a series key has no year")
	require.Equal(t, "tv", got.Details.Type)
	require.Equal(t, "Returning Series", got.Details.Status)
	require.Equal(t, 2, got.Details.Seasons)
	require.Equal(t, "Фоллаут", got.Details.Text("ru").Title)
	require.Equal(t, "/7o3XRf31lEtAaRNtgupOGTDD3sP.jpg", got.Details.Text("ru").PosterPath, "the poster of the Russian page")
	require.Equal(t, "/c15BtJxCXMrISLVmysdsnZUPQft.jpg", got.Details.Text("en").PosterPath, "the poster of the English page")
	require.Equal(t, "/c15BtJxCXMrISLVmysdsnZUPQft.jpg", got.Details.PosterPath, "the work's poster is the first language's")
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
		f.details["/3/movie/438631|"+lang] = map[string]any{"id": 438631, "title": "Dune", "original_title": "Dune", "release_date": "2021-09-15",
			"external_ids": map[string]any{"imdb_id": "tt1160419"}}
	}
	for _, tt := range []struct {
		name    string
		chooser *fakeChooser
		picked  bool
	}{
		{"picked", &fakeChooser{choice: "1"}, true},
		{"none of these", &fakeChooser{choice: "none"}, false},
		{"no chooser", nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := setup(t, store.Torrent{Name: "Dune.2021.2160p.WEB-DL", Kind: "movie", TitleKey: "y:dune 2021"})
			m := matcher(f, s)
			if tt.chooser != nil {
				m.Chooser = tt.chooser
			}
			keys, err := m.Unmatched(ctx, nil, 10)
			require.NoError(t, err)
			require.NoError(t, m.Match(ctx, keys[0], store.TriggerWorker))
			if tt.picked {
				require.Equal(t, outcome{TMDBID: 438631, IDs: []store.ExternalID{{Provider: "imdb", ID: "tt1160419"}, {Provider: "tmdb", ID: "438631"}},
					Details:    store.Details{Type: "movie", OriginalTitle: "Dune", Year: 2021, Texts: map[string]store.Text{"en": {Title: "Dune"}, "ru": {Title: "Dune"}}},
					Confidence: 0.7, Layer: store.LayerClassifier}, outcomeOf(t, s, 1))
			} else {
				require.Equal(t, store.Match{MatchedAt: now}, match(t, s, 1))
			}
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

func TestMatchDominant(t *testing.T) {
	ctx := context.Background()
	f := newTMDB(t)
	f.results["/3/search/movie|Runner|en-US|2026"] = []result{
		{ID: 1477049, Title: "Runner", OriginalTitle: "Corredora", ReleaseDate: "2026-05-29", Popularity: 3.2, VoteCount: 4, Language: "es"},
		{ID: 1377237, Title: "Runner", OriginalTitle: "Runner", ReleaseDate: "2026-09-07", Popularity: 207.8, VoteCount: 61, Language: "en", Overview: "A run. Then another."},
		{ID: 1712475, Title: "RUNNER", OriginalTitle: "RUNNER", ReleaseDate: "2026-10-23", Popularity: 1.1},
	}
	for _, lang := range []string{"en-US", "ru-RU"} {
		f.details["/3/movie/1377237|"+lang] = map[string]any{"id": 1377237, "title": "Runner", "original_title": "Runner", "release_date": "2026-09-07",
			"external_ids": map[string]any{"imdb_id": "tt31349844"}}
	}
	s := setup(t, store.Torrent{Name: "Runner.2026.VOSTFR.1080p.WEBRip.x265-GL0P.mkv", Kind: "movie", TitleKey: "y:runner 2026"})
	m := matcher(f, s)
	keys, err := m.Unmatched(ctx, nil, 10)
	require.NoError(t, err)
	require.NoError(t, m.Match(ctx, keys[0], store.TriggerWorker))
	require.Equal(t, outcome{TMDBID: 1377237, IDs: []store.ExternalID{{Provider: "imdb", ID: "tt31349844"}, {Provider: "tmdb", ID: "1377237"}},
		Details:    store.Details{Type: "movie", OriginalTitle: "Runner", Year: 2026, Texts: map[string]store.Text{"en": {Title: "Runner"}, "ru": {Title: "Runner"}}},
		Confidence: 0.8, Layer: store.LayerTMDB}, outcomeOf(t, s, 1), "the far more popular fit is taken without a Chooser")
	ws, err := s.Works(ctx, []int64{match(t, s, 1).Work.ID})
	require.NoError(t, err)
	for _, w := range ws {
		require.Equal(t, 207.8, w.Popularity, "the work keeps the search answer's popularity")
	}

	f.results["/3/search/movie|Runner|en-US|2026"][1].Popularity = 30
	s = setup(t, store.Torrent{Name: "Runner.2026.VOSTFR.1080p.WEBRip.x265-GL0P.mkv", Kind: "movie", TitleKey: "y:runner 2026"})
	m = matcher(f, s)
	chooser := &fakeChooser{choice: "none"}
	m.Chooser = chooser
	keys, err = m.Unmatched(ctx, nil, 10)
	require.NoError(t, err)
	require.NoError(t, m.Match(ctx, keys[0], store.TriggerWorker))
	require.Equal(t, store.Match{MatchedAt: now}, match(t, s, 1), "ten times the next is the bar")
	require.Equal(t, "Runner, 2026 [language en; popularity 30.0; 61 votes]: A run", chooser.options["2"])
	require.Equal(t, "Runner (Corredora), 2026 [language es; popularity 3.2; 4 votes]", chooser.options["1"])
	require.Equal(t, "RUNNER, 2026 [popularity 1.1]", chooser.options["3"])
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
		require.NoError(t, m.Match(ctx, k, store.TriggerWorker))
	}
	require.Equal(t, store.Match{MatchedAt: now}, match(t, s, 1), "no title fits")
	require.Equal(t, outcome{TMDBID: 8195, IDs: []store.ExternalID{{Provider: "tmdb", ID: "8195"}},
		Details:    store.Details{Type: "movie", OriginalTitle: "Ronin", Year: 1998, Texts: map[string]store.Text{"en": {Title: "Ronin"}}},
		Confidence: 1, Layer: store.LayerTMDB}, outcomeOf(t, s, 2), "a work TMDB refuses to tell of keeps what its search told")
	due, err := s.WithoutDetails(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, due, "and is not asked about again")

	keys, err = m.Unmatched(ctx, nil, 10)
	require.NoError(t, err)
	require.Empty(t, keys)
	keys, err = m.Matchable(ctx, []string{"y:heat 1995", "y:ronin 1998"})
	require.NoError(t, err)
	require.Equal(t, []string{"y:heat 1995"}, keyNames(keys), "a request looks the failed match up again before Retry, the match never")
	keys, err = m.Matchable(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, keys, "no keys name none")
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
	require.NoError(t, m.Match(ctx, keys[0], store.TriggerWorker), "the match is recorded though its details failed")
	got := match(t, s, 1)
	require.Equal(t, 348, got.Work.TMDBID)
	require.True(t, got.Work.DetailsAt.IsZero())
	due, err := s.WithoutDetails(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, []store.Due{{Work: got.Work.ID, Type: "movie", TMDBID: 348}}, due, "the details are due")

	require.Error(t, m.Details(ctx, due[0]), "the Russian answer is missing")
	require.True(t, match(t, s, 1).Work.DetailsAt.IsZero())
	f.fixture(t, "/3/movie/348", "ru-RU", "movie_ru.json")
	require.NoError(t, m.Details(ctx, due[0]))
	o := outcomeOf(t, s, 1)
	require.Equal(t, *alienDetails, o.Details)
	require.Equal(t, []store.ExternalID{{Provider: "imdb", ID: "tt0078748"}, {Provider: "tmdb", ID: "348"}, {Provider: "wikidata", ID: "Q103569"}}, o.IDs,
		"the IDs come with the details")
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
		require.NoError(t, m.Match(ctx, k, store.TriggerWorker))
	}

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))
	got := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != scope {
			continue
		}
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
