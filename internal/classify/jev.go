package classify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/sync/singleflight"

	"github.com/mikluko/sniffarr/internal/store"
)

// DefaultDailyTokens is the default daily token budget: at about 1,300
// tokens a torrent, some 1,500 torrents a day.
const DefaultDailyTokens = 2000000

// ErrBudget is returned by Jev.Classify once the day's token budget is spent.
var ErrBudget = errors.New("daily token budget spent")

// Store is the part of the store classification uses.
type Store interface {
	Unclassified(ctx context.Context, after int64, limit, maxFiles int) ([]store.Pending, error)
	Classify(ctx context.Context, id int64, c store.Classification, at time.Time) (int, error)
	ClassifyFailed(ctx context.Context, id int64, reason string, at time.Time) error
	TokensUsed(ctx context.Context, day string) (int64, error)
	AddTokens(ctx context.Context, day string, n int64) error
}

// Classifier answers what a torrent is.
type Classifier interface {
	Classify(ctx context.Context, in Input) (Result, error)
}

// Jev classifies stored torrents with the Classifier, within a daily token
// budget, and records the answers in the Store. It is safe for concurrent
// use; concurrent calls for one torrent share a single request.
type Jev struct {
	Store      Store
	Classifier Classifier
	// DailyTokens caps the classifier tokens spent per UTC day; 0 is no cap.
	// Requests in flight when the cap is reached may overshoot it.
	DailyTokens int64
	Log         *slog.Logger
	Now         func() time.Time
	// Meter records metrics; the global MeterProvider when nil.
	Meter metric.MeterProvider

	group       singleflight.Group
	metricsOnce sync.Once
	m           *metrics
}

type metrics struct {
	classifications, tokens, errors metric.Int64Counter
}

func (j *Jev) metrics() *metrics {
	j.metricsOnce.Do(func() {
		mp := j.Meter
		if mp == nil {
			mp = otel.GetMeterProvider()
		}
		m := mp.Meter("github.com/mikluko/sniffarr/internal/classify")
		counter := func(name, unit, desc string) metric.Int64Counter {
			c, err := m.Int64Counter(name, metric.WithUnit(unit), metric.WithDescription(desc))
			if err != nil {
				otel.Handle(err)
			}
			return c
		}
		j.m = &metrics{
			classifications: counter("sniffarr.classify.classifications", "{torrent}",
				"Torrents whose kind was decided, by kind and by the layer that decided it."),
			tokens: counter("sniffarr.classify.tokens", "{token}", "Classifier tokens spent."),
			errors: counter("sniffarr.classify.errors", "{error}", "Failed classifier requests, by whether a retry can succeed."),
		}
	})
	return j.m
}

func (j *Jev) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

func layerAttrs(layer, kind string) metric.AddOption {
	return metric.WithAttributes(attribute.String("sniffarr.classify.layer", layer), attribute.String("sniffarr.kind", kind))
}

func errorKind(k string) metric.AddOption {
	return metric.WithAttributes(attribute.String("sniffarr.classify.error", k))
}

// Classify asks the classifier about the torrent, for its kind too when the
// kind is unknown, and records the answer. It returns ErrBudget, without a
// request, once the day's budget is spent. A torrent the classifier refuses
// as malformed is marked failed and never asked about again.
func (j *Jev) Classify(ctx context.Context, p store.Pending) error {
	_, err, _ := j.group.Do(strconv.FormatInt(p.ID, 10), func() (any, error) {
		return nil, j.classify(ctx, p)
	})
	return err
}

func (j *Jev) classify(ctx context.Context, p store.Pending) error {
	if ok, err := j.UnderBudget(ctx); err != nil || !ok {
		return errors.Join(err, ErrBudget)
	}
	r, err := j.Classifier.Classify(ctx, Input{Name: p.Name, Size: p.Size, Files: p.Files, AskKind: !p.KindKnown})
	var apiErr *APIError
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.As(err, &apiErr) && apiErr.Permanent():
		j.metrics().errors.Add(ctx, 1, errorKind("permanent"))
		j.Log.Warn("classifier refused torrent", "id", p.ID, "err", err)
		if err := j.Store.ClassifyFailed(ctx, p.ID, err.Error(), j.now()); err != nil {
			return fmt.Errorf("mark classify failed: %w", err)
		}
		return err
	case err != nil:
		j.metrics().errors.Add(ctx, 1, errorKind("transient"))
		return err
	}
	j.metrics().tokens.Add(ctx, r.Tokens)
	if err := j.Store.AddTokens(ctx, day(j.now()), r.Tokens); err != nil {
		j.Log.Error("record tokens", "err", err)
	}
	copied, err := j.Store.Classify(ctx, p.ID, store.Classification{
		Kind:             r.Kind,
		KindConfidence:   r.KindConfidence,
		Source:           r.Source,
		SourceConfidence: r.SourceConfidence,
		RussianAudio:     r.RussianAudio,
	}, j.now())
	if err != nil {
		return fmt.Errorf("store classification: %w", err)
	}
	if r.Kind != "" {
		j.metrics().classifications.Add(ctx, 1, layerAttrs(store.LayerJev, r.Kind))
		j.metrics().classifications.Add(ctx, int64(copied), layerAttrs(store.LayerReuse, r.Kind))
	}
	return nil
}

// UnderBudget reports whether today's budget has room.
func (j *Jev) UnderBudget(ctx context.Context) (bool, error) {
	if j.DailyTokens <= 0 {
		return true, nil
	}
	return j.under(ctx, j.DailyTokens)
}

// under reports whether fewer than limit tokens were spent today.
func (j *Jev) under(ctx context.Context, limit int64) (bool, error) {
	used, err := j.Store.TokensUsed(ctx, day(j.now()))
	return used < limit, err
}

func day(t time.Time) string {
	return t.UTC().Format(time.DateOnly)
}
