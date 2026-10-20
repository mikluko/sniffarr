package rules

import (
	"testing"
	"unicode"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/rot13"
)

const (
	mib = 1 << 20
	gib = 1 << 30
)

func TestCheck(t *testing.T) {
	tests := []struct {
		name  string
		size  int64
		files []File
		want  Reason
	}{
		{"Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.Atmos-FGT", 60 * gib, []File{
			{"Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.Atmos-FGT.mkv", 60 * gib},
			{"Sample/sample.mkv", 50 * mib},
		}, Accept},
		{"Severance.S02.1080p.ATVP.WEB-DL.DDP5.1.H.264-NTb", 30 * gib, []File{
			{"Severance.S02E01.1080p.ATVP.WEB-DL.DDP5.1.H.264-NTb.mkv", 3 * gib},
			{"Severance.S02E02.1080p.ATVP.WEB-DL.DDP5.1.H.264-NTb.mkv", 3 * gib},
		}, Accept},
		{"Pink Floyd - The Dark Side Of The Moon (1973) [FLAC 24-96]", 900 * mib, []File{
			{"01 - Speak to Me.flac", 60 * mib},
			{"02 - Breathe.flac", 120 * mib},
			{"folder.jpg", 2 * mib},
			{"log.txt", 1024},
		}, Accept},
		{rot13.String("Oenmmref.24.03.15.Fbzr.Fprar.KKK.1080c.ZC4-JEO"), 2 * gib, []File{{rot13.String("oenmmref.24.03.15.zc4"), 2 * gib}}, Adult},
		{"[FHD] ABCD-123 Uncensored", 4 * gib, []File{{"ABCD-123.mp4", 4 * gib}}, Adult},
		{"Hot Collection", 4 * gib, []File{{"FC2-PPV-1234567/video.mp4", 4 * gib}}, Adult},
		{"Cyberpunk 2077 [FitGirl Repack]", 70 * gib, []File{{"setup.exe", 5 * mib}, {"fg-01.bin", 70 * gib}}, Software},
		{"Adobe Photoshop 2024 v25.0.1 Portable", 3 * gib, []File{{"Photoshop.exe", 3 * gib}}, Software},
		{"Ubuntu 24.04 Desktop", 6 * gib, []File{{"ubuntu-24.04-desktop-amd64.iso", 6 * gib}}, Software},
		{"Windows 11 23H2 x64 English", 6 * gib, []File{{"Win11_23H2_English_x64.iso", 6 * gib}}, Software},
		{"Blade.Runner.1982.Final.Cut.BD50", 46 * gib, []File{{"Blade.Runner.1982.Final.Cut.iso", 46 * gib}}, Accept},
		{"Amelie (2001)", 8 * gib, []File{{"Amelie.2001.DVD9.PAL.iso", 8 * gib}}, Accept},
		{"Sex.Education.S04.1080p.NF.WEB-DL.DDP5.1.x264", 20 * gib, []File{{"Sex.Education.S04E01.1080p.NF.WEB-DL.mkv", 2 * gib}}, Accept},
		{"Sex and the City (1998-2004) Complete Series", 60 * gib, []File{{"Season 1/Sex and the City S01E01.mkv", gib}}, Accept},
		{"The Nude Bomb (1980) 1080p BluRay", 8 * gib, []File{{"The.Nude.Bomb.1980.1080p.BluRay.mkv", 8 * gib}}, Accept},
		{"Radiohead - In Rainbows (2007) [FLAC]", 300 * mib, []File{{"03 - Nude.flac", 60 * mib}, {"01 - 15 Step.flac", 60 * mib}}, Accept},
		{"Some Game", 20 * gib, []File{{"game/data.bin", 20 * gib}, {"intro.mp4", 100 * mib}}, Software},
		{"Brandon Sanderson - Collection", 2 * gib, []File{{"Mistborn.epub", gib}, {"Elantris.pdf", gib}}, Book},
		{"Marvel Comics 2023", 500 * mib, []File{{"Spider-Man 001.cbz", 500 * mib}}, Book},
		{"Udemy - Complete Python Bootcamp", 10 * gib, []File{{"01 Intro.mp4", 10 * gib}}, Course},
		{"Masterclass Gordon Ramsay Teaches Cooking", 5 * gib, []File{{"lesson01.mp4", 5 * gib}}, Course},
		{"Short.Film.2023.720p.WEB-DL", 40 * mib, []File{{"Short.Film.2023.720p.WEB-DL.mkv", 40 * mib}}, Small},
		{"Wallpapers 4K", 2 * gib, []File{{"001.jpg", gib}, {"002.png", gib}}, NoMedia},
		{"Interstellar.2014.1080p.BluRay.x264.mkv", 10 * gib, nil, Accept},
		{"Анаболики", 10 * gib, []File{{"Анаболики.mkv", 10 * gib}}, Accept},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Check(tt.name, tt.size, tt.files, nil))
		})
	}
}

