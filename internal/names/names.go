// Package names reads what a torrent is from its name alone: a series, a
// movie, or neither, and a title key under which releases of the same title
// meet.
package names

import (
	"regexp"
	"strings"
	"unicode"
)

// Kinds Parse can tell.
const (
	Series = "series"
	Movie  = "movie"
)

// Parsed is what a name says.
type Parsed struct {
	// Kind is Series, Movie, or "" when the name does not say for sure.
	Kind string
	// Key is the title key: "s:" and the series title for a series, "y:" and
	// the title and year for a titled release with a year, "" when the name
	// yields no title.
	Key string
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
)

// Parse reads the kind and the title key from a torrent's name.
func Parse(name string) Parsed {
	if m := anime.FindStringSubmatch(name); m != nil {
		return Parsed{Kind: Series, Key: key("s:", cut(m[1], seriesMarker))}
	}
	clean := strings.NewReplacer(".", " ", "_", " ").Replace(name)
	title := mainTitle(clean)
	if loc := seriesMarker.FindStringIndex(clean); loc != nil {
		return Parsed{Kind: Series, Key: key("s:", cut(title, seriesMarker))}
	}
	m := year.FindStringSubmatchIndex(clean)
	if m == nil {
		return Parsed{}
	}
	y := clean[m[2]:m[3]]
	p := Parsed{Key: key("y:", cut(cut(title, year), quality)+" "+y)}
	if p.Key == "y:"+y {
		return Parsed{}
	}
	if quality.MatchString(clean) && !music.MatchString(clean) {
		p.Kind = Movie
	}
	return p
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
