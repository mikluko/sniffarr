// Package audio defines the languages of the audio extension: the signs in a
// torrent's names of an audio track in a language, and what the classifier
// is told to look for.
package audio

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/mikluko/sniffarr/internal/rules"
)

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
	// StudioNames are the display names of the Studios, by studio as
	// written in Studios; spellings of one studio share a name.
	StudioNames map[string]string
	// Codes are the translation codes of the language, matched in their
	// case: one or more after the year and before the source, as in
	// Movie.2026.D.P2.WEB-DL, or a comma-separated list between bars, as in
	// "Movie (2026) WEB-DL | D, P2 |". A code alone, Plan.B, is no marker.
	Codes []string
	// Signs tells the classifier how a release names or implies an audio
	// track of the language.
	Signs string

	marker  *regexp.Regexp
	codes   []*regexp.Regexp
	studio  *regexp.Regexp
	studios []*regexp.Regexp
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
		StudioNames: map[string]string{
			"lostfilm": "LostFilm", "newstudio": "NewStudio", "jaskier": "Jaskier", "hdrezka": "HDRezka",
			"кубик в кубе": "Кубик в Кубе", "alexfilm": "AlexFilm", "baibako": "BaibaKo", "coldfilm": "ColdFilm",
			"tvshows": "TVShows", "novamedia": "NovaMedia", "amedia": "Amedia", "кураж-бамбей": "Кураж-Бамбей",
			"red head sound": "Red Head Sound", "rhs": "Red Head Sound", "гоблин": "Гоблин", "anilibria": "AniLibria",
			"anidub": "AniDub", "shiza": "SHIZA", "anistar": "AniStar", "dream cast": "Dream Cast", "selezen": "seleZen",
			"exkinoray": "ExKinoRay", "scarabey": "Scarabey", "hellywood": "Hellywood", "generalfilm": "GeneralFilm",
		},
		// https://habr.com/ru/articles/80721/ gives D, P, P1, P2, L, L1 and
		// L2; rutor.info lists A for an author's voice-over, as in
		// https://www.rutor.info/torrent/743964. Pm has no source and says
		// a Russian voice of no known kind.
		Codes: []string{"D", "P", "P1", "P2", "Pm", "L", "L1", "L2", "A"},
		Signs: "Russian audio is named or clearly implied: Rus, RUS, rus in a file or folder name, Дубляж, Dub, MVO, DVO, AVO, ПМ, " +
			"a Russian translation code near the year or the source, D dub, P, P1, P2 professional, L, L1, L2 amateur, A author's, Pm, " +
			"as in Movie.2026.P.WEB-DL or BDRip | D, P2, " +
			"a separate Rus*.ac3/.mka/.aac track, a Russian-language title of a video release",
	}),
}

