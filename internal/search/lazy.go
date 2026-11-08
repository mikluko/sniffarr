package search

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/sync/singleflight"

	"sarak.sh/mikluko/sniffarr/internal/store"
)

const (
	DefaultLazyMax         = 20
	DefaultLazyTMDB        = 5
	DefaultLazyConcurrency = 8
	DefaultLazyWait        = 2 * time.Second
	DefaultLazyTimeout     = time.Minute
)

// Classifier examines one stored torrent and records what it finds, with
// the trigger, store.TriggerSearch or store.TriggerTorznab, that asked.
type Classifier interface {
	Classify(ctx context.Context, p store.Pending, trigger string) error
}

// TMDB matches title keys to works, recording the trigger that asked.
type TMDB interface {
	Unmatched(ctx context.Context, keys []string, limit int) ([]store.TitleKey, error)
	Match(ctx context.Context, k store.TitleKey, trigger string) error
}

// Lazy has Search classify the candidates the classifier has not examined,
// and match their title keys, before it answers. Zero fields take the
// default named in their doc.
type Lazy struct {
	// Classifier, when set, classifies candidates.
	Classifier Classifier
	// Max is the most candidates one Search classifies; DefaultLazyMax.
	Max int
	// TMDB, when set, matches the title keys of candidates.
	TMDB TMDB
	// MaxTMDB is the most title keys one Search matches;
	// DefaultLazyTMDB.
	MaxTMDB int
	// Concurrency is how many classifications and matches run at once;
	// DefaultLazyConcurrency.
	Concurrency int
	// Wait is how long Search waits for them; DefaultLazyWait. Those still
	// running go on, and their answers are recorded for later searches.
	Wait time.Duration
	// Timeout bounds one classification or match, waited for or not;
	// DefaultLazyTimeout.
	Timeout time.Duration
	// MaxFiles is how many of a torrent's files the classifier is shown.
	MaxFiles int
	Log      *slog.Logger
	// Meter records metrics; the global MeterProvider when nil.
	Meter metric.MeterProvider

	once          sync.Once
	classifying   singleflight.Group
	duration      metric.Float64Histogram
	outcomes      metric.Int64Counter
	titleOutcomes metric.Int64Counter
}

