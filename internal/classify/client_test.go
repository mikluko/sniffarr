package classify

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"sarak.sh/mikluko/libarr/names"
	"sarak.sh/mikluko/libarr/names/jev"
	"sarak.sh/mikluko/libarr/telemetry/telemetrytest"
	"sarak.sh/mikluko/libarr/typesafe"
	"sarak.sh/mikluko/sniffarr/internal/audio"
	"sarak.sh/mikluko/sniffarr/internal/rules"
)

var russian, _ = audio.Parse("ru")

func TestQuestions(t *testing.T) {
	q := questions(true, russian)
	require.ElementsMatch(t, []string{"category", "source", "audio_ru"}, slices.Collect(maps.Keys(q)))
	require.Equal(t, typesafe.Question{
		Type:         typesafe.TypeNoul,
		Instructions: "Does this release carry a Russian-language audio track (dubbing, voice-over, or Russian original), judging by its `name` and `files`?",
		Criteria: map[string]any{
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
	require.ElementsMatch(t, []string{"source"}, slices.Collect(maps.Keys(questions(false, nil))), "no kind, no audio language: the source alone")
}

// fakeEndpoint is a TypeSafe System One endpoint answering the classify
// questions, Jev's reading steps and choice questions.
type fakeEndpoint struct {
	*httptest.Server
	requests, readings atomic.Int64
	// status is answered for a request whose name contains the key.
	status map[string]int
	// overload is how many 529 answers come before the first 200.
	overload atomic.Int64
	// prefix, title and kind are Jev's picks, each taken when it is an
	// option.
	prefix, title, kind string
}

type fakeQuestion struct {
	Type     string         `json:"type"`
	Criteria map[string]any `json:"criteria"`
}

// pick is the option if q offers it, else "none".
func (q fakeQuestion) pick(option string) string {
	if _, ok := q.Criteria[option]; ok {
		return option
	}
	return "none"
}

func newFake(t *testing.T) *fakeEndpoint {
	f := &fakeEndpoint{status: map[string]int{}, kind: names.Movie}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct {
			Model     string                  `json:"model"`
			State     map[string]any          `json:"state"`
			Questions map[string]fakeQuestion `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		name, _ := req.State["name"].(string)
		for k, code := range f.status {
			if strings.Contains(name, k) {
				http.Error(w, `{"error":"max_tokens_exceeded"}`, code)
				return
			}
		}
		if f.overload.Add(-1) >= 0 {
			http.Error(w, "overloaded", 529)
			return
		}
		answers := map[string]any{}
		usage := map[string]int{"input_tokens": 900, "output_tokens": 100}
		_, reading := req.Questions["title"]
		switch {
		case req.Model != "jev-latest":
			http.Error(w, "bad model", http.StatusUnprocessableEntity)
			return
		case len(req.Questions) == 1 && req.Questions["prefix"].Type == "choice":
			answers["prefix"] = map[string]any{"type": "choice", "choice": req.Questions["prefix"].pick(f.prefix), "confidence": 0.9}
			usage = map[string]int{"input_tokens": 50, "output_tokens": 5}
		case len(req.Questions) == 1 && req.Questions["choice"].Type == "choice":
			answers["choice"] = map[string]any{"type": "choice", "choice": "b", "confidence": 0.7}
			usage = map[string]int{"input_tokens": 400, "output_tokens": 0}
		case req.Questions["source"].Type != "choice" || req.Questions["audio_ru"].Type != "noul" || reading != (req.Questions["category"].Type == "choice"):
			http.Error(w, "bad questions", http.StatusUnprocessableEntity)
			return
		default:
			if reading {
				f.readings.Add(1)
				answers["title"] = map[string]any{"type": "choice", "choice": req.Questions["title"].pick(f.title), "confidence": 0.8}
				answers["year"] = map[string]any{"type": "choice", "choice": "none", "confidence": 0.5}
				answers["kind"] = map[string]any{"type": "choice", "choice": f.kind, "confidence": 0.85}
				answers["category"] = map[string]any{"type": "choice", "choice": "music", "confidence": 0.5}
			}
			answers["audio_ru"] = map[string]any{"type": "noul", "noul": 0.75}
			answers["source"] = map[string]any{"type": "choice", "choice": "remux", "confidence": 0.6}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "answers": answers, "usage": usage})
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

func TestClientClassifyReads(t *testing.T) {
	f := newFake(t)
	f.prefix, f.title = "[rutor.info]", "Heat"
	f.overload.Store(2)
	name := "[rutor.info] Heat.1995.1080p.BluRay.REMUX"
	r, err := f.client().Classify(context.Background(), Input{Name: name, Size: 30 << 30, Files: []rules.File{{Path: "Heat.mkv", Size: 30 << 30}}, AskKind: true})
	require.NoError(t, err)
	require.NotNil(t, r.Reading)
	require.Equal(t, jev.Span{Pos: 0, End: 13}, r.Reading.Prefix)
	require.Equal(t, "Heat", r.Reading.Title)
	require.Equal(t, "Heat", name[r.Reading.TitleSpan.Pos:r.Reading.TitleSpan.End])
	r.Reading = nil
	require.Equal(t, Result{Kind: "movie", KindConfidence: 0.85, Source: "remux", SourceConfidence: 0.6, Audio: map[string]float64{"ru": 0.75},
		Usage: typesafe.Usage{Input: 950, Output: 105}}, r)
	require.EqualValues(t, 4, f.requests.Load(), "the reader's two requests, two 529 answers retried")
	require.EqualValues(t, 1, f.readings.Load(), "source and audio ride the reading's second request")
}

func TestClientClassifyKnownKind(t *testing.T) {
	f := newFake(t)
	r, err := f.client().Classify(context.Background(), Input{Name: "[rutor.info] Heat.1995.1080p.BluRay.REMUX"})
	require.NoError(t, err)
	require.Equal(t, Result{Source: "remux", SourceConfidence: 0.6, Audio: map[string]float64{"ru": 0.75}, Usage: typesafe.Usage{Input: 900, Output: 100}}, r)
	require.EqualValues(t, 1, f.requests.Load())
	require.Zero(t, f.readings.Load(), "a known kind is not read")
}

func TestKind(t *testing.T) {
	music := map[string]typesafe.Answer{"category": {Type: typesafe.TypeChoice, Choice: "music", Confidence: 0.5}}
	tests := []struct {
		name     string
		reading  jev.Reading
		kind     string
		confides float64
	}{
		{"movie", jev.Reading{Kind: names.Movie, KindConfidence: 0.9, Extra: music}, "movie", 0.9},
		{"series", jev.Reading{Kind: names.Series, KindConfidence: 0.7, Extra: music}, "series", 0.7},
		{"neither, by category", jev.Reading{KindConfidence: 0.8, Extra: music}, "music", 0.4},
		{"neither, no category", jev.Reading{KindConfidence: 0.8}, "", 0},
		{"neither, a category not offered", jev.Reading{Extra: map[string]typesafe.Answer{"category": {Choice: "movie"}}}, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, c := kind(tt.reading)
			require.Equal(t, tt.kind, k)
			require.InDelta(t, tt.confides, c, 1e-9)
		})
	}
}

func TestChoose(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	in := ChoiceInput{State: map[string]any{"name": "Alien 1979"}, Instructions: "Which?", Options: map[string]string{"a": "first", "b": "second"}}
	r, err := f.client().Choose(ctx, in)
	require.NoError(t, err)
	require.Equal(t, ChoiceResult{Choice: "b", Confidence: 0.7, Usage: typesafe.Usage{Input: 400}}, r)
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
	_, err := f.client().Classify(context.Background(), Input{Name: "huge", AskKind: true})
	var tErr *typesafe.Error
	require.ErrorAs(t, err, &tErr)
	require.True(t, tErr.Permanent())
	require.EqualValues(t, 1, f.requests.Load(), "a name with no prefix candidate costs one request")
}

func TestClientWrongKey(t *testing.T) {
	f := newFake(t)
	c := f.client()
	c.Key = "wrong"
	_, err := c.Classify(context.Background(), Input{Name: "x"})
	var tErr *typesafe.Error
	require.ErrorAs(t, err, &tErr)
	require.False(t, tErr.Permanent())
}

func TestClientNoKey(t *testing.T) {
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Values("Authorization")
		_, _ = w.Write([]byte(`{"answers": {"source": {"type": "choice", "choice": "web", "confidence": 0.9}}}`))
	}))
	t.Cleanup(srv.Close)
	c := New("")
	c.URL = srv.URL
	_, err := c.Classify(context.Background(), Input{Name: "Heat.1995.1080p.WEB-DL"})
	require.NoError(t, err)
	require.Empty(t, auth, "no key, no Authorization header")
}

func TestClientTrace(t *testing.T) {
	f := newFake(t)
	telemetrytest.RequireClientChild(t, func(ctx context.Context) {
		_, err := f.client().Classify(ctx, Input{Name: "Heat.1995.1080p.BluRay.REMUX", Size: 30 << 30})
		require.NoError(t, err)
	})
}
