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

// A work whose previous place was below the top's 100 or absent reads
// "new" in bold, its previous place in the tooltip; one within reads the
// places it moved.
func TestTopMoved(t *testing.T) {
	isNew := func(label, tip string) string {
		return `<b><span class="tip" tabindex="0" aria-label="` + label + `: ` + tip + `">` + label +
			`<span class="tipb" aria-hidden="true">` + tip + `</span></span></b>`
	}
	for lang, want := range map[string][]string{
		"ru": {
			isNew("новое", "В прошлом окне: 9\u00a0787-е место."),
			isNew("новое", "В прошлом окне не было."),
			`<td class="r dl">&#43;1</td>`,
			`<td class="r dl mu">0</td>`,
		},
		"en": {
			isNew("new", "In the previous window: 9,787th place."),
			isNew("new", "Not in the previous window."),
		},
	} {
		body := renderTop(t, lang, func(p *TopPage) {
			p.Works[4].PrevRank = 0
			p.Works[5].PrevRank = 9787
		})
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
		t.Error("the 100th place the window before is a move, not new")
	}
	if !strings.Contains(body, isNew("new", "In the previous window: 101st place.")) {
		t.Error("the 101st place the window before is new")
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
