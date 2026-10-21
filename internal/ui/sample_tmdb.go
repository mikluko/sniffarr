package ui

import "time"

// Sample titles for tests and cmd/preview.
//
// TMDB/IMDb ids, match method and confidence of the titles marked "db" come
// from real rows of the sniffarr database. Everything else is illustrative:
// ids marked "illustrative", ratings, votes, runtimes, genres, peers, release
// counts and poster paths (fake; cmd/preview serves placeholders for them).

type titleSample struct {
	kind    string // movie | series
	m       func(lang string) *TMDBWork
	rels    int // 0 = only in TMDB
	peers   int64
	delta   *float64
	voice   sv
	sources []string
	ageDays int
	ranks   [3]int // 24h, 7d, 30d; filled by the catalog order when zero
}

type sv struct {
	Explicit bool
	Prob     *float64
}

var checkedAt = time.Date(2026, 10, 12, 3, 0, 0, 0, time.UTC)

func tm(id int64, typ, imdb, method string, conf float64, year int, poster string, ru, en, orig string, f func(*TMDBWork, string)) func(string) *TMDBWork {
	return func(lang string) *TMDBWork {
		t := &TMDBWork{State: TMDBMatched, ID: id, Type: typ, IMDbID: imdb, Method: method, Confidence: conf,
			Year: year, PosterPath: poster, Title: en, OriginalTitle: orig, CheckedAt: checkedAt}
		if lang == "ru" {
			t.Title = ru
		}
		if f != nil {
			f(t, lang)
		}
		return t
	}
}

func g(lang string, ru, en []string) []string {
	if lang == "ru" {
		return ru
	}
	return en
}

func ov(lang string) string {
	return g(lang, []string{"[Описание из TMDB. В списках не показывается, на карточке видно три строки, на странице фильма или сериала шесть, полный текст на TMDB.]"},
		[]string{"[TMDB overview. Lists omit it, the torrent card shows three lines, the title page six; the full text is on TMDB.]"})[0]
}

// Spider-Man: Brand New Day stands for the merged title: releases named
// "Spider-Man Brand New Day" and "Человек-паук: Новый день" are one title.
var spiderMan = titleSample{kind: "movie", m: tm(900001 /* illustrative */, "movie", "", "search", 1, 2026, "/sample-spider.jpg",
	"Человек-паук: Новый день", "Spider-Man: Brand New Day", "Spider-Man: Brand New Day", func(t *TMDBWork, lang string) {
		t.Runtime, t.Rating, t.Votes, t.Countries, t.Language = 132, 7.1, 1400, []string{"US"}, "en"
		t.Genres = g(lang, []string{"боевик", "приключения", "фантастика"}, []string{"Action", "Adventure", "Science Fiction"})
		t.Overview = ov(lang)
	}), rels: 95, peers: 61240, delta: fp(.12), voice: sv{Explicit: true}, sources: []string{"web", "rip", "remux"}, ageDays: 40, ranks: [3]int{2, 1, 3}}

