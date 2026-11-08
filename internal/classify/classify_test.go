package classify

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"sarak.sh/mikluko/libarr/database"
	"sarak.sh/mikluko/libarr/telemetry/telemetrytest"
	"sarak.sh/mikluko/sniffarr/internal/audio"
	"sarak.sh/mikluko/sniffarr/internal/rules"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

const answer200 = `{
  "model": "jev-1.13.0",
  "answers": {
    "kind": {"type": "choice", "choice": "movie", "probabilities": {"movie": 0.9, "series": 0.1}, "confidence": 0.85},
    "audio_ru": {"type": "noul", "noul": 0.75},
    "source": {"type": "choice", "choice": "remux", "probabilities": {"remux": 0.7, "encode": 0.3}, "confidence": 0.6}
  },
  "usage": {"input_tokens": 900, "output_tokens": 100}
}`

var russian, _ = audio.Parse("ru")

func TestQuestions(t *testing.T) {
	q := questions(true, russian)
	require.ElementsMatch(t, []string{"kind", "source", "audio_ru"}, slices.Collect(maps.Keys(q)))
	require.Equal(t, map[string]any{
		"type":         "noul",
		"instructions": "Does this release carry a Russian-language audio track (dubbing, voice-over, or Russian original), judging by its `name` and `files`?",
		"criteria": map[string]any{
			"true": map[string]any{
				"signs": "Russian audio is named or clearly implied: Rus, RUS, rus in a file or folder name, Дубляж, Dub, MVO, DVO, AVO, ПМ, " +
					"a Russian translation code near the year or the source, D dub, P, P1, P2 professional, L, L1, L2 amateur, A author's, Pm, " +
					"as in Movie.2026.P.WEB-DL or BDRip | D, P2, " +
					"a separate Rus*.ac3/.mka/.aac track, a Russian-language title of a video release",
				"studios": "A Russian dubbing or voice-over studio or release group: lostfilm, newstudio, jaskier, hdrezka, кубик в кубе, alexfilm, " +
					"baibako, coldfilm, tvshows, novamedia, amedia, кураж-бамбей, red head sound, rhs, гоблин, anilibria, anidub, shiza, anistar, " +
					"dream cast, selezen, exkinoray, scarabey, hellywood, generalfilm",
			},
			"false": "No sign of Russian audio, or not a video release",
		},
	}, q["audio_ru"])
	require.ElementsMatch(t, []string{"source"}, slices.Collect(maps.Keys(questions(false, nil))), "no audio language, no audio question")
}

type fakeEndpoint struct {
	*httptest.Server
	requests, kindAsked atomic.Int64
	// status is answered for a request whose name contains the key.
	status map[string]int
	// overload is how many 529 answers come before the first 200.
	overload atomic.Int64
}

