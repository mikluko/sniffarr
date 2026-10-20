package audio

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/rules"
)

func russian(t *testing.T) Language {
	t.Helper()
	langs, err := Parse("ru")
	require.NoError(t, err)
	require.Len(t, langs, 1)
	return langs[0]
}

func TestMarker(t *testing.T) {
	ru := russian(t)
	tests := []struct {
		name  string
		files []rules.File
		want  bool
	}{
		{"Oppenheimer.2023.BDRip.1080p.Rus.Eng", nil, true},
		{"Fallout.S01.1080p.AMZN.WEB-DL", []rules.File{{Path: "Fallout.S01E01.mkv"}, {Path: "Audio/RUS/Fallout.S01E01.LostFilm.mka"}}, true},
		{"Severance.S02.WEB-DL.1080p", []rules.File{{Path: "Severance.S02E01.mkv"}, {Path: "Rus_Sound/Severance.S02E01.ac3"}}, true},
		{"Дюна: Часть вторая (2024) BDRip 1080p | Дубляж", nil, true},
		{"The Boys S04 [HDRezka Studio]", nil, true},
		{"Cyberpunk Edgerunners [AniLibria]", nil, true},
		{"Шерлок (2010) Кураж-Бамбей", nil, true},
		{"Russian.Doll.S01.1080p.NF.WEB-DL", nil, false},
		{"Brusnika.2020.WEB-DL", nil, false},
		{"Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX", []rules.File{{Path: "Dune.mkv"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ru.Marker(tt.name, tt.files))
		})
	}
}

func TestScriptHint(t *testing.T) {
	ru := russian(t)
	require.True(t, ru.ScriptHint("Брат 2 (2000) DVDRip", nil))
	require.True(t, ru.ScriptHint("Brat.2.2000.DVDRip", []rules.File{{Path: "Брат 2.avi"}}))
	require.False(t, ru.ScriptHint("Brat.2.2000.DVDRip.Rus", []rules.File{{Path: "Brat 2.avi"}}), "a marker is not a letter of the script")
}

func TestParse(t *testing.T) {
	langs, err := Parse("")
	require.NoError(t, err)
	require.Empty(t, langs, "an empty list is no language")
	langs, err = Parse(" RU ")
	require.NoError(t, err)
	require.Equal(t, []string{"ru"}, Tags(langs), "tags compare regardless of case")
	_, err = Parse("ru,xx")
	require.EqualError(t, err, `unknown audio language "xx"`)
	_, err = Parse("ru,ru")
	require.EqualError(t, err, `audio language "ru" listed twice`)
	langs, err = Parse(DefaultLanguages)
	require.NoError(t, err)
	require.NotEmpty(t, langs)
}

func TestKnown(t *testing.T) {
	for _, l := range known {
		require.NotEmpty(t, l.Name, l.Tag)
		require.NotEmpty(t, l.Scripts, l.Tag)
		require.NotEmpty(t, l.Signs, l.Tag)
		require.NotEmpty(t, append(l.Words, l.Studios...), l.Tag)
	}
	require.Panics(t, func() { compile(Language{Tag: "xx", Scripts: []string{"Klingon"}}) })
	require.False(t, compile(Language{Tag: "xx"}).Marker("Anything", nil), "no words, no marker")
}

func TestMark(t *testing.T) {
	ru := russian(t)
	require.Equal(t, "LostFilm", ru.Mark("Severance.S02.1080p.LostFilm", nil))
	require.Equal(t, "Rus", ru.Mark("Alien.1979.1080p", []rules.File{{Path: "Audio/Rus.ac3"}}), "a file path when the name has none")
	require.Empty(t, ru.Mark("Alien.1979.1080p", []rules.File{{Path: "Russian.Doll.mkv"}}), "whole words only")
}
