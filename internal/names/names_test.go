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
		{"Severance.S02E01.1080p.ATVP.WEB-DL.DDP5.1.H.264-NTb", Parsed{Series, "s:severance"}},
		{"Severance.S02.1080p.ATVP.WEB-DL.DDP5.1.H.264-NTb", Parsed{Series, "s:severance"}},
		{"How.I.Met.Your.Mother.S06E19.720p.HDTV.X264-DIMENSION.mkv", Parsed{Series, "s:how i met your mother"}},
		{"The Office (US) Season 3 Complete 720p", Parsed{Series, "s:the office"}},
		{"Twin Peaks 1x05 DVDRip", Parsed{Series, "s:twin peaks"}},
		{"Fallout S1E1-8 of 8 [2024, WEB-DL 1080p]", Parsed{Series, "s:fallout"}},
		{"[SubsPlease] Kuroneko to Majo no Kyoushitsu - 09 (1080p) [0F72F2B7].mkv", Parsed{Series, "s:kuroneko to majo no kyoushitsu"}},
		{"[Erai-raws] Honzuki no Gekokujou S4 - 19 [720p CR WEB-DL AVC AAC][MultiSub][FA2C7C2D].mkv", Parsed{Series, "s:honzuki no gekokujou"}},
		{"Taboo - Temporada 1 [HDTV 720p][Cap.104][AC3 5.1 Español Castellano]", Parsed{Series, "s:taboo"}},
		{"Фоллаут / Fallout / Сезон: 1 / Серии: 1-8 из 8 (Джонатан Нолан) [2024, фантастика, WEB-DL 1080p] MVO (LostFilm)", Parsed{Series, "s:фоллаут"}},
		{"Слово пацана. Кровь на асфальте (1 сезон, 1-8 серии из 8) [2023, WEB-DLRip]", Parsed{Series, "s:слово пацана кровь на асфальте"}},
		{"Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.Atmos-FGT", Parsed{Movie, "y:dune part two 2024"}},
		{"Interstellar 2014 1080p BluRay x264", Parsed{Movie, "y:interstellar 2014"}},
		{"Дюна: Часть вторая / Dune: Part Two (Дени Вильнёв) [2024, фантастика, BDRip 1080p] Dub", Parsed{Movie, "y:дюна часть вторая 2024"}},
		{"Брат 2 (Алексей Балабанов) [2000, драма, DVDRip]", Parsed{Movie, "y:брат 2 2000"}},
		{"Oppenheimer (2023)", Parsed{"", "y:oppenheimer 2023"}},
		{"Pink Floyd - The Dark Side Of The Moon (1973) [FLAC 24-96]", Parsed{"", "y:pink floyd the dark side of the moon 1973"}},
		{"Bruce Springsteen - 1973-2021 FLAC (Jamal The Moroccan)", Parsed{"", "y:bruce springsteen 1973"}},
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
