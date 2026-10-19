package classify

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/mikluko/sniffarr/internal/store"
)

// Store is the part of the store the Worker uses.
type Store interface {
	Unclassified(ctx context.Context, after int64, limit, maxFiles int) ([]store.Pending, error)
	Classify(ctx context.Context, id int64, c store.Classification, at time.Time) error
	ClassifyFailed(ctx context.Context, id int64, reason string, at time.Time) error
	TokensUsed(ctx context.Context, day string) (int64, error)
	AddTokens(ctx context.Context, day string, n int64) error
}

// Classifier answers what a torrent is.
type Classifier interface {
	Classify(ctx context.Context, in Input) (Result, error)
}

// MaxFiles is how many of a torrent's files the classifier is shown.
const MaxFiles = 100

// Worker classifies stored torrents as they arrive.
type Worker struct {
	Store       Store
	Classifier  Classifier
	Concurrency int
	// DailyTokens caps the classifier tokens spent per UTC day; 0 is no cap.
	// Requests in flight when the cap is reached may overshoot it.
	DailyTokens int64
	// Poll is how long the Worker waits when nothing is left to classify or
	// after a failed request.
	Poll time.Duration
	Log  *slog.Logger
	Now  func() time.Time
}

// Run classifies until ctx is done. A torrent the classifier rejects as
// malformed is marked failed; one that fails otherwise is retried later.
func (w *Worker) Run(ctx context.Context) error {
	now := w.Now
	if now == nil {
		now = time.Now
	}
	var after int64
	for {
		if err := w.waitBudget(ctx, now()); err != nil {
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
		if w.classifyAll(ctx, batch, now) {
			if err := sleep(ctx, w.Poll); err != nil {
				return err
			}
		}
	}
}

// classifyAll classifies the batch and reports whether any request failed.
func (w *Worker) classifyAll(ctx context.Context, batch []store.Pending, now func() time.Time) bool {
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
		if ok, err := w.underBudget(ctx, now()); ctx.Err() != nil || err != nil || !ok {
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			if !w.classify(ctx, p, now) {
				mu.Lock()
				failed = true
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return failed
}

func (w *Worker) classify(ctx context.Context, p store.Pending, now func() time.Time) bool {
	r, err := w.Classifier.Classify(ctx, Input{Name: p.Name, Size: p.Size, Files: p.Files})
	var apiErr *APIError
	switch {
	case ctx.Err() != nil:
		return true
	case errors.As(err, &apiErr) && apiErr.Permanent():
		w.Log.Warn("classifier refused torrent", "id", p.ID, "err", err)
		if err := w.Store.ClassifyFailed(ctx, p.ID, err.Error(), now()); err != nil {
			w.Log.Error("mark classify failed", "id", p.ID, "err", err)
		}
		return true
	case err != nil:
		w.Log.Warn("classify", "id", p.ID, "err", err)
		return false
	}
	if err := w.Store.AddTokens(ctx, day(now()), r.Tokens); err != nil {
		w.Log.Error("record tokens", "err", err)
	}
	err = w.Store.Classify(ctx, p.ID, store.Classification{
		Kind:             r.Kind,
		KindConfidence:   r.KindConfidence,
		Source:           r.Source,
		SourceConfidence: r.SourceConfidence,
		RussianAudio:     r.RussianAudio,
	}, now())
	if err != nil {
		w.Log.Error("store classification", "id", p.ID, "err", err)
		return false
	}
	return true
}

func (w *Worker) underBudget(ctx context.Context, t time.Time) (bool, error) {
	if w.DailyTokens <= 0 {
		return true, nil
	}
	used, err := w.Store.TokensUsed(ctx, day(t))
	return used < w.DailyTokens, err
}

// waitBudget returns once the day's budget has room, sleeping until the next
// UTC midnight when it has none.
func (w *Worker) waitBudget(ctx context.Context, t time.Time) error {
	ok, err := w.underBudget(ctx, t)
	if err != nil && ctx.Err() == nil {
		w.Log.Error("read token usage", "err", err)
		return sleep(ctx, w.Poll)
	}
	if ok {
		return ctx.Err()
	}
	midnight := t.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
	w.Log.Info("daily token budget spent", "until", midnight)
	return sleep(ctx, midnight.Sub(t))
}

func day(t time.Time) string {
	return t.UTC().Format(time.DateOnly)
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
