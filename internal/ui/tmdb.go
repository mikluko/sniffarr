package ui

import (
	"net/url"
	"strconv"
	"strings"
)

// TMDBImageBase is the secure_base_url from TMDB /configuration.
var TMDBImageBase = "https://image.tmdb.org/t/p/"

// TMDB image sizes used by the templates (all in TMDB's poster_sizes).
const (
	PosterThumb = "w92"  // top rows and search hits, shown at 28–40 px
	PosterCard  = "w342" // card, shown at 150 px
)

// LowConfidence marks matches the operator should look at.
const LowConfidence = 0.7

// StateOf returns the match state, treating nil as not checked.
func StateOf(t *TMDBWork) string {
	if t == nil || t.State == "" {
		return TMDBUnchecked
	}
	return t.State
}

func (t *TMDBWork) Matched() bool   { return t != nil && t.State == TMDBMatched }
func (t *TMDBWork) HasPoster() bool { return t.Matched() && t.PosterPath != "" }

// ShowOriginal reports whether the original title differs from the localized one.
func (t *TMDBWork) ShowOriginal() bool {
	return t.Matched() && t.OriginalTitle != "" &&
		!strings.EqualFold(strings.TrimSpace(t.OriginalTitle), strings.TrimSpace(t.Title))
}

// URL is the TMDB page of the match.
func (t *TMDBWork) URL() string {
	if !t.Matched() {
		return ""
	}
	typ := t.Type
	if typ != "tv" {
		typ = "movie"
	}
	return "https://www.themoviedb.org/" + typ + "/" + strconv.FormatInt(t.ID, 10)
}

// IMDbURL is the IMDb title page, "" when the id is unknown.
func (t *TMDBWork) IMDbURL() string {
	if !t.Matched() || t.IMDbID == "" {
		return ""
	}
	return "https://www.imdb.com/title/" + url.PathEscape(t.IMDbID) + "/"
}

// TMDBImg builds an image URL; "" when there is no path.
func TMDBImg(size, path string) string {
	if path == "" {
		return ""
	}
	return TMDBImageBase + size + path
}

// TMDBSearchURL and IMDbSearchURL are plain searches for unmatched titles.
func TMDBSearchURL(q string) string {
	return "https://www.themoviedb.org/search?query=" + url.QueryEscape(q)
}
func IMDbSearchURL(q string) string { return "https://www.imdb.com/find/?q=" + url.QueryEscape(q) }

// Runtime formats minutes: "2 ч 46 мин" / "2 h 46 min".
func (b Base) Runtime(min int) string {
	if min <= 0 {
		return ""
	}
	h, m := min/60, min%60
	hs, ms := b.T("unit.h"), b.T("unit.min")
	switch {
	case h == 0:
		return strconv.Itoa(m) + " " + ms
	case m == 0:
		return strconv.Itoa(h) + " " + hs
	}
	return strconv.Itoa(h) + " " + hs + " " + strconv.Itoa(m) + " " + ms
}

// Rating formats a TMDB score with one decimal: "7,9".
func (b Base) Rating(r float64) string { return b.dec(r, 1) }

// List joins strings with ", ".
func (b Base) List(xs []string) string { return strings.Join(xs, ", ") }

// Title is the display name of a title key: the TMDB title when matched, else the key.
func (b Base) Title(ref WorkRef, t *TMDBWork) string {
	if t.Matched() && t.Title != "" {
		return t.Title
	}
	return ref.Title
}

// TitleSub is what follows the title: the TMDB year for movies, the season for series,
// the key's sub otherwise.
func (b Base) TitleSub(ref WorkRef, t *TMDBWork) string {
	if t.Matched() && ref.Kind == "movie" && t.Year > 0 {
		return strconv.Itoa(t.Year)
	}
	return ref.Sub
}
