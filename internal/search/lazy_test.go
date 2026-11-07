package search

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/database"
	"github.com/mikluko/sniffarr/internal/rules"
	"github.com/mikluko/sniffarr/internal/store"
)

// fakeClassifier classifies by name: answers maps a name to its classification,
// and a name it lacks fails. It holds each answer for delay.
type fakeClassifier struct {
	store    *store.Store
	delay    time.Duration
	answers  map[string]store.Classification
	mu       sync.Mutex
	calls    map[int64]int
	triggers []string
}

func (f *fakeClassifier) Classify(ctx context.Context, p store.Pending, trigger string) error {
	f.mu.Lock()
	f.calls[p.ID]++
	f.triggers = append(f.triggers, trigger)
	f.mu.Unlock()
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return ctx.Err()
	}
	c, ok := f.answers[p.Name]
	if !ok {
		return errors.New("classifier down")
	}
	if p.KindKnown {
		c.Kind = ""
	}
	c.Trigger = trigger
	_, err := f.store.Classify(ctx, p.ID, c, time.Now())
	return err
}

func (f *fakeClassifier) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		n += c
	}
	return n
}

func setup(t *testing.T, torrents ...store.Torrent) (*store.Store, *fakeClassifier) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	s, err := store.New(ctx, db, store.Options{Languages: []string{"ru"}, TMDB: true})
	require.NoError(t, err)
	for i, tr := range torrents {
		tr.Hash = store.Hash{byte(i + 1)}
		tr.Size, tr.Discovery, tr.SeenAt = 1<<30, "sample", time.Now().Add(time.Duration(i)*time.Second)
		tr.Files = []rules.File{{Path: tr.Name + ".mkv", Size: 1 << 30}}
		_, _, err := s.Insert(ctx, tr)
		require.NoError(t, err)
	}
	return s, &fakeClassifier{store: s, calls: map[int64]int{}, answers: map[string]store.Classification{}}
}

func search(t *testing.T, svc *Service, req *sniffarrv1.SearchRequest) map[string]*sniffarrv1.Release {
	t.Helper()
	res, err := svc.Search(context.Background(), connect.NewRequest(req))
	require.NoError(t, err)
	out := map[string]*sniffarrv1.Release{}
	for _, r := range res.Msg.GetReleases() {
		out[r.GetName()] = r
	}
	return out
}

func TestLazyClassifiesAndCaches(t *testing.T) {
	s, cl := setup(t,
		store.Torrent{Name: "Heat 1995"},
		store.Torrent{Name: "Ronin S01E01", Kind: "series", KindLayer: store.LayerRules},
	)
	cl.answers["Heat 1995"] = store.Classification{Kind: "movie", Source: "remux", Audio: map[string]float64{"ru": 0.8}}
	cl.answers["Ronin S01E01"] = store.Classification{Kind: "movie", Source: "web", Audio: map[string]float64{"ru": 0.1}}
	svc := &Service{Store: s, Log: slog.New(slog.DiscardHandler), Lazy: &Lazy{Classifier: cl, Log: slog.New(slog.DiscardHandler)}}

	got := search(t, svc, &sniffarrv1.SearchRequest{})
	require.Len(t, got, 2)
	heat := got["Heat 1995"]
	require.Equal(t, sniffarrv1.Kind_KIND_MOVIE, heat.GetKind())
	require.Equal(t, sniffarrv1.Layer_LAYER_CLASSIFIER, heat.GetLayer())
	require.Equal(t, sniffarrv1.VideoSource_VIDEO_SOURCE_REMUX, heat.GetSource())
	require.True(t, heat.GetClassified())
	ronin := got["Ronin S01E01"]
	require.Equal(t, sniffarrv1.Kind_KIND_SERIES, ronin.GetKind(), "the rules' kind stays")
	require.Equal(t, sniffarrv1.Layer_LAYER_RULES, ronin.GetLayer())
	require.Equal(t, sniffarrv1.VideoSource_VIDEO_SOURCE_WEB, ronin.GetSource())
	require.Equal(t, 2, cl.total())

	search(t, svc, &sniffarrv1.SearchRequest{})
	require.Equal(t, 2, cl.total(), "the second search uses the recorded answers")
	require.Equal(t, []string{store.TriggerSearch, store.TriggerSearch}, cl.triggers, "a search that waits is the trigger")
}

