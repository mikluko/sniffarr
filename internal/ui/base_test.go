package ui

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	libui "sarak.sh/mikluko/libarr/ui"
)

// Every noun counted in Russian agrees with its number by the number's
// last two digits.
func TestPluralForms(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	b := r.Base(httptest.NewRequest("GET", "/", nil), "ru", "", nil, time.UTC)
	forms := map[int64]string{1: "one", 2: "few", 5: "many", 11: "many", 12: "many", 21: "one", 101: "one", 112: "many", 1001: "one", 3192: "few"}
	for _, key := range []string{"tmdb.votes", "top.rels", "journal.events", "card.files_n", "top.days", "q.tasks_n"} {
		for n, form := range forms {
			want, ok := b.Messages[key+"."+form]
			if !ok {
				t.Errorf("%s has no %s form", key, form)
				continue
			}
			if got := b.Count(n, key); got != b.Num(n)+" "+want {
				t.Errorf("Count(%d, %q) = %q, want the %s form %q", n, key, got, form, want)
			}
		}
	}

	p := SampleWork(b, SampleSpiderID, ReleaseFilters{})
	m := *p.Work.TMDB
	m.Votes = 3192
	p.Work.TMDB = &m
	w := httptest.NewRecorder()
	if err := r.Render(w, httptest.NewRequest("GET", "/movie/900001", nil), "work", p); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.Body.String(), "3 192 голоса") {
		t.Error("the work's votes do not agree with their number: want 3 192 голоса")
	}
}