func newFake(t *testing.T) *fakeEndpoint {
	f := &fakeEndpoint{status: map[string]int{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct {
			Model string `json:"model"`
			State struct {
				Name  string   `json:"name"`
				Files []string `json:"files"`
			} `json:"state"`
			Questions map[string]struct {
				Type string `json:"type"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if q, ok := req.Questions["choice"]; ok {
			if q.Type != "choice" || len(req.Questions) != 1 {
				http.Error(w, "bad questions", http.StatusUnprocessableEntity)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"answers": {"choice": {"type": "choice", "choice": "b", "confidence": 0.7}}, "usage": {"input_tokens": 400, "output_tokens": 0}}`))
			return
		}
		kind, askKind := req.Questions["kind"]
		if req.Model != "jev-latest" || askKind && kind.Type != "choice" || req.Questions["audio_ru"].Type != "noul" || req.Questions["source"].Type != "choice" {
			http.Error(w, "bad questions", http.StatusUnprocessableEntity)
			return
		}
		if askKind {
			f.kindAsked.Add(1)
		}
		for k, code := range f.status {
			if strings.Contains(req.State.Name, k) {
				http.Error(w, `{"error":"max_tokens_exceeded"}`, code)
				return
			}
		}
		if f.overload.Add(-1) >= 0 {
			http.Error(w, "overloaded", 529)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(answer200))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeEndpoint) client() *Client {
	c := New("key")
	c.Languages = russian
	c.URL = f.URL + "/v1/systemone"
	c.Backoff = time.Millisecond
	return c
}

func TestClientClassify(t *testing.T) {
	f := newFake(t)
	f.overload.Store(2)
	r, err := f.client().Classify(context.Background(), Input{Name: "Heat.1995.1080p.BluRay.REMUX", Size: 30 << 30, Files: []rules.File{{Path: "Heat.mkv", Size: 30 << 30}}, AskKind: true})
	require.NoError(t, err)
	require.Equal(t, Result{Kind: "movie", KindConfidence: 0.85, Source: "remux", SourceConfidence: 0.6, Audio: map[string]float64{"ru": 0.75}, Tokens: 1000}, r)
	require.EqualValues(t, 3, f.requests.Load(), "two 529 answers are retried")
}

func TestChoose(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	in := ChoiceInput{State: map[string]any{"name": "Alien 1979"}, Instructions: "Which?", Options: map[string]string{"a": "first", "b": "second"}}
	r, err := f.client().Choose(ctx, in)
	require.NoError(t, err)
	require.Equal(t, ChoiceResult{Choice: "b", Confidence: 0.7, Tokens: 400}, r)
	_, err = f.client().Choose(ctx, ChoiceInput{Instructions: "Which?", Options: map[string]string{"a": "first"}})
	require.ErrorContains(t, err, "incomplete answer", "a choice outside the options")

	s := setup(t)
	cl := &Classifier{Store: s, Model: f.client(), DailyTokens: 400, Log: slog.New(slog.DiscardHandler)}
	_, err = cl.Choose(ctx, in)
	require.NoError(t, err)
	n, err := s.TokensUsed(ctx, day(time.Now()))
	require.NoError(t, err)
	require.EqualValues(t, 400, n, "the budget is shared with classification")
	_, err = cl.Choose(ctx, in)
	require.ErrorIs(t, err, ErrBudget)
	require.EqualValues(t, 3, f.requests.Load())

	cl.DailyTokens = 1000
	ok, err := cl.UnderBudgetLeaving(ctx, 50)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = cl.UnderBudgetLeaving(ctx, 70)
	require.NoError(t, err)
	require.False(t, ok, "400 spent reaches 1000 less a 70% reserve")
}

func TestClientPermanentError(t *testing.T) {
	f := newFake(t)
	f.status["huge"] = http.StatusBadRequest
	_, err := f.client().Classify(context.Background(), Input{Name: "huge"})
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	require.True(t, apiErr.Permanent())
	require.EqualValues(t, 1, f.requests.Load())
}

func TestClientWrongKey(t *testing.T) {
	f := newFake(t)
	c := f.client()
	c.Key = "wrong"
	_, err := c.Classify(context.Background(), Input{Name: "x"})
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	require.False(t, apiErr.Permanent())
}

func TestClientNoKey(t *testing.T) {
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Values("Authorization")
		_, _ = w.Write([]byte(answer200))
	}))
	t.Cleanup(srv.Close)
	c := New("")
	c.URL = srv.URL
	_, err := c.Classify(context.Background(), Input{Name: "Heat.1995.1080p.BluRay.REMUX"})
	require.NoError(t, err)
	require.Empty(t, auth, "no key, no Authorization header")
}

func setup(t *testing.T, names ...string) *store.Store {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{Service: "sniffarr"})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	s, err := store.New(ctx, db, store.Options{Languages: audio.Tags(russian)})
	require.NoError(t, err)
	for i, n := range names {
		_, _, err := s.Insert(ctx, store.Torrent{Hash: store.Hash{byte(i + 1)}, Name: n, Size: 1 << 30, Files: []rules.File{{Path: n + ".mkv", Size: 1 << 30}},
			Discovery: "sample", SeenAt: time.Now(), Audio: []store.Audio{{Language: "ru", ScriptHint: true}}})
		require.NoError(t, err)
	}
	return s
}

func worker(s *store.Store, c Model) *Worker {
	cl := &Classifier{Store: s, Model: c, Log: slog.New(slog.DiscardHandler)}
	return &Worker{Store: s, Classifier: cl, Concurrency: 2, Poll: 10 * time.Millisecond, Log: slog.New(slog.DiscardHandler)}
}

