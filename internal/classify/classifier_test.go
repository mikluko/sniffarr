package classify

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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
