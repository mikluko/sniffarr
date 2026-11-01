package torznab

import (
	"regexp"
	"slices"
	"strings"

	"golang.org/x/text/language"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
)

// voice is a kind of Russian audio a track's name can say, labelled as the
// release-title custom formats of Radarr and Sonarr read it.
type voice struct {
	label string
	// track matches a track name that says the voice.
	track *regexp.Regexp
	// name matches the label in a release name as the custom format does,
	// save where unless matches the text that follows.
	name   *regexp.Regexp
	unless *regexp.Regexp
}

// inName reports whether the custom format of v matches the release name.
func (v voice) inName(name string) bool {
	for _, m := range v.name.FindAllStringIndex(name, -1) {
		if v.unless == nil || !v.unless.MatchString(name[m[1]:]) {
			return true
		}
	}
	return false
}

// Word boundaries in any script; regexp's \b knows ASCII only.
const (
	wordStart = `(?:^|[^\p{L}\p{N}])`
	wordEnd   = `(?:$|[^\p{L}\p{N}])`
)

// voiceOf is the voice of label, which a track name says by one of words in
// any case, or by label or the Cyrillic abbrev, when set, as a word.
func voiceOf(label, words, abbrev string) voice {
	abbrevs := `(?i:` + label + `)`
	if abbrev != "" {
		abbrevs += `|` + abbrev
	}
	return voice{
		label: label,
		track: regexp.MustCompile(`(?i:` + words + `)|` + wordStart + `(?:` + abbrevs + `)` + wordEnd),
		name:  regexp.MustCompile(`(?i)\b\d*x?\s*` + label + `\b`),
	}
}

// voices are in the order a title lists them; a track takes the first that
// matches its name.
var voices = func() []voice {
	dub := voiceOf("Dub", `дубл|dubbing|dubbed`, "")
	dub.unless = regexp.MustCompile(`(?i)^\s*\(?\s*Ukr`)
	return []voice{
		dub,
		voiceOf("MVO", `многоголос`, `МВО`),
		voiceOf("DVO", `двухголос|двуголос`, `ДВО`),
		voiceOf("AVO", `авторск`, `АВО`),
		voiceOf("VO", `одноголос`, `ВО`),
	}
}()

func audioTracks(tracks []*sniffarrv1.Track) []*sniffarrv1.Track {
	var out []*sniffarrv1.Track
	for _, t := range tracks {
		if t.GetKind() == sniffarrv1.TrackKind_TRACK_KIND_AUDIO {
			out = append(out, t)
		}
	}
	return out
}

// base is the language of the track, by its BCP-47 tag when it gives one;
// the zero Base when neither field gives one.
func base(t *sniffarrv1.Track) (language.Base, bool) {
	for _, s := range []string{t.GetLanguageBcp47(), t.GetLanguage()} {
		if s == "" {
			continue
		}
		tag, err := language.Parse(s)
		if err != nil {
			continue
		}
		if b, conf := tag.Base(); conf == language.Exact {
			return b, true
		}
	}
	return language.Base{}, false
}

var ruBase = language.MustParseBase("ru")

// russianLabels returns the voices the names of the Russian tracks among
// the audio tracks say, in voices order; false when none is Russian.
func russianLabels(tracks []*sniffarrv1.Track) ([]voice, bool) {
	var found bool
	said := map[string]bool{}
	for _, t := range tracks {
		if b, ok := base(t); !ok || b != ruBase {
			continue
		}
		found = true
		for _, v := range voices {
			if v.track.MatchString(t.GetName()) {
				said[v.label] = true
				break
			}
		}
	}
	var out []voice
	for _, v := range voices {
		if said[v.label] {
			out = append(out, v)
		}
	}
	return out, found
}

// refused are the codes the custom format for releases without Russian
// audio reads, in its order.
var refused = []string{"ENG", "UKR"}

// otherLanguages returns the ISO 639-2 codes of the audio tracks in the
// form the custom format for releases without Russian audio reads, ENG and
// UKR first and the rest after a slash, as "ENG, UKR / FRA"; "" when there
// is no audio track or one gives no language.
func otherLanguages(tracks []*sniffarrv1.Track) string {
	var known, other []string
	for _, t := range tracks {
		b, ok := base(t)
		if !ok {
			return ""
		}
		code := strings.ToUpper(b.ISO3())
		switch {
		case slices.Contains(known, code), slices.Contains(other, code):
		case slices.Contains(refused, code):
			known = append(known, code)
		default:
			other = append(other, code)
		}
	}
	slices.SortFunc(known, func(a, b string) int { return slices.Index(refused, a) - slices.Index(refused, b) })
	switch {
	case len(known) == 0:
		return strings.Join(other, ", ")
	case len(other) == 0:
		return strings.Join(known, ", ")
	}
	return strings.Join(known, ", ") + " / " + strings.Join(other, ", ")
}
