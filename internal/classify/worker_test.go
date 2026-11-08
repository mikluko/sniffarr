package classify

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"sarak.sh/mikluko/sniffarr/internal/store"
)

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
