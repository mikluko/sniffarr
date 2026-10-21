// Package names reads what a torrent is from its name alone: a series, a
// movie, or neither, a title key under which releases of the same title
// meet, and the release's year, season, episode and quality.
package names

import (
	"regexp"
	"slices"
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
// with every non-letter, non-digit as a separator. Group 1 is the word.
func words(alternatives string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(` + alternatives + `)(?:$|[^\p{L}\p{N}])`)
}

var (
	seriesMarker = words(`s\d{1,2}\s?e\d{1,3}|s\d{1,2}|\d{1,2}x\d{2,3}|e\d{1,3}\s?of\s?\d{1,3}|seasons?\s?\d{1,2}|complete series|` +
		`temporada\s?\d{1,2}|temporadas|staffel\s?\d{1,2}|cap\s?\d{3,4}|` +
		`сезоны?|\d{1,2}\s?сезон|серии|серия|выпуски?`)
	// anime group 1 is the title, group 2 the episode part, "- 12".
	anime   = regexp.MustCompile(`^\[[^\]]+\]\s*(.+?)\s+(-\s+\d{2,4}(?:v\d)?)(?:\s|$|[\[(])`)
	year    = regexp.MustCompile(`(?:^|[^\p{L}\p{N}])((?:19[2-9]|20[0-4])\d)(?:$|[^\p{L}\p{N}])`)
	quality = words(`2160p|1080p|1080i|720p|576p|480p|4k|uhd|bdrip|brrip|bluray|blu-ray|bdremux|remux|bd25|bd50|` +
		`web-?dl|webrip|hdrip|dvdrip|dvd5|dvd9|hdtv|hdtvrip|x264|x265|h\.?264|h\.?265|hevc|xvid|camrip|telesync|hdcam`)
	music  = words(`flac|mp3|lossless|discography|дискография|альбом|album|ost`)
	groups = regexp.MustCompile(`^(?:\s*\[[^\]]*\])+`)
	// season group 1 is the marker, the groups after it the season number,
	// one per alternative.
	season = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(s(\d{1,2})\s?e\d{1,3}|s(\d{1,2})|(\d{1,2})x\d{2,3}|seasons?\s?(\d{1,2})|` +
		`temporada\s?(\d{1,2})|staffel\s?(\d{1,2})|(\d{1,2})\s?сезон|сезоны?:?\s?(\d{1,2}))(?:$|[^\p{L}\p{N}])`)
	// episode group 1 is the marker, the groups after it the episode
	// number, one per alternative.
	episode = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(s\d{1,2}\s?e(\d{1,3})|\d{1,2}x(\d{2,3})|e(\d{2,3})|(?:серия|episode|ep)\s?:?\s?(\d{1,3})|(\d{1,3})\s?серия)`)
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
	return parse(name, nil)
}

// Hit is a feature of a name and the fragment of the name that gave it.
type Hit struct {
	// Rule is "kind.series" or "kind.movie", "year", "season", "episode",
	// or "quality." and the quality, one of Qualities.
	Rule string
	// Match is the fragment as written in the name.
	Match string
	// Pos is the byte offset of Match in the name.
	Pos int
}

// Explain returns the features Parse recognises in a name, each with the
// fragment that gave it, in the order the fragments read; features at the
// same place keep the order kind, year, season, episode, quality. It has a
// hit for each of the non-empty Kind, Year, Season, Episode and Quality that
// Parse returns for the name.
func Explain(name string) []Hit {
	c := &collector{name: name}
	parse(name, c)
	slices.SortStableFunc(c.hits, func(a, b Hit) int { return a.Pos - b.Pos })
	return c.hits
}

// collector gathers hits on a name; a nil collector gathers none.
type collector struct {
	name string
	hits []Hit
}

// add records rule for name[from:to]. The cleaned name is the same length
// as the name, so offsets in either are offsets in both.
func (c *collector) add(rule string, from, to int) {
	if c != nil {
		c.hits = append(c.hits, Hit{Rule: rule, Match: c.name[from:to], Pos: from})
	}
}

func parse(name string, c *collector) Parsed {
	clean := strings.NewReplacer(".", " ", "_", " ").Replace(name)
	var p Parsed
	if m := anime.FindStringSubmatchIndex(name); m != nil {
		c.add("kind.series", m[4], m[5])
		p = series(key("s:", cut(name[m[2]:m[3]], seriesMarker)), clean, c)
	} else if m := seriesMarker.FindStringSubmatchIndex(clean); m != nil {
		c.add("kind.series", m[2], m[3])
		p = series(key("s:", cut(mainTitle(clean), seriesMarker)), clean, c)
	} else {
		p = movie(mainTitle(clean), clean, c)
	}
	p.Quality = qualityOf(clean, c)
	return p
}

// movie returns the Parsed of a titled release with a year, or nothing.
func movie(title, clean string, c *collector) Parsed {
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
	c.add("year", m[2], m[3])
	if q := quality.FindStringSubmatchIndex(clean); q != nil && !music.MatchString(clean) {
		p.Kind = Movie
		c.add("kind.movie", q[2], q[3])
	}
	return p
}

// series returns the Parsed of a series with the title key, with the season,
// episode and year the cleaned name gives.
func series(k, clean string, c *collector) Parsed {
	p := Parsed{Kind: Series, Key: k}
	if k == "" {
		return p
	}
	if m := season.FindStringSubmatchIndex(clean); m != nil {
		from, to, _ := group(m[4:])
		p.Season, _ = strconv.Atoi(clean[from:to])
		if p.Season != 0 {
			c.add("season", m[2], m[3])
		}
	}
	p.Episode = episodeOf(clean, c)
	if m := year.FindStringSubmatchIndex(clean); m != nil {
		p.Year, _ = strconv.Atoi(clean[m[2]:m[3]])
		c.add("year", m[2], m[3])
	}
	return p
}

// group returns the bounds of the first participating group of the match
// indexes loc, a pair per group.
func group(loc []int) (from, to int, ok bool) {
	for g := 0; 2*g+1 < len(loc); g++ {
		if loc[2*g] >= 0 {
			return loc[2*g], loc[2*g+1], true
		}
	}
	return 0, 0, false
}

// episodeOf returns the episode number the cleaned name gives, 0 when it
// gives none or a range.
func episodeOf(clean string, c *collector) int {
	m := episode.FindStringSubmatchIndex(clean)
	if m == nil {
		return 0
	}
	from, to, _ := group(m[4:])
	n, _ := strconv.Atoi(clean[from:to])
	if n == 0 || episodeRange.MatchString(clean[to:]) {
		return 0
	}
	c.add("episode", m[2], m[3])
	return n
}

// qualityOf returns the highest quality the cleaned name gives, "" for none.
func qualityOf(clean string, c *collector) string {
	for _, q := range qualities {
		if m := q.re.FindStringSubmatchIndex(clean); m != nil {
			c.add("quality."+q.quality, m[2], m[3])
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