func TestCheckScripts(t *testing.T) {
	greek, err := ParseScripts("Latin,Cyrillic,Greek")
	require.NoError(t, err)
	tests := []struct {
		name    string
		scripts Scripts
		want    Reason
	}{
		{"Мандалорец.S03.WEB-DL.Rus", nil, Accept},
		{"Amélie.2001.BDRip", nil, Accept},
		{"【4K】Movie.2020", nil, Accept},
		{"Movie.2020 🎬 ★", nil, Accept},
		{"進撃の巨人 S04", nil, Script},
		{"مسلسل.الحلقة.5", nil, Script},
		{"Ελληνικά", nil, Script},
		{"Ελληνικά", greek, Accept},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := []File{{"Sample/外国語.srt", mib}, {"video.mkv", gib}}
			require.Equal(t, tt.want, Check(tt.name, gib, files, tt.scripts))
		})
	}
}

func TestParseScripts(t *testing.T) {
	s, err := ParseScripts("")
	require.NoError(t, err)
	require.Equal(t, Scripts{unicode.Latin, unicode.Cyrillic}, s, "the empty list is DefaultScripts")
	s, err = ParseScripts("Latin, Greek")
	require.NoError(t, err)
	require.Equal(t, Scripts{unicode.Latin, unicode.Greek}, s)
	_, err = ParseScripts("Latin,Klingon")
	require.EqualError(t, err, `unknown script "Klingon"`)
}

func TestRussianMarker(t *testing.T) {
	tests := []struct {
		name  string
		files []File
		want  bool
	}{
		{"Oppenheimer.2023.BDRip.1080p.Rus.Eng", nil, true},
		{"Fallout.S01.1080p.AMZN.WEB-DL", []File{{"Fallout.S01E01.mkv", 1}, {"Audio/RUS/Fallout.S01E01.LostFilm.mka", 1}}, true},
		{"Severance.S02.WEB-DL.1080p", []File{{"Severance.S02E01.mkv", 1}, {"Rus_Sound/Severance.S02E01.ac3", 1}}, true},
		{"Дюна: Часть вторая (2024) BDRip 1080p | Дубляж", nil, true},
		{"The Boys S04 [HDRezka Studio]", nil, true},
		{"Cyberpunk Edgerunners [AniLibria]", nil, true},
		{"Шерлок (2010) Кураж-Бамбей", nil, true},
		{"Russian.Doll.S01.1080p.NF.WEB-DL", nil, false},
		{"Brusnika.2020.WEB-DL", nil, false},
		{"Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX", []File{{"Dune.mkv", 1}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, RussianMarker(tt.name, tt.files))
		})
	}
}

func TestCyrillic(t *testing.T) {
	require.True(t, Cyrillic("Брат 2 (2000) DVDRip", nil))
	require.True(t, Cyrillic("Brat.2.2000.DVDRip", []File{{Path: "Брат 2.avi"}}))
	require.False(t, Cyrillic("Brat.2.2000.DVDRip.Rus", []File{{Path: "Brat 2.avi"}}), "a marker is not a Cyrillic letter")
}
