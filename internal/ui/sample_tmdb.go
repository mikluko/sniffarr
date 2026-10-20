package ui

import "time"

// Sample TMDB matches for tests. TMDB/IMDb ids, match method and confidence
// come from real rows of the sniffarr database; ratings, votes, runtimes,
// genres and poster paths are illustrative, and the poster paths are fake.

type tmdbSample struct {
	key     string // title key as the top shows it today
	kind    string // movie | series
	sub     string // season for series
	m       func(lang string) *TMDBWork
	rank    int
	peers   int64
	delta   *float64
	rels    int
	voice   VoiceCell
	sources []string
	ageDays int
}

func tm(id int64, typ, imdb, method string, conf float64, year int, poster string, ru, en, orig string, f func(*TMDBWork, string)) func(string) *TMDBWork {
	return func(lang string) *TMDBWork {
		t := &TMDBWork{State: TMDBMatched, ID: id, Type: typ, IMDbID: imdb, Method: method, Confidence: conf,
			Year: year, PosterPath: poster, Title: en, OriginalTitle: orig,
			CheckedAt: time.Date(2026, 10, 12, 3, 0, 0, 0, time.UTC)}
		if lang == "ru" {
			t.Title = ru
		}
		if f != nil {
			f(t, lang)
		}
		return t
	}
}

func unmatched(string) *TMDBWork {
	return &TMDBWork{State: TMDBUnmatched, CheckedAt: time.Date(2026, 10, 12, 3, 0, 0, 0, time.UTC)}
}

func unchecked(string) *TMDBWork { return nil }

func g(lang string, ru, en []string) []string {
	if lang == "ru" {
		return ru
	}
	return en
}

var sampleMovies = []tmdbSample{
	{key: "the big short 2015", kind: "movie", m: tm(318846, "movie", "", "search", 1, 2015, "/sample-318846.jpg",
		"Игра на понижение", "The Big Short", "The Big Short", func(t *TMDBWork, lang string) {
			t.Runtime, t.Rating, t.Votes, t.Countries, t.Language = 130, 7.4, 9800, []string{"US"}, "en"
			t.Genres = g(lang, []string{"комедия", "драма"}, []string{"Comedy", "Drama"})
			t.Overview = g(lang, []string{"[Описание из TMDB. На карточке видно три строки, остальное обрезается многоточием, полный текст на странице TMDB.]"},
				[]string{"[TMDB overview. The card shows three lines and truncates the rest; the full text is on the TMDB page.]"})[0]
		}), peers: 41760, delta: fp(.41), rels: 5, voice: VoiceCell{Explicit: true}, sources: []string{"web", "rip"}, ageDays: 56},
	{key: "luca 2021", kind: "movie", m: tm(508943, "movie", "", "classifier", 1, 2021, "/sample-508943.jpg",
		"Лука", "Luca", "Luca", func(t *TMDBWork, lang string) { t.Runtime, t.Countries, t.Language = 95, []string{"US"}, "en" }),
		peers: 35020, delta: fp(.06), rels: 6, voice: VoiceCell{Explicit: true}, sources: []string{"web", "rip"}, ageDays: 400},
	{key: "тайна третьей планеты 1981", kind: "movie", m: tm(49802, "movie", "", "search", 1, 1981, "/sample-49802.jpg",
		"Тайна третьей планеты", "The Mystery of the Third Planet", "Тайна третьей планеты", func(t *TMDBWork, lang string) {
			t.Runtime, t.Countries, t.Language = 50, []string{"SU"}, "ru"
		}), peers: 31880, delta: fp(-.02), rels: 8, voice: VoiceCell{Explicit: true}, sources: []string{"web", "rip"}, ageDays: 900},
	{key: "narcissists playbook 2026", kind: "movie", m: unmatched,
		peers: 29570, delta: nil, rels: 2, voice: VoiceCell{Prob: fp(.04)}, sources: []string{"web"}, ageDays: 9},
	{key: "одиссея the odyssey 2026", kind: "movie", m: tm(1368337, "movie", "", "search", 1, 2026, "",
		"Одиссея", "The Odyssey", "The Odyssey", func(t *TMDBWork, lang string) { t.Countries, t.Language = []string{"US"}, "en" }),
		peers: 27310, delta: nil, rels: 3, voice: VoiceCell{Prob: fp(.91)}, sources: []string{"web"}, ageDays: 4},
	{key: "sentimental value 2025", kind: "movie", m: tm(1124566, "movie", "tt27714581", "search", 1, 2025, "/sample-1124566.jpg",
		"Sentimental Value", "Sentimental Value", "Sentimental Value", func(t *TMDBWork, lang string) { t.Language = "no" }),
		peers: 25990, delta: fp(-.04), rels: 4, voice: VoiceCell{Prob: fp(.22)}, sources: []string{"web"}, ageDays: 30},
	{key: "el final de oak street 2026", kind: "movie", m: unmatched,
		peers: 22140, delta: fp(.09), rels: 2, voice: VoiceCell{Prob: fp(.02)}, sources: []string{"rip"}, ageDays: 12},
	{key: "я побєда і берлін 2024", kind: "movie", m: tm(851479, "movie", "", "search", 1, 2024, "",
		"Я, Побєда і Берлін", "Я, Побєда і Берлін", "Я, Побєда і Берлін", func(t *TMDBWork, lang string) { t.Countries, t.Language = []string{"UA"}, "uk" }),
		peers: 20470, delta: fp(-.01), rels: 2, voice: VoiceCell{Prob: fp(.31)}, sources: []string{"web"}, ageDays: 300},
	{key: "tenacious d in the pick of destiny 2006", kind: "movie", m: tm(2179, "movie", "tt0365830", "search", 1, 2006, "/sample-2179.jpg",
		"Tenacious D in The Pick of Destiny", "Tenacious D in The Pick of Destiny", "Tenacious D in The Pick of Destiny", func(t *TMDBWork, lang string) { t.Language = "en" }),
		peers: 19880, delta: fp(-.11), rels: 5, voice: VoiceCell{Explicit: true}, sources: []string{"rip"}, ageDays: 1200},
	{key: "dune part two 2024", kind: "movie", m: unchecked,
		peers: 18210, delta: fp(-.07), rels: 6, voice: VoiceCell{Explicit: true}, sources: []string{"web", "remux", "disc"}, ageDays: 2},
	{key: "twin peaks fire walk with me david lynch remastered 1992", kind: "movie", m: unmatched,
		peers: 17630, delta: fp(.02), rels: 1, voice: VoiceCell{}, sources: []string{"rip"}, ageDays: 60},
}

