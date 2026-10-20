package classify

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/mikluko/sniffarr/internal/store"
)

// MaxFiles is how many of a torrent's files the classifier is shown.
const MaxFiles = 100

// DefaultSearchReserve is the default Worker.Reserve.
const DefaultSearchReserve = 25

// Worker classifies in the background the stored torrents Store.Unclassified
// returns: those of unknown kind whose names hint at Russian audio.
type Worker struct {
	Store       Store
	Jev         *Jev
	Concurrency int
	// Reserve is the percentage of Jev.DailyTokens the Worker leaves for
	// Search to spend.
	Reserve int
	// Poll is how long the Worker waits when nothing is left to classify or
	// after a failed request.
	Poll time.Duration
	Log  *slog.Logger
}

// Run classifies until ctx is done. A torrent that fails other than by being
// refused is retried later.
func (w *Worker) Run(ctx context.Context) error {
	var after int64
	for {
		if err := w.waitBudget(ctx); err != nil {
			return err
		}
		batch, err := w.Store.Unclassified(ctx, after, 4*w.Concurrency, MaxFiles)
		if err != nil && ctx.Err() == nil {
			w.Log.Error("list unclassified", "err", err)
		}
		if len(batch) == 0 || err != nil {
			after = 0
			if err := sleep(ctx, w.Poll); err != nil {
				return err
			}
			continue
		}
		after = batch[len(batch)-1].ID
		if w.classifyAll(ctx, batch) {
			if err := sleep(ctx, w.Poll); err != nil {
				return err
			}
		}
	}
}

// classifyAll classifies the batch and reports whether any request failed.
func (w *Worker) classifyAll(ctx context.Context, batch []store.Pending) bool {
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		failed bool
	)
	sem := make(chan struct{}, w.Concurrency)
	for _, p := range batch {
		select {
		case <-ctx.Done():
		case sem <- struct{}{}:
		}
		if ok, err := w.underBudget(ctx); ctx.Err() != nil || err != nil || !ok {
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			err := w.Jev.Classify(ctx, p)
			var apiErr *APIError
			if err == nil || ctx.Err() != nil || errors.Is(err, ErrBudget) || errors.As(err, &apiErr) && apiErr.Permanent() {
				return
			}
			w.Log.Warn("classify", "id", p.ID, "err", err)
			mu.Lock()
			failed = true
			mu.Unlock()
		})
	}
	wg.Wait()
	return failed
}

// underBudget reports whether today's budget has room outside the Reserve.
func (w *Worker) underBudget(ctx context.Context) (bool, error) {
	if w.Jev.DailyTokens <= 0 {
		return true, nil
	}
	return w.Jev.under(ctx, int64(float64(w.Jev.DailyTokens)*float64(100-w.Reserve)/100))
}

// waitBudget returns once the day's budget has room outside the Reserve,
// sleeping until the next UTC midnight when it has none.
func (w *Worker) waitBudget(ctx context.Context) error {
	ok, err := w.underBudget(ctx)
	if err != nil && ctx.Err() == nil {
		w.Log.Error("read token usage", "err", err)
		return sleep(ctx, w.Poll)
	}
	if ok {
		return ctx.Err()
	}
	t := w.Jev.now()
	midnight := t.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
	w.Log.Info("background share of the daily token budget spent", "until", midnight)
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
