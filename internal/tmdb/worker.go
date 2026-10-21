package tmdb

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

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
// returns, newest first, and fetches the details of the works that lack
// them, those with releases first.
type Worker struct {
	Matcher     *Matcher
	Concurrency int
	// Poll is how long the Worker waits when nothing is due or after a
	// failed match or fetch.
	Poll time.Duration
	// Budget, when set, holds matching back while its spending leaves no
	// room outside Reserve percent; fetching details spends none.
	Budget  Budget
	Reserve int
	Log     *slog.Logger
}

// Run matches and fetches details until ctx is done.
func (w *Worker) Run(ctx context.Context) error {
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return w.match(ctx) })
	g.Go(func() error { return w.backfill(ctx) })
	return g.Wait()
}

// match matches the title keys due for a match until ctx is done.
func (w *Worker) match(ctx context.Context) error {
	for {
		if err := w.waitBudget(ctx); err != nil {
			return err
		}
		keys, err := w.Matcher.Unmatched(ctx, nil, w.batch())
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

// backfill fetches the details of the matches that lack them until ctx is
// done.
func (w *Worker) backfill(ctx context.Context) error {
	for {
		due, err := w.Matcher.Store.WithoutDetails(ctx, w.batch())
		if err != nil && ctx.Err() == nil {
			w.Log.Error("list matches without details", "err", err)
		}
		failed := run(ctx, w.Concurrency, due, w.Matcher.Details, func(d store.Due, err error) {
			w.Log.Warn("fetch details", "type", d.Type, "id", d.TMDBID, "err", err)
		})
		if len(due) == 0 || err != nil || failed {
			if err := sleep(ctx, w.Poll); err != nil {
				return err
			}
		}
	}
}

// batch is how many keys a round takes.
func (w *Worker) batch() int {
	return 4 * max(w.Concurrency, 1)
}

// matchAll matches the keys and reports whether any match failed.
func (w *Worker) matchAll(ctx context.Context, keys []store.TitleKey) bool {
	return run(ctx, w.Concurrency, keys, w.Matcher.Match, func(k store.TitleKey, err error) {
		if !errors.Is(err, classify.ErrBudget) {
			w.Log.Warn("match title", "key", k.Key, "err", err)
		}
	})
}

// run calls f on every item, at most concurrency at a time, tells fail of
// each call that failed, and reports whether any did. Failures after ctx is
// done are not failures.
func run[T any](ctx context.Context, concurrency int, items []T, f func(context.Context, T) error, fail func(T, error)) bool {
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		failed bool
	)
	sem := make(chan struct{}, max(concurrency, 1))
	for _, item := range items {
		select {
		case <-ctx.Done():
		case sem <- struct{}{}:
		}
		if ctx.Err() != nil {
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			err := f(ctx, item)
			if err == nil || ctx.Err() != nil {
				return
			}
			fail(item, err)
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
