package ui

import (
	"strconv"
	"strings"
)

// DefaultVoiceLang picks the dub language the filters start with: the UI
// language when it is configured, else the first configured one, else "".
func DefaultVoiceLang(voiceLangs []string, uiLang string) string {
	for _, l := range voiceLangs {
		if l == uiLang {
			return l
		}
	}
	if len(voiceLangs) > 0 {
		return voiceLangs[0]
	}
	return ""
}

func (b Base) has(key string) bool { _, ok := b.msgs[key]; return ok }

// DubLabel names the dub of a language: "русская озвучка" / "Russian dub".
func (b Base) DubLabel(code string) string {
	if b.has("dub." + code) {
		return b.T("dub." + code)
	}
	return b.T("dub.other") + " " + code
}

// LangName names a language: "русская" / "Russian"; the code when unknown.
func (b Base) LangName(code string) string {
	if b.has("langname." + code) {
		return b.T("langname." + code)
	}
	return code
}

// AudioOf returns the entry for lang, ok=false when the release has none.
func AudioOf(list []Audio, lang string) (Audio, bool) {
	for _, a := range list {
		if a.Lang == lang {
			return a, true
		}
	}
	return Audio{Lang: lang}, false
}

// AudioCell renders one release and language: the mark, "явно" for an unnamed
// marker, else the probability, else "—".
func (b Base) AudioCell(a Audio) string {
	switch {
	case a.Marker && a.Mark != "":
		return a.Mark
	case a.Marker:
		return b.T("audio.marker")
	case a.Probability != nil:
		return b.dec(*a.Probability, 2)
	}
	return "—"
}

// AudioFor is AudioCell for lang looked up in list.
func (b Base) AudioFor(list []Audio, lang string) string {
	a, _ := AudioOf(list, lang)
	return b.AudioCell(a)
}

// Marks lists up to three marks and the number of the rest: "seleZen, ExKinoRay +2".
func (b Base) Marks(m []string) string {
	if len(m) <= 3 {
		return strings.Join(m, ", ")
	}
	return strings.Join(m[:3], ", ") + " +" + strconv.Itoa(len(m)-3)
}

// WorkAudioOf returns the summary for lang.
func WorkAudioOf(list []WorkAudio, lang string) WorkAudio {
	for _, a := range list {
		if a.Lang == lang {
			return a
		}
	}
	return WorkAudio{Lang: lang}
}
