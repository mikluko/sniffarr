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

// WorkID is the URL id of a title: "m318846" for a movie, "tv1399" for a series.
// TMDB numbers movies and series separately, so the bare TMDB id is ambiguous.
func WorkID(kind string, tmdbID int64) string {
	if kind == "series" {
		return "tv" + strconv.FormatInt(tmdbID, 10)
	}
	return "m" + strconv.FormatInt(tmdbID, 10)
}

// ParseWorkID reverses WorkID; ok=false for anything else.
func ParseWorkID(id string) (kind string, tmdbID int64, ok bool) {
	num := ""
	switch {
	case strings.HasPrefix(id, "tv"):
		kind, num = "series", id[2:]
	case strings.HasPrefix(id, "m"):
		kind, num = "movie", id[1:]
	default:
		return "", 0, false
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return kind, n, true
}

// WorkPath is the title page of a summary.
func (w WorkSummary) Path() string { return "/w/" + w.ID }

// Facts is the one-line description under a title: "фильм · 2015 · 2 ч 10 мин",
// "сериал · 2011 · завершён · 8 сез.".
func (b Base) Facts(kind string, t *TMDBWork) string {
	parts := []string{b.T("kind." + kind)}
	if t.Matched() {
		if t.Year > 0 {
			parts = append(parts, strconv.Itoa(t.Year))
		}
		if kind == "series" {
			if t.Status != "" {
				parts = append(parts, b.T("tvstatus."+t.Status))
			}
			if t.Seasons > 0 {
				parts = append(parts, strconv.Itoa(t.Seasons)+"\u00a0"+b.T("tmdb.seasons"))
			}
		} else if r := b.Runtime(t.Runtime); r != "" {
			parts = append(parts, r)
		}
	}
	return strings.Join(parts, " · ")
}
