package classify

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"sarak.sh/mikluko/libarr/database"
	"sarak.sh/mikluko/sniffarr/internal/audio"
	"sarak.sh/mikluko/sniffarr/internal/rules"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

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
	require.EqualValues(t, 1, f.readings.Load(), "only Heat's kind was unknown")

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

func TestClassifierStoresReading(t *testing.T) {
	ctx := context.Background()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	f := newFake(t)
	f.prefix, f.title = "[rutor.info]", "Heat"
	s := setup(t, "[rutor.info] Heat.1995", "Heat.1995")
	client := f.client()
	client.Meter = mp
	cl := &Classifier{Store: s, Model: client, Log: slog.New(slog.DiscardHandler), Meter: mp}
	p, err := s.Pending(ctx, []int64{1, 2}, MaxFiles)
	require.NoError(t, err)
	for _, p := range p {
		require.NoError(t, cl.Classify(ctx, p, store.TriggerWorker))
	}

	c, err := s.Card(ctx, store.Hash{1}, 0, time.Now())
	require.NoError(t, err)
	require.Equal(t, "movie", c.Kind)
	require.Equal(t, store.LayerClassifier, c.KindLayer)
	require.InDelta(t, 0.85, c.KindConfidence, 1e-9)
	require.Equal(t, &store.Reading{PrefixEnd: 13, TitlePos: 13, TitleEnd: 17, Title: "Heat", TitleConfidence: 0.8}, c.Reading)
	c, err = s.Card(ctx, store.Hash{2}, 0, time.Now())
	require.NoError(t, err)
	require.Equal(t, &store.Reading{TitleEnd: 4}, c.Reading, "without a prefix the spans, not the title")

	n, err := s.TokensUsed(ctx, day(time.Now()))
	require.NoError(t, err)
	require.EqualValues(t, 50+5+900+100+900+100, n, "both directions of every request count")
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))
	got := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, x := range sm.Metrics {
			if x.Name != "sniffarr.classify.tokens" && x.Name != "sniffarr.typesafe.requests" {
				continue
			}
			for _, p := range x.Data.(metricdata.Sum[int64]).DataPoints {
				for _, kv := range p.Attributes.ToSlice() {
					got[x.Name+" "+kv.Value.String()] += p.Value
				}
			}
		}
	}
	require.Equal(t, map[string]int64{
		"sniffarr.classify.tokens input":  1850,
		"sniffarr.classify.tokens output": 205,
		"sniffarr.typesafe.requests 200":  3,
	}, got)
}