func TestWorker(t *testing.T) {
	f := newFake(t)
	f.status["Broken"] = http.StatusUnprocessableEntity
	s := setup(t, "Heat.1995", "Ronin.1998", "Broken", "Collateral.2004")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- worker(s, f.client()).Run(ctx) }()

	require.Eventually(t, func() bool {
		p, err := s.Unclassified(ctx, 0, 10, 1)
		return err == nil && len(p) == 0
	}, 5*time.Second, 10*time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	r, err := s.Get(context.Background(), store.Hash{2})
	require.NoError(t, err)
	require.Equal(t, "movie", r.Kind)
	require.Equal(t, "remux", r.Source)
	require.Len(t, r.Audio, 1)
	require.InDelta(t, 0.75, *r.Audio[0].Probability, 1e-9)
	r, err = s.Get(context.Background(), store.Hash{3})
	require.NoError(t, err)
	require.Empty(t, r.Kind, "a refused torrent stays without a kind")
	n, err := s.TokensUsed(context.Background(), day(time.Now()))
	require.NoError(t, err)
	require.EqualValues(t, 3000, n)
}

func TestWorkerBudget(t *testing.T) {
	f := newFake(t)
	s := setup(t, "Heat.1995", "Ronin.1998", "Collateral.2004")
	w := worker(s, f.client())
	w.Concurrency = 1
	w.Classifier.DailyTokens = 1500
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- w.Run(ctx) }()

	require.Eventually(t, func() bool {
		p, err := s.Unclassified(ctx, 0, 10, 1)
		return err == nil && len(p) == 1
	}, 5*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.EqualValues(t, 2, f.requests.Load(), "the budget stops requests once spent")
}

func TestWorkerReserve(t *testing.T) {
	f := newFake(t)
	s := setup(t, "Heat.1995", "Ronin.1998", "Collateral.2004", "Thief.1981")
	w := worker(s, f.client())
	w.Concurrency = 1
	w.Classifier.DailyTokens = 3000
	w.Reserve = 50
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- w.Run(ctx) }()

	require.Eventually(t, func() bool {
		p, err := s.Unclassified(ctx, 0, 10, 1)
		return err == nil && len(p) == 2
	}, 5*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.EqualValues(t, 2, f.requests.Load(), "the Worker stops at the reserve")

	p, err := s.Unclassified(context.Background(), 0, 10, 1)
	require.NoError(t, err)
	require.NoError(t, w.Classifier.Classify(context.Background(), p[0], store.TriggerSearch), "Search spends the reserve")
	require.ErrorIs(t, w.Classifier.Classify(context.Background(), p[1], store.TriggerSearch), ErrBudget)
	require.EqualValues(t, 3, f.requests.Load())
}

func TestClassifierAsksKindOnlyWhenUnknown(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	s := setup(t, "Heat.1995")
	_, _, err := s.Insert(ctx, store.Torrent{Hash: store.Hash{9}, Name: "Ronin.S01E01", Size: 1 << 30, Discovery: "sample", SeenAt: time.Now(),
		Kind: "series", KindLayer: store.LayerRules})
	require.NoError(t, err)
	cl := &Classifier{Store: s, Model: f.client(), Log: slog.New(slog.DiscardHandler)}

	p, err := s.Pending(ctx, []int64{1, 2}, MaxFiles)
	require.NoError(t, err)
	for _, p := range p {
		require.NoError(t, cl.Classify(ctx, p, store.TriggerWorker))
	}
	require.EqualValues(t, 2, f.requests.Load())
	require.EqualValues(t, 1, f.kindAsked.Load(), "only Heat's kind was unknown")

	r, err := s.Get(ctx, store.Hash{9})
	require.NoError(t, err)
	require.Equal(t, "series", r.Kind, "the rules' kind stays")
	require.Equal(t, store.LayerRules, r.KindLayer)
	require.Equal(t, "remux", r.Source)
	require.True(t, r.Classified)
	r, err = s.Get(ctx, store.Hash{1})
	require.NoError(t, err)
	require.Equal(t, "movie", r.Kind)
	require.Equal(t, store.LayerClassifier, r.KindLayer)

	cl.DailyTokens = 1
	require.ErrorIs(t, cl.Classify(ctx, store.Pending{ID: 1, Name: "Heat.1995"}, store.TriggerWorker), ErrBudget)
	require.EqualValues(t, 2, f.requests.Load(), "no request once the budget is spent")
}

func TestClientTrace(t *testing.T) {
	f := newFake(t)
	telemetrytest.RequireClientChild(t, func(ctx context.Context) {
		_, err := f.client().Classify(ctx, Input{Name: "Heat.1995.1080p.BluRay.REMUX", Size: 30 << 30, AskKind: true})
		require.NoError(t, err)
	})
}