func (l *Lazy) init() {
	l.once.Do(func() {
		mp := l.Meter
		if mp == nil {
			mp = otel.GetMeterProvider()
		}
		m := mp.Meter("sarak.sh/mikluko/sniffarr/internal/search")
		var err error
		l.duration, err = m.Float64Histogram("sniffarr.search.lazy.duration", metric.WithUnit("s"),
			metric.WithDescription("Time a search spent classifying its candidates and matching their titles."),
			metric.WithExplicitBucketBoundaries(0.05, 0.1, 0.25, 0.5, 1, 1.5, 2, 3, 5))
		if err != nil {
			otel.Handle(err)
		}
		l.outcomes, err = m.Int64Counter("sniffarr.search.lazy.classifications", metric.WithUnit("{torrent}"),
			metric.WithDescription("Candidates a search had classified, by whether the answer came in time, late, or for a search that did not wait."))
		if err != nil {
			otel.Handle(err)
		}
		l.titleOutcomes, err = m.Int64Counter("sniffarr.search.lazy.titles", metric.WithUnit("{title}"),
			metric.WithDescription("Title keys a search had matched, by whether the match came in time, late, or for a search that did not wait."))
		if err != nil {
			otel.Handle(err)
		}
		if l.Max <= 0 {
			l.Max = DefaultLazyMax
		}
		if l.MaxTMDB <= 0 {
			l.MaxTMDB = DefaultLazyTMDB
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
// to Max candidates the classifier has not examined, and matching up to
// MaxTMDB title keys of candidates due for a match; the filters on kind
// and audio apply to what is known once it is done. Without wait it
// answers from what the store holds and leaves the classifications and
// matches to run in the background, past the end of ctx.
func (l *Lazy) search(ctx context.Context, s *store.Store, q store.Query, wait bool) ([]store.Release, error) {
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
	if !wait {
		go l.examine(context.WithoutCancel(ctx), s, slices.Clone(found), false)
		return known(found, q, limit), nil
	}
	classified, matched := l.examine(ctx, s, found, true)
	for i, r := range found {
		if classified[r.ID] || matched[r.TitleKey] {
			if found[i], err = s.Get(ctx, r.Hash); err != nil {
				return nil, err
			}
		}
	}
	return known(found, q, limit), nil
}

// known returns, in place, up to limit of found that pass the filters of q.
func known(found []store.Release, q store.Query, limit int) []store.Release {
	out := found[:0]
	for _, r := range found {
		if matches(r, q) && len(out) < limit {
			out = append(out, r)
		}
	}
	return out
}

// examine classifies the candidates and matches their title keys, and
// returns the IDs classified and the keys matched within Wait. Without
// wait it returns once the jobs are started, with nothing classified or
// matched in time.
func (l *Lazy) examine(ctx context.Context, s *store.Store, found []store.Release, wait bool) (map[int64]bool, map[string]bool) {
	var (
		jobs       []func(context.Context)
		mu         sync.Mutex
		expired    = make(chan struct{})
		classified = map[int64]bool{}
		matched    = map[string]bool{}
	)
	late, trigger := result("late"), store.TriggerSearch
	if !wait {
		close(expired)
		late, trigger = result("background"), store.TriggerTorznab
	}
	var ids []int64
	var keys []string
	for _, r := range found {
		if l.Classifier != nil && !r.Classified && len(ids) < l.Max {
			ids = append(ids, r.ID)
		}
		if l.TMDB != nil && r.TitleKey != "" && (r.Kind == "movie" || r.Kind == "series") && !slices.Contains(keys, r.TitleKey) {
			keys = append(keys, r.TitleKey)
		}
	}
	if len(ids) > 0 {
		pending, err := s.Pending(ctx, ids, l.MaxFiles)
		if err != nil {
			l.Log.ErrorContext(ctx, "pending", "err", err)
		}
		for _, p := range pending {
			jobs = append(jobs, l.job(func(ctx context.Context) error { return l.classify(ctx, s, p, trigger) },
				l.outcomes, late, &mu, expired, func() { classified[p.ID] = true }))
		}
	}
	if len(keys) > 0 {
		due, err := l.TMDB.Unmatched(ctx, keys, l.MaxTMDB)
		if err != nil {
			l.Log.ErrorContext(ctx, "unmatched titles", "err", err)
		}
		for _, k := range due {
			jobs = append(jobs, l.job(func(ctx context.Context) error { return l.TMDB.Match(ctx, k, trigger) },
				l.titleOutcomes, late, &mu, expired, func() { matched[k.Key] = true }))
		}
	}
	if len(jobs) == 0 {
		return nil, nil
	}
	if !wait {
		l.start(ctx, jobs)
		return nil, nil
	}
	start := time.Now()
	l.run(ctx, jobs, expired)
	l.duration.Record(ctx, time.Since(start).Seconds())
	mu.Lock()
	defer mu.Unlock()
	return maps.Clone(classified), maps.Clone(matched)
}

// classify classifies p, joining a classification of p already running for
// another search. A classification recorded since p was read, by a job that
// ran to the end before this one started, is not repeated.
func (l *Lazy) classify(ctx context.Context, s *store.Store, p store.Pending, trigger string) error {
	_, err, _ := l.classifying.Do(strconv.FormatInt(p.ID, 10), func() (any, error) {
		pending, err := s.Pending(ctx, []int64{p.ID}, l.MaxFiles)
		if err != nil {
			return nil, err
		}
		if len(pending) == 0 {
			return nil, nil
		}
		return nil, l.Classifier.Classify(ctx, pending[0], trigger)
	})
	return err
}

// job runs f under its own Timeout, counts the outcome, and, when f succeeds
// before expired closes, calls done under mu; a success after it counts as
// late.
func (l *Lazy) job(f func(context.Context) error, outcomes metric.Int64Counter, late metric.AddOption, mu *sync.Mutex, expired <-chan struct{}, done func()) func(context.Context) {
	return func(ctx context.Context) {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), l.Timeout)
		defer cancel()
		if err := f(cctx); err != nil {
			outcomes.Add(ctx, 1, result("failed"))
			return
		}
		mu.Lock()
		defer mu.Unlock()
		select {
		case <-expired:
			outcomes.Add(ctx, 1, late)
		default:
			outcomes.Add(ctx, 1, result("in_time"))
			done()
		}
	}
}

// run starts the jobs and returns after Wait, once ctx is done, or once
// they all are, whichever is first, having closed expired. The jobs still
// running go on.
func (l *Lazy) run(ctx context.Context, jobs []func(context.Context), expired chan struct{}) {
	all := l.start(ctx, jobs)
	timer := time.NewTimer(l.Wait)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	case <-all:
	}
	close(expired)
}

// start starts the jobs, Concurrency at once, and returns a channel closed
// once they are all done.
func (l *Lazy) start(ctx context.Context, jobs []func(context.Context)) <-chan struct{} {
	var wg sync.WaitGroup
	sem := make(chan struct{}, l.Concurrency)
	for _, j := range jobs {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			j(ctx)
		})
	}
	all := make(chan struct{})
	go func() {
		wg.Wait()
		close(all)
	}()
	return all
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