var sampleMovies = []titleSample{
	spiderMan,
	{kind: "movie", m: tm(318846 /* db */, "movie", "", "search", 1, 2015, "/sample-318846.jpg",
		"Игра на понижение", "The Big Short", "The Big Short", func(t *TMDBWork, lang string) {
			t.Runtime, t.Rating, t.Votes, t.Countries, t.Language = 130, 7.4, 9800, []string{"US"}, "en"
			t.Genres = g(lang, []string{"комедия", "драма"}, []string{"Comedy", "Drama"})
			t.Overview = ov(lang)
		}), rels: 18, peers: 41760, delta: fp(.41), voice: sv{Explicit: true}, sources: []string{"web", "rip"}, ageDays: 900},
	{kind: "movie", m: tm(508943 /* db */, "movie", "", "classifier", 1, 2021, "/sample-508943.jpg",
		"Лука", "Luca", "Luca", func(t *TMDBWork, lang string) { t.Runtime, t.Countries, t.Language = 95, []string{"US"}, "en" }),
		rels: 6, peers: 35020, delta: fp(.06), voice: sv{Explicit: true}, sources: []string{"web", "rip"}, ageDays: 400},
	{kind: "movie", m: tm(49802 /* db */, "movie", "", "search", 1, 1981, "/sample-49802.jpg",
		"Тайна третьей планеты", "The Mystery of the Third Planet", "Тайна третьей планеты", func(t *TMDBWork, lang string) {
			t.Runtime, t.Countries, t.Language = 50, []string{"SU"}, "ru"
		}), rels: 8, peers: 31880, delta: fp(-.02), voice: sv{Explicit: true}, sources: []string{"web", "rip"}, ageDays: 900},
	{kind: "movie", m: tm(1368337 /* db */, "movie", "", "search", 1, 2026, "",
		"Одиссея", "The Odyssey", "The Odyssey", func(t *TMDBWork, lang string) { t.Countries, t.Language = []string{"US"}, "en" }),
		rels: 3, peers: 27310, delta: nil, voice: sv{Prob: fp(.91)}, sources: []string{"web"}, ageDays: 4},
	{kind: "movie", m: tm(1124566 /* db */, "movie", "tt27714581", "search", 1, 2025, "/sample-1124566.jpg",
		"Sentimental Value", "Sentimental Value", "Sentimental Value", func(t *TMDBWork, lang string) { t.Language = "no" }),
		rels: 4, peers: 25990, delta: fp(-.04), voice: sv{Prob: fp(.22)}, sources: []string{"web"}, ageDays: 30},
	{kind: "movie", m: tm(851479 /* db */, "movie", "", "search", 1, 2024, "",
		"Я, Побєда і Берлін", "Я, Побєда і Берлін", "Я, Побєда і Берлін", func(t *TMDBWork, lang string) { t.Countries, t.Language = []string{"UA"}, "uk" }),
		rels: 2, peers: 20470, delta: fp(-.01), voice: sv{Prob: fp(.31)}, sources: []string{"web"}, ageDays: 300},
	{kind: "movie", m: tm(2179 /* db */, "movie", "tt0365830", "search", 1, 2006, "/sample-2179.jpg",
		"Tenacious D in The Pick of Destiny", "Tenacious D in The Pick of Destiny", "Tenacious D in The Pick of Destiny", func(t *TMDBWork, lang string) { t.Language = "en" }),
		rels: 5, peers: 19880, delta: fp(-.11), voice: sv{Explicit: true}, sources: []string{"rip"}, ageDays: 1200},
}

var sampleSeries = []titleSample{
	{kind: "series", m: tm(106379 /* db */, "tv", "", "classifier", .99, 2024, "/sample-106379.jpg",
		"Fallout", "Fallout", "Fallout", func(t *TMDBWork, lang string) {
			t.Status, t.Seasons, t.Countries, t.Language, t.Runtime = "Returning Series", 2, []string{"US"}, "en", 60
			t.Genres = g(lang, []string{"драма", "фантастика"}, []string{"Drama", "Sci-Fi & Fantasy"})
			t.Overview = ov(lang)
		}), rels: 41, peers: 88410, delta: nil, voice: sv{Explicit: true}, sources: []string{"web"}, ageDays: 23},
	{kind: "series", m: tm(1413 /* db */, "tv", "tt1844624", "search", .9, 2011, "/sample-1413.jpg",
		"Американская история ужасов", "American Horror Story", "American Horror Story", func(t *TMDBWork, lang string) { t.Countries, t.Language = []string{"US"}, "en" }),
		rels: 64, peers: 52930, delta: fp(-.18), voice: sv{Explicit: true}, sources: []string{"web"}, ageDays: 20},
	{kind: "series", m: tm(1399 /* db */, "tv", "tt0944947", "search", .9, 2011, "/sample-1399.jpg",
		"Игра престолов", "Game of Thrones", "Game of Thrones", func(t *TMDBWork, lang string) {
			t.Status, t.Seasons, t.Countries, t.Language = "Ended", 8, []string{"US"}, "en"
		}), rels: 120, peers: 33010, delta: fp(-.03), voice: sv{Explicit: true}, sources: []string{"web", "remux"}, ageDays: 2000},
	{kind: "series", m: tm(289560 /* db */, "tv", "", "classifier", .61, 2026, "/sample-289560.jpg",
		"CIA", "CIA", "CIA", func(t *TMDBWork, lang string) {
			t.Status, t.Runtime, t.Rating, t.Votes, t.Countries, t.Language = "Returning Series", 45, 6.8, 210, []string{"US"}, "en"
		}), rels: 10, peers: 30880, delta: fp(-.04), voice: sv{Prob: fp(.4)}, sources: []string{"web"}, ageDays: 14},
	{kind: "series", m: tm(318485 /* db */, "tv", "", "search", .85, 2025, "",
		"Грачи", "Грачи", "Грачи", func(t *TMDBWork, lang string) { t.Countries, t.Language = []string{"RU"}, "ru" }),
		rels: 8, peers: 28440, delta: fp(.01), voice: sv{Explicit: true}, sources: []string{"web", "rip"}, ageDays: 40},
	{kind: "series", m: tm(82728 /* db */, "tv", "", "classifier", 1, 2018, "/sample-82728.jpg",
		"Блуи", "Bluey", "Bluey", func(t *TMDBWork, lang string) { t.Countries, t.Language = []string{"AU"}, "en" }),
		rels: 7, peers: 19870, delta: fp(.02), voice: sv{Explicit: true}, sources: []string{"web"}, ageDays: 700},
}

