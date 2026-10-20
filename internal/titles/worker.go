package titles

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/mikluko/sniffarr/internal/classify"
	"github.com/mikluko/sniffarr/internal/store"
)

// DefaultConcurrency is the default Worker.Concurrency.
const DefaultConcurrency = 2

// Budget is the classifier budget the Chooser spends; classify.Jev is one.
type Budget interface {
	UnderBudgetLeaving(ctx context.Context, reserve int) (bool, error)
}

// Worker matches in the background the title keys Matcher.Unmatched
// returns, newest first.
type Worker struct {
	Matcher     *Matcher
	Concurrency int
	// Poll is how long the Worker waits when nothing is due or after a
	// failed match.
	Poll time.Duration
	// Budget, when set, holds the Worker back while its spending leaves no
	// room outside Reserve percent.
	Budget  Budget
	Reserve int
	Log     *slog.Logger
}

// Run matches until ctx is done.
func (w *Worker) Run(ctx context.Context) error {
	for {
		if err := w.waitBudget(ctx); err != nil {
			return err
		}
		keys, err := w.Matcher.Unmatched(ctx, nil, 4*max(w.Concurrency, 1))
		if err != nil && ctx.Err() == nil {
			w.Log.Error("list unmatched titles", "err", err)
		}
		if len(keys) == 0 || err != nil || w.matchAll(ctx, keys) {
			if err := sleep(ctx, w.Poll); err != nil {
				return err
			}
		}
	}
}

// matchAll matches the keys and reports whether any match failed.
func (w *Worker) matchAll(ctx context.Context, keys []store.TitleKey) bool {
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		failed bool
	)
	sem := make(chan struct{}, max(w.Concurrency, 1))
	for _, k := range keys {
		select {
		case <-ctx.Done():
		case sem <- struct{}{}:
		}
		if ctx.Err() != nil {
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			err := w.Matcher.Match(ctx, k)
			if err == nil || ctx.Err() != nil {
				return
			}
			if !errors.Is(err, classify.ErrBudget) {
				w.Log.Warn("match title", "key", k.Key, "err", err)
			}
			mu.Lock()
			failed = true
			mu.Unlock()
		})
	}
	wg.Wait()
	return failed
}

// waitBudget returns once the Budget has room, sleeping until the next UTC
// midnight when it has none.
func (w *Worker) waitBudget(ctx context.Context) error {
	if w.Budget == nil {
		return ctx.Err()
	}
	ok, err := w.Budget.UnderBudgetLeaving(ctx, w.Reserve)
	if err != nil && ctx.Err() == nil {
		w.Log.Error("read token usage", "err", err)
		return sleep(ctx, w.Poll)
	}
	if ok {
		return ctx.Err()
	}
	t := w.Matcher.now()
	midnight := t.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
	w.Log.Info("background share of the daily token budget spent; title matching waits", "until", midnight)
	return sleep(ctx, midnight.Sub(t))
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
