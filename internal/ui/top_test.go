package ui

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// renderTop renders the sample top of movies in lang with the Russian
// audio language.
func renderTop(t *testing.T, lang string, edit func(*TopPage)) string {
	t.Helper()
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/top", nil)
	b := r.Base(req, lang, "top", []string{"ru"}, time.UTC)
	b.Now = time.Date(2026, 10, 20, 14, 5, 0, 0, time.UTC)
	p := SampleTop(b, "movie", false)
	if edit != nil {
		edit(&p)
	}
	w := httptest.NewRecorder()
	if err := r.Render(w, req, "top", p); err != nil {
		t.Fatal(err)
	}
	return w.Body.String()
}

// tip is the markup of a tooltip on label.
func tip(label, text string) string {
	return `<span class="tip" tabindex="0" aria-label="` + label + `: ` + text + `">` + label +
		`<span class="tipb" aria-hidden="true">` + text + `</span></span>`
}

// A work within the top in the previous window reads the places it moved,
// "=" muted for none. One outside it, by its place or for having none,
// reads "new" or "back" in bold, or a muted dash where the history cannot
// tell which.
func TestTopChange(t *testing.T) {
	for lang, want := range map[string][]string{
		"ru": {
			`<td class="r dl">&#43;1</td>`,
			`<td class="r dl">−1</td>`,
			`<td class="r dl mu">=</td>`,
			`<td class="r dl"><b>` + tip("новое", "Впервые в первой сотне.") + `</b></td>`,
			`<td class="r dl"><b>` + tip("снова", "Вернулось в сотню. В прошлом окне: 9 787-е место.") + `</b></td>`,
		},
		"en": {
			`<td class="r dl"><b>` + tip("NEW", "In the top 100 for the first time.") + `</b></td>`,
			`<td class="r dl"><b>` + tip("RE", "Back in the top 100. In the previous window: 9,787th place.") + `</b></td>`,
		},
	} {
		body := renderTop(t, lang, nil)
		for _, s := range want {
			if !strings.Contains(body, s) {
				t.Errorf("%s: no %s", lang, s)
			}
		}
	}
	body := renderTop(t, "en", func(p *TopPage) {
		p.Works[0].PrevRank = 100
		p.Works[1].PrevRank = 101
	})
	if !strings.Contains(body, `<td class="r dl">&#43;99</td>`) {
		t.Error("the 100th place the window before is a move")
	}
	if strings.Count(body, ">NEW<") != 2 {
		t.Error("the 101st place the window before is an entry")
	}
	body = renderTop(t, "ru", func(p *TopPage) { p.Since = p.Works[4].Debut })
	if want := `<td class="r dl mu">` + tip("—", "В сотне с начала истории, 19.10.2026; было ли раньше, не записано. В прошлом окне не было.") + `</td>`; !strings.Contains(body, want) {
		t.Errorf("an entry at the history's start is neither new nor back: no %s", want)
	}
	body = renderTop(t, "ru", func(p *TopPage) { p.Works[4].Debut = time.Time{} })
	if want := `<td class="r dl mu">` + tip("—", "Истории топа ещё нет. В прошлом окне не было.") + `</td>`; !strings.Contains(body, want) {
		t.Errorf("no history: no %s", want)
	}
}

func TestOnChart(t *testing.T) {
	b := Base{Lang: "ru", msgs: map[string]string{"top.d": "дн", "top.w": "нед"}}
	day := 24 * time.Hour
	for d, want := range map[time.Duration]string{0: "< 1 дн", 23 * time.Hour: "< 1 дн", day: "1 дн",
		14*day + 23*time.Hour: "14 дн", 15 * day: "2 нед", 26 * day: "3 нед", 120 * day: "17 нед"} {
		if got := b.OnChart(d); got != want {
			t.Errorf("OnChart(%v) = %q, want %q", d, got, want)
		}
	}
}

// The chart columns read the work's history: time in the top with its
// days in the tooltip, the peak bold when the work is at it, and the
// debut; a work in the top when the history began says so, and one
// without history has dashes.
func TestTopHistory(t *testing.T) {
	body := renderTop(t, "ru", nil)
	for _, want := range []string{
		`<td class="r oc">` + tip("3 нед", "Всего в топе: 26 дней.") + `<span class="mb"> в топе</span></td>`,
		`<td class="r pk"><span class="mb">пик </span><b>№1</b></td>`,
		`<td class="r pk mu"><span class="mb">пик </span>№1</td>`,
		`<td class="r db mu"><span class="mb">с </span>11.10</td>`,
		"Сколько всего фильм был в первой сотне этого окна, с повторными входами. До двух недель в днях, дальше в неделях. История топа ведётся с 15.09.2025.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("no %s", want)
		}
	}
	body = renderTop(t, "ru", func(p *TopPage) { p.Since = p.Works[4].Debut })
	for _, want := range []string{
		`<td class="r oc">` + tip("1 дн", "В топе с начала истории, 19.10.2026: 1 день; раньше не записано.") + `<span class="mb"> в топе</span></td>`,
		`<td class="r db mu mh">` + tip("≤ 19.10", "Было в сотне уже в начале истории, 19.10.2026; раньше не записано.") + `</td>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("at the history's start: no %s", want)
		}
	}
	body = renderTop(t, "ru", func(p *TopPage) { p.Works[4].Debut = time.Time{} })
	if want := `<td class="r oc mu mh">—</td><td class="r pk mu mh">—</td><td class="r db mu mh">—</td>`; !strings.Contains(body, want) {
		t.Errorf("no history: no %s", want)
	}
}

// The top's filters are segments of links with the voice's label in a
// tooltip, a work with no poster has an empty frame, and the releases'
// audio and sources are not the top's.
func TestTopMarkup(t *testing.T) {
	body := renderTop(t, "en", nil)
	for _, want := range []string{
		`<a class="o on" href="/top?kind=movie&amp;win=7d" hx-get="/top?kind=movie&amp;win=7d" hx-target="#top" hx-swap="morph:outerHTML" hx-push-url="true" aria-current="true">7 days</a>`,
		`<a class="o" href="/top?kind=movie&amp;voice=explicit&amp;win=7d"`,
		`hx-push-url="true">explicit</a>`,
		`<span class="mu">` + tip("Russian dub", "Which releases count towards peers and place. “≥ 0.5”: an audio estimate of 0.5 or more. “explicit”: the audio is named in the torrent&#39;s name.") + `</span>`,
		`<span class="th th-none" aria-hidden="true"></span>`,
		"<td class=\"r rl\"><span class=\"dk\">95</span><span class=\"mb\">95 releases</span></td>",
		`<table class="t tt fit">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("no %s", want)
		}
	}
	for _, gone := range []string{`class="au"`, `class="src`, "Marked in the name"} {
		if strings.Contains(body, gone) {
			t.Errorf("has %s", gone)
		}
	}
}

func TestPrevPlace(t *testing.T) {
	b := Base{Lang: "en", msgs: map[string]string{"top.prev_place": "{0}"}}
	for n, want := range map[int]string{1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 11: "11th", 12: "12th", 13: "13th",
		21: "21st", 101: "101st", 111: "111th", 112: "112th", 9787: "9,787th"} {
		if got := b.PrevPlace(n); got != want {
			t.Errorf("PrevPlace(%d) = %q, want %q", n, got, want)
		}
	}
}