// TMDB-only titles: found by the TMDB search, no releases with us.
var sampleTMDBOnly = []titleSample{
	{kind: "movie", m: tm(1930 /* illustrative */, "movie", "", "", 0, 2012, "/sample-1930.jpg",
		"Новый Человек-паук", "The Amazing Spider-Man", "The Amazing Spider-Man", func(t *TMDBWork, lang string) {
			t.Runtime, t.Rating, t.Votes, t.Countries, t.Language = 136, 6.7, 17000, []string{"US"}, "en"
			t.Overview = ov(lang)
		})},
	{kind: "series", m: tm(900002 /* illustrative */, "tv", "", "", 0, 1994, "",
		"Человек-паук", "Spider-Man", "Spider-Man", func(t *TMDBWork, lang string) {
			t.Status, t.Seasons, t.Countries, t.Language = "Ended", 5, []string{"US"}, "en"
		})},
}

func (s titleSample) summary(lang string, rank int) WorkSummary {
	t := s.m(lang)
	return WorkSummary{ID: WorkID(s.kind, t.ID), Kind: s.kind, TMDB: t, Releases: s.rels, Peers: s.peers, Rank: rank}
}

var markPool = []string{"Дубляж", "LostFilm", "MVO", "HDRezka", "Jaskier", "seleZen", "ExKinoRay"}

func sampleAudio(s titleSample, i int, langs []string) []WorkAudio {
	var out []WorkAudio
	for _, l := range langs {
		a := WorkAudio{Lang: l}
		switch {
		case l == "ru" && s.rels == 95:
			a.Marked, a.Marks, a.Likely = 2, []string{"seleZen", "ExKinoRay"}, 7
		case l == "ru" && s.voice.Explicit:
			a.Marked = 1 + s.rels/3
			for k := 0; k < a.Marked && k < 5; k++ {
				a.Marks = append(a.Marks, markPool[(i+k)%len(markPool)])
			}
			a.Likely = s.rels / 4
		case l == "ru" && s.voice.Prob != nil && *s.voice.Prob >= .5:
			a.Likely = 1 + s.rels/2
		case l == "uk" && i%3 == 0:
			a.Marked, a.Marks, a.Likely = 1, []string{"UKR"}, s.rels/6
		case l == "uk" && i%3 == 1:
			a.Likely = 1
		}
		out = append(out, a)
	}
	return out
}

// findTitle looks a sample title up by its URL id.
func findTitle(id string) (titleSample, int, bool) {
	for _, list := range [][]titleSample{sampleMovies, sampleSeries, sampleTMDBOnly} {
		for i, s := range list {
			if WorkID(s.kind, s.m("en").ID) == id {
				return s, i, true
			}
		}
	}
	return titleSample{}, 0, false
}

// Sample ids for tests and preview links.
var (
	SampleSpiderID   = WorkID("movie", 900001)
	SampleFalloutID  = WorkID("series", 106379)
	SampleTMDBOnlyID = WorkID("movie", 1930)
)
