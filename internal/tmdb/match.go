// Package tmdb matches title keys to works on TMDB, so that every release
// of a movie or series can be found by the work's IDs.
package tmdb

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

const scope = "github.com/mikluko/sniffarr/internal/tmdb"

// DefaultRetry is how long a key no work was found for waits before it is
// looked up again.
const DefaultRetry = 7 * 24 * time.Hour

// maxChoices is the most candidates the Chooser is shown.
const maxChoices = 8

// none is the Chooser's option for no candidate.
const none = "none"

// Store is the part of the store matching uses.
type Store interface {
	UnmatchedTitles(ctx context.Context, keys []string, before time.Time, limit int) ([]store.TitleKey, error)
	RecordTitle(ctx context.Context, key string, t store.Title, at time.Time) error
	WithoutDetails(ctx context.Context, limit int) ([]store.Matched, error)
	RecordDetails(ctx context.Context, key string, d store.Details, ids []store.ExternalID, at time.Time) error
}

// languages are the languages details are stored in, each with the tag TMDB
// is asked for it by; the first one's details are the work's.
var languages = []struct{ lang, tag string }{{"en", "en-US"}, {"ru", "ru-RU"}}

// Chooser answers a choice question; classify.Jev is one.
type Chooser interface {
	Choose(ctx context.Context, in classify.ChoiceInput) (classify.ChoiceResult, error)
}

// Matcher matches title keys to works and records the matches. It is safe
// for concurrent use; concurrent calls for one key share a single match.
type Matcher struct {
	Client *Client
	Store  Store
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
// It returns classify.ErrBudget when the Chooser's budget is spent. A match
// recorded, it fetches the work's details as Details does; when that fails
// the match stays without them for Worker's backfill.
func (m *Matcher) Match(ctx context.Context, k store.TitleKey) error {
	_, err, _ := m.group.Do(k.Key, func() (any, error) {
		return nil, m.match(ctx, k)
	})
	return err
}

func (m *Matcher) match(ctx context.Context, k store.TitleKey) error {
	t, id, err := m.find(ctx, k)
	var tErr *Error
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
	if id == 0 {
		return nil
	}
	err = m.Details(ctx, store.Matched{Key: k.Key, Kind: k.Kind, ID: id})
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case err != nil:
		m.Log.Warn("fetch details", "key", k.Key, "err", err)
	}
	return nil
}

// Details fetches the details of the matched work in every language and
// records them, with the work's IDs with other providers. A request TMDB
// refuses for good records empty details, so the work is not asked about
// again.
func (m *Matcher) Details(ctx context.Context, k store.Matched) error {
	d, ids, err := m.details(ctx, k.Kind == names.Series, k.ID)
	var tErr *Error
	switch {
	case errors.As(err, &tErr) && tErr.Permanent():
		m.Log.Warn("tmdb refuses the work", "key", k.Key, "err", err)
		d, ids = store.Details{}, nil
	case err != nil:
		return err
	}
	if err := m.Store.RecordDetails(ctx, k.Key, d, ids, m.now()); err != nil {
		return fmt.Errorf("record details: %w", err)
	}
	return nil
}

// details fetches the work in every language: the details and IDs of the
// first language, and the texts of each with the poster of that language.
func (m *Matcher) details(ctx context.Context, series bool, id int) (store.Details, []store.ExternalID, error) {
	d := store.Details{Type: "movie", Texts: map[string]store.Text{}}
	if series {
		d.Type = "tv"
	}
	ids := []store.ExternalID{{Provider: store.ProviderTMDB, ID: strconv.Itoa(id)}}
	for i, l := range languages {
		r, err := m.Client.Details(ctx, series, id, l.tag)
		if err != nil {
			return store.Details{}, nil, err
		}
		switch {
		case i == 0:
			d.OriginalTitle, d.PosterPath, d.Rating, d.Votes, d.Runtime = r.OriginalTitle, r.PosterPath, r.Rating, r.Votes, r.Runtime
			d.Status, d.Seasons, d.Countries, d.Language = r.Status, r.Seasons, r.Countries, r.Language
			for _, p := range []string{store.ProviderIMDb, store.ProviderTVDB, store.ProviderWikidata} {
				if id := r.IDs[p]; id != "" {
					ids = append(ids, store.ExternalID{Provider: p, ID: id})
				}
			}
		case d.PosterPath == "":
			d.PosterPath = r.PosterPath
		}
		d.Texts[l.lang] = store.Text{Title: r.Title, Overview: r.Overview, Genres: r.Genres, PosterPath: r.PosterPath}
	}
	return d, ids, nil
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

// find returns the match of the key and the TMDB ID of the work, with an
// empty Layer and 0 when there is none.
func (m *Matcher) find(ctx context.Context, k store.TitleKey) (store.Title, int, error) {
	w := workOf(k)
	found, err := m.search(ctx, w)
	if err != nil {
		return store.Title{}, 0, err
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
		return store.Title{}, 0, nil
	case len(fits) == 1:
		pick = fits[0]
		pick.layer = store.LayerTMDB
	case dominant(fits) >= 0:
		pick = fits[dominant(fits)]
		pick.confidence, pick.layer = dominantConfidence, store.LayerTMDB
	case m.Chooser == nil:
		return store.Title{}, 0, nil
	default:
		var ok bool
		if pick, ok, err = m.choose(ctx, k, fits); err != nil || !ok {
			return store.Title{}, 0, err
		}
	}
	t := store.Title{Year: pick.c.Year, Confidence: pick.confidence, Layer: pick.layer,
		IDs: []store.ExternalID{{Provider: store.ProviderTMDB, ID: strconv.Itoa(pick.c.ID)}}}
	return t, pick.c.ID, nil
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
			found, err := m.Client.Search(ctx, w.series, title, w.year, lang)
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

// A fit is dominant when its popularity is at least dominantRatio times
// every other fit's and at least dominantFloor; it is taken without the
// Chooser, at dominantConfidence.
const (
	dominantRatio      = 10
	dominantFloor      = 20
	dominantConfidence = 0.8
)

// dominant returns the index of the dominant fit, or -1 when none is.
func dominant(fits []fit) int {
	best, second := -1, 0.0
	for i, f := range fits {
		switch {
		case best < 0 || f.c.Popularity > fits[best].c.Popularity:
			if best >= 0 {
				second = fits[best].c.Popularity
			}
			best = i
		case f.c.Popularity > second:
			second = f.c.Popularity
		}
	}
	if best < 0 || fits[best].c.Popularity < dominantFloor || fits[best].c.Popularity < dominantRatio*second {
		return -1
	}
	return best
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
	var facts []string
	if c.Language != "" {
		facts = append(facts, "language "+c.Language)
	}
	if len(c.Countries) > 0 {
		facts = append(facts, "from "+strings.Join(c.Countries, ", "))
	}
	if c.Popularity > 0 {
		facts = append(facts, "popularity "+strconv.FormatFloat(c.Popularity, 'f', 1, 64))
	}
	if c.Votes > 0 {
		facts = append(facts, strconv.Itoa(c.Votes)+" votes")
	}
	if len(facts) > 0 {
		s += " [" + strings.Join(facts, "; ") + "]"
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
		m.matches, err = mp.Meter(scope).Int64Counter("sniffarr.tmdb.matches", metric.WithUnit("{title}"),
			metric.WithDescription("Title keys looked up, by the layer that matched them, or none."))
		if err != nil {
			otel.Handle(err)
		}
	})
	m.matches.Add(ctx, 1, metric.WithAttributes(attribute.String("sniffarr.tmdb.layer", layer)))
}
