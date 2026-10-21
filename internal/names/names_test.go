package names

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		want Parsed
	}{
		{"Severance.S02E01.1080p.ATVP.WEB-DL.DDP5.1.H.264-NTb", Parsed{Series, "s:severance", 0, 2, 1, Quality1080p}},
		{"Severance.S02.1080p.ATVP.WEB-DL.DDP5.1.H.264-NTb", Parsed{Series, "s:severance", 0, 2, 0, Quality1080p}},
		{"How.I.Met.Your.Mother.S06E19.720p.HDTV.X264-DIMENSION.mkv", Parsed{Series, "s:how i met your mother", 0, 6, 19, Quality720p}},
		{"The Office (US) Season 3 Complete 720p", Parsed{Series, "s:the office", 0, 3, 0, Quality720p}},
		{"Twin Peaks 1x05 DVDRip", Parsed{Series, "s:twin peaks", 0, 1, 5, QualitySD}},
		{"Fallout S1E1-8 of 8 [2024, WEB-DL 1080p]", Parsed{Series, "s:fallout", 2024, 1, 0, Quality1080p}},
		{"[SubsPlease] Kuroneko to Majo no Kyoushitsu - 09 (1080p) [0F72F2B7].mkv", Parsed{Series, "s:kuroneko to majo no kyoushitsu", 0, 0, 0, Quality1080p}},
		{"[Erai-raws] Honzuki no Gekokujou S4 - 19 [720p CR WEB-DL AVC AAC][MultiSub][FA2C7C2D].mkv", Parsed{Series, "s:honzuki no gekokujou", 0, 4, 0, Quality720p}},
		{"Taboo - Temporada 1 [HDTV 720p][Cap.104][AC3 5.1 Español Castellano]", Parsed{Series, "s:taboo", 0, 1, 0, Quality720p}},
		{"Dark - Staffel 2 [WEB-DL 1080p]", Parsed{Series, "s:dark", 0, 2, 0, Quality1080p}},
		{"Фоллаут / Fallout / Сезон: 1 / Серии: 1-8 из 8 (Джонатан Нолан) [2024, фантастика, WEB-DL 1080p] MVO (LostFilm)", Parsed{Series, "s:фоллаут", 2024, 1, 0, Quality1080p}},
		{"Слово пацана. Кровь на асфальте (1 сезон, 1-8 серии из 8) [2023, WEB-DLRip]", Parsed{Series, "s:слово пацана кровь на асфальте", 2023, 1, 0, ""}},
		{"Слово пацана. Кровь на асфальте (1 сезон, 5 серия) [2023, WEB-DL 1080p]", Parsed{Series, "s:слово пацана кровь на асфальте", 2023, 1, 5, Quality1080p}},
		{"Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.Atmos-FGT", Parsed{Movie, "y:dune part two 2024", 2024, 0, 0, Quality2160p}},
		{"Interstellar 2014 1080p BluRay x264", Parsed{Movie, "y:interstellar 2014", 2014, 0, 0, Quality1080p}},
		{"Дюна: Часть вторая / Dune: Part Two (Дени Вильнёв) [2024, фантастика, BDRip 1080p] Dub", Parsed{Movie, "y:дюна часть вторая 2024", 2024, 0, 0, Quality1080p}},
		{"Брат 2 (Алексей Балабанов) [2000, драма, DVDRip]", Parsed{Movie, "y:брат 2 2000", 2000, 0, 0, QualitySD}},
		{"Игра на понижение.2015.WEB-DL.1080р.Open Matte.mkv", Parsed{Movie, "y:игра на понижение 2015", 2015, 0, 0, Quality1080p}},
		{"Oppenheimer (2023)", Parsed{"", "y:oppenheimer 2023", 2023, 0, 0, ""}},
		{"Pink Floyd - The Dark Side Of The Moon (1973) [FLAC 24-96]", Parsed{"", "y:pink floyd the dark side of the moon 1973", 1973, 0, 0, ""}},
		{"Bruce Springsteen - 1973-2021 FLAC (Jamal The Moroccan)", Parsed{"", "y:bruce springsteen 1973", 1973, 0, 0, ""}},
		{"Ubuntu 24.04 Desktop", Parsed{}},
		{"Various Artists - Chill Collection", Parsed{}},
		{"2024 1080p", Parsed{Quality: Quality1080p}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Parse(tt.name))
		})
	}
}

func TestEpisodeAndQuality(t *testing.T) {
	tests := []struct {
		name    string
		episode int
		quality string
	}{
		{"Series.S02E05.1080p", 5, Quality1080p},
		{"Series.S02.E05.1080p", 5, Quality1080p},
		{"Series 1x05", 5, ""},
		{"Series.1x05.720p.WEB-DL", 5, Quality720p},
		{"Show.S01.2160p.HDR.WEB-DL", 0, Quality2160p},
		{"Show.S01E12.2160p.HDR", 12, Quality2160p},
		{"Show.S01E01-E08.1080p", 1, Quality1080p},
		{"Show S1E1-8 of 8 WEB-DL 720p", 0, Quality720p},
		{"Show S01 E05 WEB-DL 720p", 5, Quality720p},
		{"Сериал (1 сезон, Серия 5) WEB-DL 1080p", 5, Quality1080p},
		{"Сериал (1 сезон, 7 серия) HDTVRip", 7, ""},
		{"Сериал (1 сезон, 1-8 серии из 8) DVDRip", 0, QualitySD},
		{"Movie.2001.DVDRip.XviD", 0, QualitySD},
		{"Movie.2001.4K.UHD.BluRay", 0, Quality2160p},
		{"Movie.2001.1080i.HDTV", 0, Quality1080p},
		{"Movie.2001.576p.BluRay", 0, QualitySD},
		{"Movie (2001) BDRemux", 0, ""},
		{"Movie.2001.1080p.Upscale.DVDRip", 0, Quality1080p},
		{"Keynote E3 2001 1080p", 0, Quality1080p},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Parse(tt.name)
			require.Equal(t, tt.episode, p.Episode, "episode")
			require.Equal(t, tt.quality, p.Quality, "quality")
		})
	}
}

func TestTitles(t *testing.T) {
	tests := []struct {
		name string
		want []string
	}{
		{"Severance.S02E01.1080p.ATVP.WEB-DL.DDP5.1.H.264-NTb", []string{"Severance"}},
		{"Alien.1979.Directors.Cut.1080p.BluRay.x264", []string{"Alien"}},
		{"[SubsPlease] Kuroneko to Majo no Kyoushitsu - 09 (1080p) [0F72F2B7].mkv", []string{"Kuroneko to Majo no Kyoushitsu"}},
		{"Фоллаут / Fallout / Сезон: 1 / Серии: 1-8 из 8 (Джонатан Нолан) [2024, фантастика, WEB-DL 1080p] MVO (LostFilm)", []string{"Фоллаут", "Fallout"}},
		{"Дюна: Часть вторая / Dune: Part Two (Дени Вильнёв) [2024, фантастика, BDRip 1080p] Dub", []string{"Дюна: Часть вторая", "Dune: Part Two"}},
		{"Чужой / Alien (1979) BDRip 1080p", []string{"Чужой", "Alien"}},
		{"[2024] 1080p", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Titles(tt.name))
		})
	}
}