var sampleSeries = []tmdbSample{
	{key: "fallout", kind: "series", sub: "S02", m: tm(106379, "tv", "", "classifier", .99, 2024, "/sample-106379.jpg",
		"Fallout", "Fallout", "Fallout", func(t *TMDBWork, lang string) {
			t.Status, t.Countries, t.Language, t.Runtime = "Returning Series", []string{"US"}, "en", 60
		}),
		peers: 88410, delta: nil, rels: 14, voice: VoiceCell{Explicit: true}, sources: []string{"web"}, ageDays: 23},
	{key: "american horror story", kind: "series", sub: "S13", m: tm(1413, "tv", "tt1844624", "search", .9, 2011, "/sample-1413.jpg",
		"Американская история ужасов", "American Horror Story", "American Horror Story", func(t *TMDBWork, lang string) { t.Countries, t.Language = []string{"US"}, "en" }),
		peers: 52930, delta: fp(-.18), rels: 11, voice: VoiceCell{Explicit: true}, sources: []string{"web"}, ageDays: 20},
	{key: "law and order svu", kind: "series", sub: "S28", m: unmatched,
		peers: 47520, delta: fp(.07), rels: 12, voice: VoiceCell{Prob: fp(.1)}, sources: []string{"web"}, ageDays: 30},
	{key: "game of thrones", kind: "series", sub: "S01", m: tm(1399, "tv", "tt0944947", "search", .9, 2011, "/sample-1399.jpg",
		"Игра престолов", "Game of Thrones", "Game of Thrones", func(t *TMDBWork, lang string) {
			t.Status, t.Seasons, t.Countries, t.Language = "Ended", 8, []string{"US"}, "en"
		}), peers: 33010, delta: fp(-.03), rels: 9, voice: VoiceCell{Explicit: true}, sources: []string{"web", "remux"}, ageDays: 2000},
	{key: "cia", kind: "series", sub: "S02", m: tm(289560, "tv", "", "classifier", .61, 2026, "/sample-289560.jpg",
		"CIA", "CIA", "CIA", func(t *TMDBWork, lang string) {
			t.Status, t.Runtime, t.Rating, t.Votes, t.Countries, t.Language = "Returning Series", 45, 6.8, 210, []string{"US"}, "en"
			t.Genres = g(lang, []string{"криминал", "драма"}, []string{"Crime", "Drama"})
		}),
		peers: 30880, delta: fp(-.04), rels: 10, voice: VoiceCell{Prob: fp(.4)}, sources: []string{"web"}, ageDays: 14},
	{key: "грачи", kind: "series", sub: "S01", m: tm(318485, "tv", "", "search", .85, 2025, "",
		"Грачи", "Грачи", "Грачи", func(t *TMDBWork, lang string) { t.Countries, t.Language = []string{"RU"}, "ru" }),
		peers: 28440, delta: fp(.01), rels: 8, voice: VoiceCell{Explicit: true}, sources: []string{"web", "rip"}, ageDays: 40},
	{key: "the traitors us", kind: "series", sub: "S05", m: unmatched,
		peers: 21300, delta: fp(-.09), rels: 6, voice: VoiceCell{Prob: fp(.05)}, sources: []string{"web"}, ageDays: 26},
	{key: "bluey", kind: "series", sub: "S01", m: tm(82728, "tv", "", "classifier", 1, 2018, "/sample-82728.jpg",
		"Блуи", "Bluey", "Bluey", func(t *TMDBWork, lang string) { t.Countries, t.Language = []string{"AU"}, "en" }),
		peers: 19870, delta: fp(.02), rels: 7, voice: VoiceCell{Explicit: true}, sources: []string{"web"}, ageDays: 700},
	{key: "re zero kara hajimeru isekai seikatsu 4th season", kind: "series", sub: "", m: unmatched,
		peers: 18420, delta: nil, rels: 9, voice: VoiceCell{}, sources: []string{"web"}, ageDays: 18},
}

func sampleID(s tmdbSample) string {
	b := []byte{}
	for _, r := range s.key + " " + s.sub {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b = append(b, byte(r))
		case r == ' ':
			if len(b) > 0 && b[len(b)-1] != '-' {
				b = append(b, '-')
			}
		default:
			b = append(b, 'x')
		}
	}
	for len(b) > 0 && b[len(b)-1] == '-' {
		b = b[:len(b)-1]
	}
	return s.kind[:1] + "-" + string(b)
}

func (s tmdbSample) ref(rank int) WorkRef {
	return WorkRef{ID: sampleID(s), Kind: s.kind, Title: s.key, Sub: s.sub, Rank: rank}
}
