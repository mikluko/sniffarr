// Package audio defines the languages of the audio extension: the signs in a
// torrent's names of an audio track in a language, and what the classifier
// is told to look for.
package audio

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/mikluko/sniffarr/internal/rules"
)

// DefaultLanguages is the list of tags the server reads when none is
// configured.
const DefaultLanguages = "ru"

// Language is an audio language the extension knows.
type Language struct {
	// Tag is the language's BCP-47 tag.
	Tag string
	// Name is the language's English name.
	Name string
	// Scripts are the unicode.Scripts names of the language's letters.
	Scripts []string
	// Words name an audio track of the language in a release name, matched
	// as whole words regardless of case.
	Words []string
	// Studios are the language's dubbing and voice-over studios and release
	// groups, matched as Words are.
	Studios []string
	// Signs tells the classifier how a release names or implies an audio
	// track of the language.
	Signs string

	marker  *regexp.Regexp
	scripts []*unicode.RangeTable
}

var known = []Language{
	compile(Language{
		Tag:     "ru",
		Name:    "Russian",
		Scripts: []string{"Cyrillic"},
		Words:   []string{"rus", "дубляж", "дублированный", "многоголосый", "двухголосый", "одноголосый"},
		Studios: []string{
			"lostfilm", "newstudio", "jaskier", "hdrezka", "кубик в кубе", "alexfilm", "baibako", "coldfilm",
			"tvshows", "novamedia", "amedia", "кураж-бамбей", "red head sound", "rhs", "гоблин", "anilibria",
			"anidub", "shiza", "anistar", "dream cast", "selezen", "exkinoray", "scarabey", "hellywood", "generalfilm",
		},
		Signs: "Russian audio is named or clearly implied: Rus, RUS, rus in a file or folder name, Дубляж, Dub, MVO, DVO, AVO, ПМ, " +
			"a separate Rus*.ac3/.mka/.aac track, a Russian-language title of a video release",
	}),
}

// compile prepares l's matchers; it panics on a script name unicode.Scripts
// does not have.
func compile(l Language) Language {
	var alts []string
	for _, w := range append(append([]string{}, l.Words...), l.Studios...) {
		alts = append(alts, regexp.QuoteMeta(w))
	}
	if len(alts) > 0 {
		l.marker = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(?:` + strings.Join(alts, "|") + `)(?:$|[^\p{L}\p{N}])`)
	}
	for _, s := range l.Scripts {
		t, ok := unicode.Scripts[s]
		if !ok {
			panic(fmt.Sprintf("audio: language %s: unknown script %q", l.Tag, s))
		}
		l.scripts = append(l.scripts, t)
	}
	return l
}

// Parse returns the known languages of a comma-separated list of tags, in
// list order; an empty list returns none.
func Parse(list string) ([]Language, error) {
	if strings.TrimSpace(list) == "" {
		return nil, nil
	}
	var out []Language
	seen := map[string]bool{}
	for item := range strings.SplitSeq(list, ",") {
		tag := strings.TrimSpace(item)
		l, ok := lookup(tag)
		if !ok {
			return nil, fmt.Errorf("unknown audio language %q", tag)
		}
		if seen[l.Tag] {
			return nil, fmt.Errorf("audio language %q listed twice", tag)
		}
		seen[l.Tag] = true
		out = append(out, l)
	}
	return out, nil
}

func lookup(tag string) (Language, bool) {
	for _, l := range known {
		if strings.EqualFold(l.Tag, tag) {
			return l, true
		}
	}
	return Language{}, false
}

// Tags returns the tags of langs.
func Tags(langs []Language) []string {
	out := make([]string, len(langs))
	for i, l := range langs {
		out[i] = l.Tag
	}
	return out
}

// Marker reports whether the name or a file path has one of the language's
// Words or Studios.
func (l Language) Marker(name string, files []rules.File) bool {
	return l.marker != nil && anyName(name, files, l.marker.MatchString)
}

// ScriptHint reports whether the name or a file path has a letter of one of
// the language's Scripts.
func (l Language) ScriptHint(name string, files []rules.File) bool {
	return anyName(name, files, func(s string) bool {
		for _, r := range s {
			if unicode.In(r, l.scripts...) {
				return true
			}
		}
		return false
	})
}

func anyName(name string, files []rules.File, match func(string) bool) bool {
	if match(name) {
		return true
	}
	for _, f := range files {
		if match(f.Path) {
			return true
		}
	}
	return false
}