func TestFormat(t *testing.T) {
	t5 := time.Date(2026, 10, 20, 14, 5, 0, 0, time.UTC)
	ru := Base{Locale: libui.Locale{Lang: "ru"}}
	c := Base{Locale: libui.Locale{Lang: "en"}}
	units := Base{Locale: libui.Locale{Lang: "ru", Messages: map[string]string{"unit.min": "мин", "unit.h": "ч", "unit.d": "д"}}}
	files := map[string]string{"f.one": "файл", "f.few": "файла", "f.many": "файлов"}
	ruf, enf := Base{Locale: libui.Locale{Lang: "ru", Messages: files}}, Base{Locale: libui.Locale{Lang: "en", Messages: map[string]string{"f.one": "file", "f.many": "files"}}}
	tips := Base{Locale: libui.Locale{Lang: "ru", Now: t5, Loc: time.UTC, Messages: map[string]string{"card.match.how.search": "Поиском TMDB",
		"card.match.tip": "{0}, уверенность {1}, {2}.", "card.match.tip_nodate": "{0}, уверенность {1}."}}}
	for _, x := range []struct{ got, want string }{
		{ruf.Count(1, "f"), "1 файл"},
		{ruf.Count(3, "f"), "3 файла"},
		{ruf.Count(12, "f"), "12 файлов"},
		{ruf.Count(21, "f"), "21 файл"},
		{ruf.Count(1014, "f"), "1 014 файлов"},
		{enf.Count(1, "f"), "1 file"},
		{enf.Count(3, "f"), "3 files"},
		{tips.MatchTip(&TMDBWork{Method: "search", Confidence: 1, CheckedAt: t5.AddDate(0, 0, -8)}), "Поиском TMDB, уверенность 1,00, 12.10."},
		{tips.MatchTip(&TMDBWork{Method: "search", Confidence: .5}), "Поиском TMDB, уверенность 0,50."},
		{ru.Num(1284311), "1\u00a0284\u00a0311"},
		{ru.Num(-1284), "-1\u00a0284"},
		{c.Num(1284311), "1,284,311"},
		{ru.Size(26629001216), "24,8\u00a0ГБ"},
		{Base{Locale: libui.Locale{Lang: "ru", Messages: map[string]string{"kind.movie": "фильм"}}}.Facts("movie", &TMDBWork{}), "Фильм"},
		{sentence("сериал"), "Сериал"},
		{sentence(""), ""},
		{c.Size(412 << 20), "412 MB"},
		{ru.Prob(fp(.93)), "0,93"},
		{ru.Pct(41280, 50000), "82,6%"},
		{c.PctW(41280, 50000), "82.6%"},
		{ru.AudioCell(Audio{Lang: "ru", Marker: true, Mark: "seleZen"}), "seleZen"},
		{ru.AudioCell(Audio{Lang: "ru", Probability: fp(.62)}), "0,62"},
		{ru.AudioCell(Audio{Lang: "ru"}), "—"},
		{ru.Marks([]string{"a", "b", "c", "d", "e"}), "a, b, c +2"},
		{DefaultVoiceLang([]string{"uk", "ru"}, "ru"), "ru"},
		{DefaultVoiceLang([]string{"uk", "de"}, "en"), "uk"},
		{DefaultVoiceLang(nil, "ru"), ""},
		{WorkID("series", 1399), "tv1399"},
		{WorkID("movie", 318846), "m318846"},
		{WorkPath("series", 1399), "/tv/1399"},
		{WorkPath("movie", 318846), "/movie/318846"},
		{Base{Seerr: "https://seerr.example.com/"}.SeerrURL(&TMDBWork{State: TMDBMatched, Type: "tv", ID: 1399}), "https://seerr.example.com/tv/1399"},
		{Base{Seerr: "https://seerr.example.com"}.SeerrURL(&TMDBWork{State: TMDBMatched, Type: "movie", ID: 348}), "https://seerr.example.com/movie/348"},
		{Base{}.SeerrURL(&TMDBWork{State: TMDBMatched, Type: "movie", ID: 348}), ""},
		{Base{Seerr: "https://seerr.example.com"}.SeerrURL(nil), ""},
		{Base{Radarr: "https://radarr.example.com/"}.RadarrURL(&TMDBWork{State: TMDBMatched, Type: "movie", ID: 348}), "https://radarr.example.com/add/new?term=tmdb%3A348"},
		{Base{Radarr: "https://radarr.example.com"}.RadarrURL(&TMDBWork{State: TMDBMatched, Type: "tv", ID: 1399}), ""},
		{Base{Sonarr: "https://example.com/sonarr"}.SonarrURL(&TMDBWork{State: TMDBMatched, Type: "tv", ID: 1399}), "https://example.com/sonarr/add/new?term=tmdb%3A1399"},
		{Base{Sonarr: "https://example.com/sonarr"}.SonarrURL(&TMDBWork{State: TMDBMatched, Type: "movie", ID: 348}), ""},
		{Base{}.RadarrURL(&TMDBWork{State: TMDBMatched, Type: "movie", ID: 348}), ""},
		{Base{Radarr: "https://radarr.example.com"}.RadarrURL(&TMDBWork{State: TMDBUnmatched}), ""},
		{units.Span(30 * time.Second), "<\u00a01\u00a0мин"},
		{units.Span(42 * time.Minute), "42\u00a0мин"},
		{units.Span(200 * time.Minute), "3\u00a0ч 20\u00a0мин"},
		{units.Span(5*24*time.Hour + 4*time.Hour + 59*time.Minute), "5\u00a0д 4\u00a0ч"},
		{units.Span(48 * time.Hour), "2\u00a0д"},
		{ru.SE(2, 5), "S02E05"},
		{ru.SE(2, 0), "S02"},
		{Base{Locale: libui.Locale{Lang: "en", Now: t5, Messages: map[string]string{"ago.min": "{0} min ago", "ago.now": "just now"}}}.Ago(t5.Add(-125 * time.Minute)), "125 min ago"},
		{Base{Locale: libui.Locale{Lang: "en", Now: t5, Messages: map[string]string{"ago.now": "just now"}}}.Ago(t5.Add(-59 * time.Second)), "just now"},
	} {
		if x.got != x.want {
			t.Errorf("got %q, want %q", x.got, x.want)
		}
	}
	if k, id, ok := ParseWorkID("tv1399"); !ok || k != "series" || id != 1399 {
		t.Error("ParseWorkID")
	}
	if _, _, ok := ParseWorkID("1399"); ok {
		t.Error("ParseWorkID accepted a bare id")
	}
	if niceCeil(15740) != 20000 || niceCeil(89187) != 100000 {
		t.Error("niceCeil")
	}
}
