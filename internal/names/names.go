// Package names reads what a torrent is from its name alone: a series, a
// movie, or neither, a title key under which releases of the same title
// meet, and the release's year, season, episode and quality.
package names

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Kinds Parse can tell.
const (
	Series = "series"
	Movie  = "movie"
)

// Qualities Parse can tell, by the resolution the name gives; "" is none.
const (
	Quality2160p = "2160p"
	Quality1080p = "1080p"
	Quality720p  = "720p"
	QualitySD    = "sd"
)

// Qualities are the qualities Parse can tell, highest first.
var Qualities = []string{Quality2160p, Quality1080p, Quality720p, QualitySD}

// Parsed is what a name says.
type Parsed struct {
	// Kind is Series, Movie, or "" when the name does not say for sure.
	Kind string
	// Key is the title key: "s:" and the series title for a series, "y:" and
	// the title and year for a titled release with a year, "" when the name
	// yields no title.
	Key string
	// Year is the year in the name, 0 when there is none.
	Year int
	// Season is the season number in the name, 0 when there is none.
	Season int
	// Episode is the episode number in the name; 0 when there is none, or
	// when the name gives a range or a whole season.
	Episode int
	// Quality is one of Qualities, or "" when the name gives no resolution.
	Quality string
}

// words matches any of the alternatives as a whole word, case-insensitively,
// with every non-letter, non-digit as a separator.
func words(alternatives string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(?:` + alternatives + `)(?:$|[^\p{L}\p{N}])`)
}

var (
	seriesMarker = words(`s\d{1,2}\s?e\d{1,3}|s\d{1,2}|\d{1,2}x\d{2,3}|e\d{1,3}\s?of\s?\d{1,3}|seasons?\s?\d{1,2}|complete series|` +
		`temporada\s?\d{1,2}|temporadas|staffel\s?\d{1,2}|cap\s?\d{3,4}|` +
		`сезоны?|\d{1,2}\s?сезон|серии|серия|выпуски?`)
	anime   = regexp.MustCompile(`^\[[^\]]+\]\s*(.+?)\s+-\s+\d{2,4}(?:v\d)?(?:\s|$|[\[(])`)
	year    = regexp.MustCompile(`(?:^|[^\p{L}\p{N}])((?:19[2-9]|20[0-4])\d)(?:$|[^\p{L}\p{N}])`)
	quality = words(`2160p|1080p|1080i|720p|576p|480p|4k|uhd|bdrip|brrip|bluray|blu-ray|bdremux|remux|bd25|bd50|` +
		`web-?dl|webrip|hdrip|dvdrip|dvd5|dvd9|hdtv|hdtvrip|x264|x265|h\.?264|h\.?265|hevc|xvid|camrip|telesync|hdcam`)
	music  = words(`flac|mp3|lossless|discography|дискография|альбом|album|ost`)
	groups = regexp.MustCompile(`^(?:\s*\[[^\]]*\])+`)
	season = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(?:s(\d{1,2})\s?e\d{1,3}|s(\d{1,2})|(\d{1,2})x\d{2,3}|seasons?\s?(\d{1,2})|` +
		`temporada\s?(\d{1,2})|staffel\s?(\d{1,2})|(\d{1,2})\s?сезон|сезоны?:?\s?(\d{1,2}))(?:$|[^\p{L}\p{N}])`)
	episode = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(?:s\d{1,2}\s?e(\d{1,3})|\d{1,2}x(\d{2,3})|e(\d{2,3})|(?:серия|episode|ep)\s?:?\s?(\d{1,3})|(\d{1,3})\s?серия)`)
	// episodeRange follows an episode number that starts a range or a
	// count, "1-8" or "1 of 8".
	episodeRange = regexp.MustCompile(`(?i)^\s*(?:-\s*\d|(?:of|из)\b)`)
	// qualities are matched in order; the first to match is the quality.
	qualities = []struct {
		re      *regexp.Regexp
		quality string
	}{
		{words(`2160[pр]|4k|uhd`), Quality2160p},
		{words(`1080[pрi]`), Quality1080p},
		{words(`720[pр]`), Quality720p},
		{words(`576[pр]|480[pр]|sd|dvdrip|dvd-rip|dvd5|dvd9|dvdscr|vhsrip|tvrip|satrip|camrip|telesync`), QualitySD},
	}
)

// Parse reads the kind, the title key, the year, the season, the episode and
// the quality from a torrent's name.
func Parse(name string) Parsed {
	clean := strings.NewReplacer(".", " ", "_", " ").Replace(name)
	var p Parsed
	switch {
	case anime.MatchString(name):
		p = series(key("s:", cut(anime.FindStringSubmatch(name)[1], seriesMarker)), clean)
	case seriesMarker.MatchString(clean):
		p = series(key("s:", cut(mainTitle(clean), seriesMarker)), clean)
	default:
		p = movie(mainTitle(clean), clean)
	}
	p.Quality = qualityOf(clean)
	return p
}

// movie returns the Parsed of a titled release with a year, or nothing.
func movie(title, clean string) Parsed {
	m := year.FindStringSubmatchIndex(clean)
	if m == nil {
		return Parsed{}
	}
	y := clean[m[2]:m[3]]
	p := Parsed{Key: key("y:", cut(cut(title, year), quality)+" "+y)}
	if p.Key == "y:"+y {
		return Parsed{}
	}
	p.Year, _ = strconv.Atoi(y)
	if quality.MatchString(clean) && !music.MatchString(clean) {
		p.Kind = Movie
	}
	return p
}

// series returns the Parsed of a series with the title key, with the season,
// episode and year the cleaned name gives.
func series(k, clean string) Parsed {
	p := Parsed{Kind: Series, Key: k}
	if k == "" {
		return p
	}
	if m := season.FindStringSubmatch(clean); m != nil {
		for _, g := range m[1:] {
			if g != "" {
				p.Season, _ = strconv.Atoi(g)
				break
			}
		}
	}
	p.Episode = episodeOf(clean)
	if m := year.FindStringSubmatch(clean); m != nil {
		p.Year, _ = strconv.Atoi(m[1])
	}
	return p
}

// episodeOf returns the episode number the cleaned name gives, 0 when it
// gives none or a range.
func episodeOf(clean string) int {
	m := episode.FindStringSubmatchIndex(clean)
	if m == nil {
		return 0
	}
	for g := 1; 2*g+1 < len(m); g++ {
		if m[2*g] < 0 {
			continue
		}
		if episodeRange.MatchString(clean[m[2*g+1]:]) {
			return 0
		}
		n, _ := strconv.Atoi(clean[m[2*g]:m[2*g+1]])
		return n
	}
	return 0
}

// qualityOf returns the highest quality the cleaned name gives, "" for none.
func qualityOf(clean string) string {
	for _, q := range qualities {
		if q.re.MatchString(clean) {
			return q.quality
		}
	}
	return ""
}

// Titles returns the titles a name gives, in name order: each title of a
// "Russian / Original / ..." name, without the season, year, quality and
// bracketed parts that follow it.
func Titles(name string) []string {
	if m := anime.FindStringSubmatch(name); m != nil {
		return titles([]string{cut(m[1], seriesMarker)})
	}
	clean := groups.ReplaceAllString(strings.NewReplacer(".", " ", "_", " ").Replace(name), "")
	var parts []string
	for _, part := range strings.Split(clean, " / ") {
		if i := strings.IndexAny(part, "[("); i >= 0 {
			part = part[:i]
		}
		parts = append(parts, cut(cut(cut(part, seriesMarker), year), quality))
	}
	return titles(parts)
}

// titles trims the parts and keeps those with a letter or digit.
func titles(parts []string) []string {
	var out []string
	for _, p := range parts {
		p = strings.TrimFunc(p, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// mainTitle returns the title part of a name: without leading [group]
// tags, and in a "Russian / Original / ..." name the first title only.
func mainTitle(name string) string {
	name = groups.ReplaceAllString(name, "")
	if i := strings.Index(name, " / "); i >= 0 {
		name = name[:i]
	}
	if i := strings.IndexAny(name, "[("); i > 0 {
		name = name[:i]
	}
	return name
}

// cut returns s up to the first match of re.
func cut(s string, re *regexp.Regexp) string {
	if loc := re.FindStringIndex(s); loc != nil {
		return s[:loc[0]]
	}
	return s
}

// key lowercases title and reduces it to words joined by single spaces,
// behind prefix; it returns "" for a title with no words.
func key(prefix, title string) string {
	f := strings.FieldsFunc(strings.ToLower(title), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	if len(f) == 0 {
		return ""
	}
	return prefix + strings.Join(f, " ")
}
