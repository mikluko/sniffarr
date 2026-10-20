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
		{"Severance.S02E01.1080p.ATVP.WEB-DL.DDP5.1.H.264-NTb", Parsed{Series, "s:severance", "s:severance/2", 0, 2}},
		{"Severance.S02.1080p.ATVP.WEB-DL.DDP5.1.H.264-NTb", Parsed{Series, "s:severance", "s:severance/2", 0, 2}},
		{"How.I.Met.Your.Mother.S06E19.720p.HDTV.X264-DIMENSION.mkv", Parsed{Series, "s:how i met your mother", "s:how i met your mother/6", 0, 6}},
		{"The Office (US) Season 3 Complete 720p", Parsed{Series, "s:the office", "s:the office/3", 0, 3}},
		{"Twin Peaks 1x05 DVDRip", Parsed{Series, "s:twin peaks", "s:twin peaks/1", 0, 1}},
		{"Fallout S1E1-8 of 8 [2024, WEB-DL 1080p]", Parsed{Series, "s:fallout", "s:fallout/1", 2024, 1}},
		{"[SubsPlease] Kuroneko to Majo no Kyoushitsu - 09 (1080p) [0F72F2B7].mkv", Parsed{Series, "s:kuroneko to majo no kyoushitsu", "s:kuroneko to majo no kyoushitsu", 0, 0}},
		{"[Erai-raws] Honzuki no Gekokujou S4 - 19 [720p CR WEB-DL AVC AAC][MultiSub][FA2C7C2D].mkv", Parsed{Series, "s:honzuki no gekokujou", "s:honzuki no gekokujou/4", 0, 4}},
		{"Taboo - Temporada 1 [HDTV 720p][Cap.104][AC3 5.1 Español Castellano]", Parsed{Series, "s:taboo", "s:taboo/1", 0, 1}},
		{"Dark - Staffel 2 [WEB-DL 1080p]", Parsed{Series, "s:dark", "s:dark/2", 0, 2}},
		{"Фоллаут / Fallout / Сезон: 1 / Серии: 1-8 из 8 (Джонатан Нолан) [2024, фантастика, WEB-DL 1080p] MVO (LostFilm)", Parsed{Series, "s:фоллаут", "s:фоллаут/1", 2024, 1}},
		{"Слово пацана. Кровь на асфальте (1 сезон, 1-8 серии из 8) [2023, WEB-DLRip]", Parsed{Series, "s:слово пацана кровь на асфальте", "s:слово пацана кровь на асфальте/1", 2023, 1}},
		{"Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.Atmos-FGT", Parsed{Movie, "y:dune part two 2024", "y:dune part two 2024", 2024, 0}},
		{"Interstellar 2014 1080p BluRay x264", Parsed{Movie, "y:interstellar 2014", "y:interstellar 2014", 2014, 0}},
		{"Дюна: Часть вторая / Dune: Part Two (Дени Вильнёв) [2024, фантастика, BDRip 1080p] Dub", Parsed{Movie, "y:дюна часть вторая 2024", "y:дюна часть вторая 2024", 2024, 0}},
		{"Брат 2 (Алексей Балабанов) [2000, драма, DVDRip]", Parsed{Movie, "y:брат 2 2000", "y:брат 2 2000", 2000, 0}},
		{"Oppenheimer (2023)", Parsed{"", "y:oppenheimer 2023", "y:oppenheimer 2023", 2023, 0}},
		{"Pink Floyd - The Dark Side Of The Moon (1973) [FLAC 24-96]", Parsed{"", "y:pink floyd the dark side of the moon 1973", "y:pink floyd the dark side of the moon 1973", 1973, 0}},
		{"Bruce Springsteen - 1973-2021 FLAC (Jamal The Moroccan)", Parsed{"", "y:bruce springsteen 1973", "y:bruce springsteen 1973", 1973, 0}},
		{"Ubuntu 24.04 Desktop", Parsed{}},
		{"Various Artists - Chill Collection", Parsed{}},
		{"2024 1080p", Parsed{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Parse(tt.name))
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