func TestLazyFiltersOnWhatIsKnown(t *testing.T) {
	s, cl := setup(t,
		store.Torrent{Name: "Heat 1995"},
		store.Torrent{Name: "Ronin 1998"},
		store.Torrent{Name: "Thief 1981"},
		store.Torrent{Name: "Collateral 2004", Kind: "movie", KindLayer: store.LayerRules},
	)
	cl.answers["Heat 1995"] = store.Classification{Kind: "movie", Audio: map[string]float64{"ru": 0.9}}
	cl.answers["Ronin 1998"] = store.Classification{Kind: "music", Audio: map[string]float64{"ru": 0.9}}
	cl.answers["Collateral 2004"] = store.Classification{Audio: map[string]float64{"ru": 0.2}}
	svc := &Service{Store: s, Log: slog.New(slog.DiscardHandler), Lazy: &Lazy{Classifier: cl, Log: slog.New(slog.DiscardHandler)}}

	got := search(t, svc, &sniffarrv1.SearchRequest{Kinds: []sniffarrv1.Kind{sniffarrv1.Kind_KIND_MOVIE}})
	require.ElementsMatch(t, []string{"Heat 1995", "Collateral 2004"}, keys(got), "Ronin turned out music; Thief failed and stays unknown")

	got = search(t, svc, &sniffarrv1.SearchRequest{AudioLanguage: new("ru"), MinAudioProbability: new(float32(0.5))})
	require.ElementsMatch(t, []string{"Heat 1995", "Ronin 1998"}, keys(got))
	require.Len(t, got["Heat 1995"].GetAudio(), 1)
	require.Equal(t, "ru", got["Heat 1995"].GetAudio()[0].GetLanguage())
	require.InDelta(t, 0.9, got["Heat 1995"].GetAudio()[0].GetProbability(), 1e-6)

	_, err := svc.Search(context.Background(), connect.NewRequest(&sniffarrv1.SearchRequest{MinAudioProbability: new(float32(0.5))}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "a probability needs its language")
}

// fakeTMDB matches a key to the IDs in ids, or to nothing, after delay.
type fakeTMDB struct {
	store *store.Store
	delay time.Duration
	ids   map[string][]store.ExternalID
	mu    sync.Mutex
	asked []string
}

func (f *fakeTMDB) Unmatched(ctx context.Context, keys []string, limit int) ([]store.TitleKey, error) {
	return f.store.UnmatchedTitles(ctx, keys, time.Now().Add(-time.Hour), limit)
}

func (f *fakeTMDB) Match(ctx context.Context, k store.TitleKey, trigger string) error {
	f.mu.Lock()
	f.asked = append(f.asked, k.Key)
	f.mu.Unlock()
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return ctx.Err()
	}
	ids, ok := f.ids[k.Key]
	if !ok {
		_, _, err := f.store.RecordMatch(ctx, k.Key, store.Found{}, time.Now())
		return err
	}
	var tmdbID int
	var others []store.ExternalID
	for _, id := range ids {
		if id.Provider == store.ProviderTMDB {
			tmdbID, _ = strconv.Atoi(id.ID)
		} else {
			others = append(others, id)
		}
	}
	work, _, err := f.store.RecordMatch(ctx, k.Key, store.Found{Hit: &store.Hit{Type: store.TypeOf(k.Kind), TMDBID: tmdbID}, Confidence: 1, Layer: store.LayerTMDB}, time.Now())
	if err != nil {
		return err
	}
	return f.store.RecordDetails(ctx, work, store.Details{Type: store.TypeOf(k.Kind)}, others, time.Now())
}