// wholeWords matches any of words as a whole word regardless of case and
// captures it; nil for no words.
func wholeWords(words []string) *regexp.Regexp {
	if len(words) == 0 {
		return nil
	}
	alts := make([]string, len(words))
	for i, w := range words {
		alts[i] = regexp.QuoteMeta(w)
	}
	return regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(` + strings.Join(alts, "|") + `)(?:$|[^\p{L}\p{N}])`)
}

// Where translation codes stand in a release name: after a year, its zeros
// perhaps written as the letter O, and before a source; or between bars.
const (
	codeYear   = `(?:^|[^\p{L}\p{N}])(?:19|2[0oOоО])[\doOоО]{2}[.\s_]+`
	codeSource = `[.\s_]+(?i:web-?dl(?:rip)?|web-?rip|web|bd-?rip|bd-?remux|blu-?ray|hd-?rip|hdtv(?:rip)?|dvd-?rip|dvd(?:5|9|scr)?|` +
		`hdts|ts|tc|telecine|telesync|hdcam|cam(?:rip)?|satrip|tvrip|remux|uhd|d-theaterrip)(?:$|[^\p{L}\p{N}])`
)

// codeLists matches translation codes where release names put them; group 1
// is the codes and their separators.
func codeLists(codes []string) []*regexp.Regexp {
	if len(codes) == 0 {
		return nil
	}
	alts := make([]string, len(codes))
	for i, c := range codes {
		alts[i] = regexp.QuoteMeta(c)
	}
	code := `(?:` + strings.Join(alts, "|") + `)`
	return []*regexp.Regexp{
		regexp.MustCompile(codeYear + `(` + code + `(?:[.\s_]+` + code + `)*)` + codeSource),
		regexp.MustCompile(`\|\s*(` + code + `(?:\s*,\s*` + code + `)*)\s*(?:\||$)`),
	}
}

// compile prepares l's matchers; it panics on a script name unicode.Scripts
// does not have, or on a studio without a display name.
func compile(l Language) Language {
	l.codes = codeLists(l.Codes)
	l.marker = wholeWords(append(append([]string{}, l.Words...), l.Studios...))
	l.studio = wholeWords(l.Studios)
	for _, st := range l.Studios {
		if l.StudioNames[st] == "" {
			panic(fmt.Sprintf("audio: language %s: no display name for studio %q", l.Tag, st))
		}
		l.studios = append(l.studios, wholeWords([]string{st}))
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

// Lookup returns the known language with the tag, regardless of case.
func Lookup(tag string) (Language, bool) {
	return lookup(tag)
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
	if len(langs) == 0 {
		return nil
	}
	out := make([]string, len(langs))
	for i, l := range langs {
		out[i] = l.Tag
	}
	return out
}

// Marker reports whether the name or a file path that is not a subtitle has
// one of the language's Words, Studios or Codes.
func (l Language) Marker(name string, files []rules.File) bool {
	return slices.ContainsFunc(append([]string{name}, markerPaths(files)...), func(s string) bool {
		return l.marker != nil && l.marker.MatchString(s) || l.CodesIn(s) != nil
	})
}

// Mark returns the first of the language's Studios in the name, or else in a
// file path that is not a subtitle, as written there; without a studio, the
// first of its Words the same way, then the first of its Codes; "" when
// there is none. A studio names the track more precisely than a word that
// only names the language.
func (l Language) Mark(name string, files []rules.File) string {
	texts := append([]string{name}, markerPaths(files)...)
	for _, re := range []*regexp.Regexp{l.studio, l.marker} {
		if re == nil {
			continue
		}
		for _, s := range texts {
			if m := re.FindStringSubmatch(s); m != nil {
				return m[1]
			}
		}
	}
	for _, s := range texts {
		if codes := l.CodesIn(s); codes != nil {
			return codes[0]
		}
	}
	return ""
}

// CodesIn returns the language's Codes in s where release names put them,
// in the order they read, each once; nil when there is none.
func (l Language) CodesIn(s string) []string {
	var out []string
	for _, re := range l.codes {
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			for _, c := range strings.FieldsFunc(m[1], func(r rune) bool { return r == '.' || r == ',' || r == '_' || unicode.IsSpace(r) }) {
				if !slices.Contains(out, c) {
					out = append(out, c)
				}
			}
		}
	}
	return out
}

// StudiosIn returns the display names of the language's Studios found in the
// name or a file path, in the order of Studios, each once; nil when there is
// none.
func (l Language) StudiosIn(name string, files []rules.File) []string {
	return l.StudiosInTexts(append([]string{name}, paths(files)...))
}

// StudiosInTexts returns the display names of the language's Studios found
// in any of the texts, in the order of Studios, each once; nil when there is
// none.
func (l Language) StudiosInTexts(texts []string) []string {
	var out []string
	for i, re := range l.studios {
		n := l.StudioNames[l.Studios[i]]
		if !slices.Contains(out, n) && slices.ContainsFunc(texts, re.MatchString) {
			out = append(out, n)
		}
	}
	return out
}

// subtitles are the extensions of subtitle files, in lower case.
var subtitles = []string{".srt", ".ass", ".ssa", ".sub", ".idx", ".sup"}

// Subtitle reports whether p is the path of a subtitle file, by its
// extension in any case.
func Subtitle(p string) bool {
	return slices.Contains(subtitles, strings.ToLower(path.Ext(p)))
}

// markerPaths are the paths of the files that are not subtitles: a word in
// a subtitle's path names the subtitle's language, not the audio's.
func markerPaths(files []rules.File) []string {
	var out []string
	for _, f := range files {
		if !Subtitle(f.Path) {
			out = append(out, f.Path)
		}
	}
	return out
}

func paths(files []rules.File) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
	}
	return out
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
