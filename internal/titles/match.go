// Package titles matches title keys to works on TMDB, so that every release
// of a movie or series can be found by the work's IDs.
package titles

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/sync/singleflight"

	"github.com/mikluko/sniffarr/internal/classify"
	"github.com/mikluko/sniffarr/internal/names"
	"github.com/mikluko/sniffarr/internal/store"
)

const scope = "github.com/mikluko/sniffarr/internal/titles"

// DefaultRetry is how long a key no work was found for waits before it is
// looked up again.
const DefaultRetry = 30 * 24 * time.Hour

// maxChoices is the most candidates the Chooser is shown.
const maxChoices = 8

// none is the Chooser's option for no candidate.
const none = "none"

// Store is the part of the store matching uses.
type Store interface {
	UnmatchedTitles(ctx context.Context, keys []string, before time.Time, limit int) ([]store.TitleKey, error)
	RecordTitle(ctx context.Context, key string, t store.Title, at time.Time) error
}

// Chooser answers a choice question; classify.Jev is one.
type Chooser interface {
	Choose(ctx context.Context, in classify.ChoiceInput) (classify.ChoiceResult, error)
}

// Matcher matches title keys to works and records the matches. It is safe
// for concurrent use; concurrent calls for one key share a single match.
type Matcher struct {
	TMDB  *TMDB
	Store Store
	// Chooser picks among several plausible candidates; without one, a key
	// with several stays unmatched.
	Chooser Chooser
	// Retry is how long a key no work was found for waits before it is
	// looked up again; DefaultRetry.
	Retry time.Duration
	Log   *slog.Logger
	Now   func() time.Time
	// Meter records metrics; the global MeterProvider when nil.
	Meter metric.MeterProvider

	group   singleflight.Group
	once    sync.Once
	matches metric.Int64Counter
}

func (m *Matcher) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Unmatched returns, newest first, up to limit title keys due for a match,
// among keys unless it is nil.
func (m *Matcher) Unmatched(ctx context.Context, keys []string, limit int) ([]store.TitleKey, error) {
	retry := m.Retry
	if retry <= 0 {
		retry = DefaultRetry
	}
	return m.Store.UnmatchedTitles(ctx, keys, m.now().Add(-retry), limit)
}

// Match looks the key up on TMDB and records what it finds: the one
// plausible candidate, under layer tmdb; the Chooser's pick among several,
// under layer jev; or no match. It records nothing when it fails, except
// for a request TMDB refuses for good, which is recorded as a failed match.
// It returns classify.ErrBudget when the Chooser's budget is spent.
func (m *Matcher) Match(ctx context.Context, k store.TitleKey) error {
	_, err, _ := m.group.Do(k.Key, func() (any, error) {
		return nil, m.match(ctx, k)
	})
	return err
}

func (m *Matcher) match(ctx context.Context, k store.TitleKey) error {
	t, err := m.find(ctx, k)
	var tErr *TMDBError
	switch {
	case errors.As(err, &tErr) && tErr.Permanent():
		t = store.Title{Error: tErr.Error()}
	case err != nil:
		return err
	}
	if err := m.Store.RecordTitle(ctx, k.Key, t, m.now()); err != nil {
		return fmt.Errorf("record title: %w", err)
	}
	layer := t.Layer
	if layer == "" {
		layer = "none"
	}
	m.count(ctx, layer)
	return nil
}

// work is a title key's title, year and kind.
type work struct {
	series bool
	year   int
	titles []string
}

func workOf(k store.TitleKey) work {
	w := work{series: k.Kind == names.Series, titles: names.Titles(k.Name)}
	title, ok := strings.CutPrefix(k.Key, "y:")
	if ok {
		if i := strings.LastIndexByte(title, ' '); i > 0 {
			w.year, _ = strconv.Atoi(title[i+1:])
			title = title[:i]
		}
	} else {
		title = strings.TrimPrefix(k.Key, "s:")
	}
	if len(w.titles) == 0 {
		w.titles = []string{title}
	}
	return w
}

// find returns the match of the key, with an empty Layer when there is none.
func (m *Matcher) find(ctx context.Context, k store.TitleKey) (store.Title, error) {
	w := workOf(k)
	found, err := m.search(ctx, w)
	if err != nil {
		return store.Title{}, err
	}
	var fits []fit
	for _, c := range found {
		if f, ok := w.fit(c); ok {
			fits = append(fits, f)
		}
	}
	var pick fit
	switch {
	case len(fits) == 0:
		return store.Title{}, nil
	case len(fits) == 1:
		pick = fits[0]
		pick.layer = store.LayerTMDB
	case m.Chooser == nil:
		return store.Title{}, nil
	default:
		var ok bool
		if pick, ok, err = m.choose(ctx, k, fits); err != nil || !ok {
			return store.Title{}, err
		}
	}
	ids, err := m.TMDB.ExternalIDs(ctx, w.series, pick.c.ID)
	if err != nil {
		return store.Title{}, err
	}
	t := store.Title{Year: pick.c.Year, Confidence: pick.confidence, Layer: pick.layer,
		IDs: []store.ExternalID{{Provider: store.ProviderTMDB, ID: strconv.Itoa(pick.c.ID)}}}
	for _, p := range []string{store.ProviderIMDb, store.ProviderTVDB, store.ProviderWikidata} {
		if id := ids[p]; id != "" {
			t.IDs = append(t.IDs, store.ExternalID{Provider: p, ID: id})
		}
	}
	return t, nil
}

