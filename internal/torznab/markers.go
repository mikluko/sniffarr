package torznab

import (
	"regexp"
	"slices"
	"strings"

	"golang.org/x/text/language"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/audio"
)

// voice is a kind of Russian audio a track's name can say, labelled as the
// release-title custom formats of Radarr and Sonarr read it.
type voice struct {
	label string
	// track matches a track name that says the voice.
	track *regexp.Regexp
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
	}
}

// voices are in the order a title lists them; a track takes the first that
// matches its name.
var voices = []voice{
	voiceOf("Dub", `дубл|dubbing|dubbed`, ""),
	voiceOf("MVO", `многоголос`, `МВО`),
	voiceOf("DVO", `двухголос|двуголос`, `ДВО`),
	voiceOf("AVO", `авторск`, `АВО`),
	voiceOf("VO", `одноголос`, `ВО`),
}

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

// russianCodes reads the Russian translation codes in release names.
var russianCodes, _ = audio.Lookup("ru")

// codeVoices are the voices the Russian translation codes say; a code
// missing here, Pm, says no kind of voice.
var codeVoices = map[string]string{"D": "Dub", "P": "MVO", "L": "MVO", "P2": "DVO", "L2": "DVO", "A": "AVO", "P1": "VO", "L1": "VO"}

// codeLabels returns the voices the Russian translation codes in name say,
// in voices order.
func codeLabels(name string) []voice {
	said := map[string]bool{}
	for _, c := range russianCodes.CodesIn(name) {
		said[codeVoices[c]] = true
	}
	var out []voice
	for _, v := range voices {
		if said[v.label] {
			out = append(out, v)
		}
	}
	return out
}

// noRussian opens the marker of a release whose probed audio tracks are all
// in other languages. No release name has it, and neither Radarr's language
// parser nor a custom format reading RUS as a word takes it for Russian.
const noRussian = "NORUS"

// otherLanguages returns noRussian and the ISO 639-2 codes of the audio
// tracks in their order, as "NORUS FRA, ENG"; "" when there is no audio track
// or one gives no language.
func otherLanguages(tracks []*sniffarrv1.Track) string {
	var codes []string
	for _, t := range tracks {
		b, ok := base(t)
		if !ok {
			return ""
		}
		if code := strings.ToUpper(b.ISO3()); !slices.Contains(codes, code) {
			codes = append(codes, code)
		}
	}
	if len(codes) == 0 {
		return ""
	}
	return noRussian + " " + strings.Join(codes, ", ")
}