func TestLazyTMDB(t *testing.T) {
	s, _ := setup(t,
		store.Torrent{Name: "Heat.1995.1080p", Kind: "movie", KindLayer: store.LayerRules, TitleKey: "y:heat 1995"},
		store.Torrent{Name: "Heat.1995.2160p", Kind: "movie", KindLayer: store.LayerRules, TitleKey: "y:heat 1995"},
		store.Torrent{Name: "Ronin.1998.1080p", Kind: "movie", KindLayer: store.LayerRules, TitleKey: "y:ronin 1998"},
		store.Torrent{Name: "Moon.Safari.1998", Kind: "music", KindLayer: store.LayerRules, TitleKey: "y:moon safari 1998"},
		store.Torrent{Name: "Thief.1981", TitleKey: "y:thief 1981"},
	)
	matcher := &fakeTMDB{store: s, ids: map[string][]store.ExternalID{"y:heat 1995": {{Provider: "tmdb", ID: "949"}, {Provider: "imdb", ID: "tt0113277"}}}}
	svc := &Service{Store: s, Log: slog.New(slog.DiscardHandler), Lazy: &Lazy{TMDB: matcher, Log: slog.New(slog.DiscardHandler)}}

	got := search(t, svc, &sniffarrv1.SearchRequest{})
	require.Len(t, got, 5)
	heat := got["Heat.1995.2160p"]
	require.Equal(t, sniffarrv1.TMDBLayer_TMDB_LAYER_SEARCH, heat.GetTmdbLayer())
	require.InDelta(t, 1, heat.GetTmdbConfidence(), 1e-6)
	require.Len(t, heat.GetIds(), 2)
	require.Equal(t, "imdb", heat.GetIds()[0].GetProvider(), "in provider order")
	require.Equal(t, sniffarrv1.TMDBLayer_TMDB_LAYER_UNSPECIFIED, got["Ronin.1998.1080p"].GetTmdbLayer())
	require.ElementsMatch(t, []string{"y:heat 1995", "y:ronin 1998"}, matcher.asked, "each key once, movies and series only, no classifier needed")

	search(t, svc, &sniffarrv1.SearchRequest{})
	require.Len(t, matcher.asked, 2, "recorded matches are not looked up again")

	got = search(t, svc, &sniffarrv1.SearchRequest{Id: &sniffarrv1.ExternalId{Provider: "imdb", Id: "tt0113277"}})
	require.ElementsMatch(t, []string{"Heat.1995.1080p", "Heat.1995.2160p"}, keys(got), "every release of the work")
	_, err := svc.Search(context.Background(), connect.NewRequest(&sniffarrv1.SearchRequest{Id: &sniffarrv1.ExternalId{Provider: "imdb"}}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestLazyTMDBLate(t *testing.T) {
	s, _ := setup(t, store.Torrent{Name: "Heat.1995.1080p", Kind: "movie", KindLayer: store.LayerRules, TitleKey: "y:heat 1995"})
	matcher := &fakeTMDB{store: s, delay: 300 * time.Millisecond, ids: map[string][]store.ExternalID{"y:heat 1995": {{Provider: "tmdb", ID: "949"}}}}
	svc := &Service{Store: s, Log: slog.New(slog.DiscardHandler), Lazy: &Lazy{TMDB: matcher, Wait: 50 * time.Millisecond, Log: slog.New(slog.DiscardHandler)}}

	got := search(t, svc, &sniffarrv1.SearchRequest{})
	require.Empty(t, got["Heat.1995.1080p"].GetIds(), "not waited for")
	require.Eventually(t, func() bool {
		return len(search(t, svc, &sniffarrv1.SearchRequest{})["Heat.1995.1080p"].GetIds()) == 1
	}, 5*time.Second, 50*time.Millisecond, "recorded for later searches")
}

func TestLazyBounded(t *testing.T) {
	var torrents []store.Torrent
	for _, n := range []string{"A 2001", "B 2002", "C 2003", "D 2004", "E 2005"} {
		torrents = append(torrents, store.Torrent{Name: n})
	}
	s, cl := setup(t, torrents...)
	svc := &Service{Store: s, Log: slog.New(slog.DiscardHandler), Lazy: &Lazy{Classifier: cl, Max: 2, Log: slog.New(slog.DiscardHandler)}}
	search(t, svc, &sniffarrv1.SearchRequest{})
	require.Equal(t, 2, cl.total())
}

func TestLazySlowClassifierDoesNotFailSearch(t *testing.T) {
	s, cl := setup(t, store.Torrent{Name: "Heat 1995"})
	cl.answers["Heat 1995"] = store.Classification{Kind: "movie", Source: "remux", Audio: map[string]float64{"ru": 0.8}}
	cl.delay = 300 * time.Millisecond
	svc := &Service{Store: s, Log: slog.New(slog.DiscardHandler), Lazy: &Lazy{Classifier: cl, Wait: 20 * time.Millisecond, Log: slog.New(slog.DiscardHandler)}}

	start := time.Now()
	got := search(t, svc, &sniffarrv1.SearchRequest{})
	require.Less(t, time.Since(start), 200*time.Millisecond)
	require.False(t, got["Heat 1995"].GetClassified(), "answered with what was known")

	require.Eventually(t, func() bool {
		r, err := s.Get(context.Background(), store.Hash{1})
		return err == nil && r.Classified
	}, 2*time.Second, 10*time.Millisecond, "the late answer is recorded")
	cl.delay = 0
	got = search(t, svc, &sniffarrv1.SearchRequest{})
	require.Equal(t, sniffarrv1.Kind_KIND_MOVIE, got["Heat 1995"].GetKind())
	require.Equal(t, 1, cl.total())
}

func TestLazyNoWait(t *testing.T) {
	s, cl := setup(t,
		store.Torrent{Name: "Heat 1995"},
		store.Torrent{Name: "Ronin 1998", Kind: "movie", KindLayer: store.LayerRules},
	)
	cl.answers["Heat 1995"] = store.Classification{Kind: "movie", Source: "remux"}
	cl.answers["Ronin 1998"] = store.Classification{Source: "web"}
	cl.delay = 100 * time.Millisecond
	svc := &Service{Store: s, Log: slog.New(slog.DiscardHandler), NoWait: true,
		Lazy: &Lazy{Classifier: cl, Wait: time.Minute, Log: slog.New(slog.DiscardHandler)}}
	movies := &sniffarrv1.SearchRequest{Kinds: []sniffarrv1.Kind{sniffarrv1.Kind_KIND_MOVIE}}

	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	res, err := svc.Search(ctx, connect.NewRequest(movies))
	cancel()
	require.NoError(t, err)
	require.Less(t, time.Since(start), cl.delay, "not waited for")
	require.Len(t, res.Msg.GetReleases(), 1)
	require.Equal(t, "Ronin 1998", res.Msg.GetReleases()[0].GetName(), "the kind the store holds; Heat's is still unknown")
	require.False(t, res.Msg.GetReleases()[0].GetClassified())

	require.Eventually(t, func() bool {
		return len(search(t, svc, movies)) == 2
	}, 5*time.Second, 20*time.Millisecond, "the classifications run past the request and serve later searches")
	require.Equal(t, 2, cl.total())
	require.Equal(t, []string{store.TriggerTorznab, store.TriggerTorznab}, cl.triggers, "a search that does not wait is Torznab's")
}

func TestLazyClassifyRecordedMeanwhile(t *testing.T) {
	s, cl := setup(t, store.Torrent{Name: "Heat 1995"})
	cl.answers["Heat 1995"] = store.Classification{Source: "web"}
	l := &Lazy{Classifier: cl, Log: slog.New(slog.DiscardHandler)}
	ctx := context.Background()
	r, err := s.Get(ctx, store.Hash{1})
	require.NoError(t, err)
	pending, err := s.Pending(ctx, []int64{r.ID}, 0)
	require.NoError(t, err)
	require.Len(t, pending, 1)

	_, err = s.Classify(ctx, r.ID, store.Classification{Source: "remux"}, time.Now())
	require.NoError(t, err)
	require.NoError(t, l.classify(ctx, s, pending[0], store.TriggerSearch))
	require.Zero(t, cl.total(), "recorded since it was read")
}

func keys(m map[string]*sniffarrv1.Release) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