// candidate is a TMDB work with every title it was found under.
type candidate struct {
	Candidate
	titles []string
}

// search searches every title of the work in English, and in Russian too
// for a title with Cyrillic letters; it returns each work once, in the
// order found.
func (m *Matcher) search(ctx context.Context, w work) ([]candidate, error) {
	var out []candidate
	index := map[int]int{}
	for _, title := range w.titles {
		languages := []string{"en-US"}
		if strings.IndexFunc(title, func(r rune) bool { return unicode.Is(unicode.Cyrillic, r) }) >= 0 {
			languages = append(languages, "ru-RU")
		}
		for _, lang := range languages {
			found, err := m.TMDB.Search(ctx, w.series, title, w.year, lang)
			if err != nil {
				return nil, err
			}
			for _, c := range found {
				i, ok := index[c.ID]
				if !ok {
					i = len(out)
					index[c.ID] = i
					out = append(out, candidate{Candidate: c})
				}
				out[i].titles = append(out[i].titles, c.Title, c.OriginalTitle)
			}
		}
	}
	return out, nil
}

// fit is a plausible candidate, how sure the fit is, and what decided it.
type fit struct {
	c          candidate
	confidence float64
	layer      string
}

// fit reports whether c is plausibly the work: one of its titles, in any
// language it was found in, equals one of the work's in letters and digits
// regardless of case, and its year is the work's or one off. The confidence
// is 1 for the same year, 0.9 for a work without a year, and 0.8 for a year
// one off.
func (w work) fit(c candidate) (fit, bool) {
	same := false
	for _, a := range c.titles {
		for _, b := range w.titles {
			same = same || normal(a) != "" && normal(a) == normal(b)
		}
	}
	if !same {
		return fit{}, false
	}
	switch d := c.Year - w.year; {
	case w.year == 0:
		return fit{c: c, confidence: 0.9}, true
	case d == 0:
		return fit{c: c, confidence: 1}, true
	case d == 1 || d == -1:
		return fit{c: c, confidence: 0.8}, true
	}
	return fit{}, false
}

// normal reduces a title to its lowercase words joined by single spaces.
func normal(title string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(title), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }), " ")
}

// choose asks the Chooser which of the fits the key's torrent is a release
// of; it reports false when the answer is none of them.
func (m *Matcher) choose(ctx context.Context, k store.TitleKey, fits []fit) (fit, bool, error) {
	fits = fits[:min(len(fits), maxChoices)]
	options := map[string]string{none: "None of these works, or not enough to tell"}
	for i, f := range fits {
		options[strconv.Itoa(i+1)] = describe(f.c.Candidate)
	}
	kind := "movie"
	if k.Kind == names.Series {
		kind = "series"
	}
	r, err := m.Chooser.Choose(ctx, classify.ChoiceInput{
		State:        map[string]any{"name": k.Name, "kind": kind},
		Instructions: "Which work is this torrent a release of, judging by its `name`?",
		Options:      options,
	})
	if err != nil || r.Choice == none {
		return fit{}, false, err
	}
	i, err := strconv.Atoi(r.Choice)
	if err != nil || i < 1 || i > len(fits) {
		return fit{}, false, fmt.Errorf("choice %q", r.Choice)
	}
	f := fits[i-1]
	f.confidence, f.layer = r.Confidence, store.LayerJev
	return f, true, nil
}

// describe is the Chooser's description of a candidate: its titles, year,
// and the first sentence of its overview.
func describe(c Candidate) string {
	s := c.Title
	if c.OriginalTitle != "" && c.OriginalTitle != c.Title {
		s += " (" + c.OriginalTitle + ")"
	}
	if c.Year != 0 {
		s += ", " + strconv.Itoa(c.Year)
	}
	overview, _, _ := strings.Cut(c.Overview, ". ")
	if r := []rune(overview); len(r) > 200 {
		overview = string(r[:200])
	}
	if overview != "" {
		s += ": " + overview
	}
	return s
}

func (m *Matcher) count(ctx context.Context, layer string) {
	m.once.Do(func() {
		mp := m.Meter
		if mp == nil {
			mp = otel.GetMeterProvider()
		}
		var err error
		m.matches, err = mp.Meter(scope).Int64Counter("sniffarr.titles.matches", metric.WithUnit("{title}"),
			metric.WithDescription("Title keys looked up, by the layer that matched them, or none."))
		if err != nil {
			otel.Handle(err)
		}
	})
	m.matches.Add(ctx, 1, metric.WithAttributes(attribute.String("sniffarr.titles.layer", layer)))
}
