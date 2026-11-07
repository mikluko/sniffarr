package ui

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// headerMarkup renders the page "notfound" under nav in lang and returns its header.
func headerMarkup(t *testing.T, lang, nav string) string {
	t.Helper()
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/x", nil)
	b := r.Base(req, lang, nav, nil, time.UTC)
	w := httptest.NewRecorder()
	if err := r.Render(w, req, "notfound", NotFoundPage{Base: b, Asked: "/x"}); err != nil {
		t.Fatal(err)
	}
	body := w.Body.String()
	i, j := strings.Index(body, "<header"), strings.Index(body, "</header>")
	if i < 0 || j < i {
		t.Fatalf("no header in %s", body)
	}
	return body[i:j]
}

// The header is the logo, the tabs of the torrents and the top, and the
// language switch, in one row that does not wrap, so that at 375 px it is
// one line of 44.
func TestHeader(t *testing.T) {
	css, err := os.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), ".top{flex-wrap:nowrap}") {
		t.Error("the header wraps")
	}
	for _, lang := range []string{"ru", "en"} {
		for _, c := range []struct{ nav, on string }{
			{"data", ""}, {"", ""}, {"torrents", "/torrents"}, {"top", "/top"},
		} {
			t.Run(lang+"/"+c.nav, func(t *testing.T) { checkTabs(t, headerMarkup(t, lang, c.nav), c.nav, c.on) })
			t.Run(lang+"/"+c.nav+"/row", func(t *testing.T) { checkRow(t, headerMarkup(t, lang, c.nav), c.nav) })
		}
	}
}

// checkTabs fails unless h has the two tabs, the torrents and the top, and
// only the one at href on selected, none for "".
func checkTabs(t *testing.T, h, nav, on string) {
	t.Helper()
	tabs := h[strings.Index(h, "<nav>") : strings.Index(h, "</nav>")+len("</nav>")]
	if n := strings.Count(tabs, "<a "); n != 2 {
		t.Errorf("%d tabs, want 2: %s", n, tabs)
	}
	if !strings.Contains(tabs, `<a href="/torrents"`) || !strings.Contains(tabs, `<a href="/top"`) {
		t.Errorf("tabs are not the torrents and the top: %s", tabs)
	}
	if strings.Contains(tabs, `href="/"`) {
		t.Errorf("the data page is a tab: %s", tabs)
	}
	want := 0
	if on != "" {
		want = 1
		if !strings.Contains(tabs, `<a href="`+on+`" class="on" aria-current="page">`) {
			t.Errorf("%s not selected: %s", on, tabs)
		}
	}
	if n := strings.Count(h, `class="on"`); n != want {
		t.Errorf("%d tabs selected, want %d", n, want)
	}
}

// checkRow fails unless h is the logo, the tabs and the switch, in that order,
// the logo marked current on the data page only.
func checkRow(t *testing.T, h, nav string) {
	t.Helper()
	last := -1
	for _, s := range []string{`<a href="/" class="brand"`, "<nav>", `<span class="langs">`} {
		i := strings.Index(h, s)
		if i <= last {
			t.Errorf("%q is not after the one before it: %s", s, h)
		}
		last = i
	}
	if got := strings.Contains(h, `class="brand" aria-current="page"`); got != (nav == "data") {
		t.Errorf("the logo marked current = %v", got)
	}
}
