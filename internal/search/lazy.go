package search

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/mikluko/sniffarr/internal/store"
)

const (
	DefaultLazyMax         = 20
	DefaultLazyConcurrency = 8
	DefaultLazyWait        = 2 * time.Second
	DefaultLazyTimeout     = time.Minute
)

// Classifier examines one stored torrent and records what it finds.
type Classifier interface {
	Classify(ctx context.Context, p store.Pending) error
}

// Lazy has Search classify the candidates the classifier has not examined
// before it answers. Zero fields take the default named in their doc.
type Lazy struct {
	Classifier Classifier
	// Max is the most candidates one Search classifies; DefaultLazyMax.
	Max int
	// Concurrency is how many it classifies at once; DefaultLazyConcurrency.
	Concurrency int
	// Wait is how long Search waits for them; DefaultLazyWait. Those still
	// running go on, and their answers are recorded for later searches.
	Wait time.Duration
	// Timeout bounds one classification, waited for or not;
	// DefaultLazyTimeout.
	Timeout time.Duration
	// MaxFiles is how many of a torrent's files the classifier is shown.
	MaxFiles int
	Log      *slog.Logger
	// Meter records metrics; the global MeterProvider when nil.
	Meter metric.MeterProvider

	once     sync.Once
	duration metric.Float64Histogram
	outcomes metric.Int64Counter
}

func (l *Lazy) init() {
	l.once.Do(func() {
		mp := l.Meter
		if mp == nil {
			mp = otel.GetMeterProvider()
		}
		m := mp.Meter("github.com/mikluko/sniffarr/internal/search")
		var err error
		l.duration, err = m.Float64Histogram("sniffarr.search.lazy.duration", metric.WithUnit("s"),
			metric.WithDescription("Time a search spent classifying its candidates."),
			metric.WithExplicitBucketBoundaries(0.05, 0.1, 0.25, 0.5, 1, 1.5, 2, 3, 5))
		if err != nil {
			otel.Handle(err)
		}
		l.outcomes, err = m.Int64Counter("sniffarr.search.lazy.classifications", metric.WithUnit("{torrent}"),
			metric.WithDescription("Candidates a search had classified, by whether the answer came in time."))
		if err != nil {
			otel.Handle(err)
		}
		if l.Max <= 0 {
			l.Max = DefaultLazyMax
		}
		if l.Concurrency <= 0 {
			l.Concurrency = DefaultLazyConcurrency
		}
		if l.Wait <= 0 {
			l.Wait = DefaultLazyWait
		}
		if l.Timeout <= 0 {
			l.Timeout = DefaultLazyTimeout
		}
	})
}

// search returns the releases matching q after classifying, within Wait, up
// to Max candidates the classifier has not examined; the filters on kind and
// audio apply to what is known once it is done.
func (l *Lazy) search(ctx context.Context, s *store.Store, q store.Query) ([]store.Release, error) {
	l.init()
	limit := q.Limit
	if len(q.Kinds) > 0 || q.MinAudioProbability != nil {
		q.Limit = min(3*limit, 3*MaxLimit)
	}
	q.Unexamined = true
	found, err := s.Search(ctx, q)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, r := range found {
		if !r.Classified && len(ids) < l.Max {
			ids = append(ids, r.ID)
		}
	}
	if done := l.classify(ctx, s, ids); len(done) > 0 {
		for i, r := range found {
			if done[r.ID] {
				if found[i], err = s.Get(ctx, r.Hash); err != nil {
					return nil, err
				}
			}
		}
	}
	out := found[:0]
	for _, r := range found {
		if matches(r, q) && len(out) < limit {
			out = append(out, r)
		}
	}
	return out, nil
}

// classify classifies the torrents with the IDs and returns those classified
// within Wait.
func (l *Lazy) classify(ctx context.Context, s *store.Store, ids []int64) map[int64]bool {
	if len(ids) == 0 {
		return nil
	}
	start := time.Now()
	pending, err := s.Pending(ctx, ids, l.MaxFiles)
	if err != nil {
		l.Log.ErrorContext(ctx, "pending", "err", err)
		return nil
	}
	var (
		mu   sync.Mutex
		done = map[int64]bool{}
		wg   sync.WaitGroup
		sem  = make(chan struct{}, l.Concurrency)
	)
	deadline := time.After(l.Wait)
	expired := make(chan struct{})
	all := make(chan struct{})
	go func() {
		select {
		case <-deadline:
		case <-ctx.Done():
		case <-all:
		}
		close(expired)
	}()
	for _, p := range pending {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), l.Timeout)
			defer cancel()
			if err := l.Classifier.Classify(cctx, p); err != nil {
				l.outcomes.Add(ctx, 1, result("failed"))
				return
			}
			select {
			case <-expired:
				l.outcomes.Add(ctx, 1, result("late"))
			default:
				l.outcomes.Add(ctx, 1, result("in_time"))
				mu.Lock()
				done[p.ID] = true
				mu.Unlock()
			}
		})
	}
	go func() {
		wg.Wait()
		close(all)
	}()
	<-expired
	l.duration.Record(ctx, time.Since(start).Seconds())
	mu.Lock()
	defer mu.Unlock()
	return maps.Clone(done)
}

func result(r string) metric.AddOption {
	return metric.WithAttributes(attribute.String("sniffarr.search.lazy.result", r))
}

// matches reports whether r passes the kind and audio filters of q,
// which leave out what is still unknown.
func matches(r store.Release, q store.Query) bool {
	if slices.Contains(hidden, r.Kind) {
		return false
	}
	if len(q.Kinds) > 0 && !slices.Contains(q.Kinds, r.Kind) {
		return false
	}
	if q.MinAudioProbability != nil {
		i := slices.IndexFunc(r.Audio, func(a store.Audio) bool { return a.Language == q.AudioLanguage })
		if i < 0 || r.Audio[i].Probability == nil || *r.Audio[i].Probability < *q.MinAudioProbability {
			return false
		}
	}
	return true
}
