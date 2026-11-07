package ui

import (
	"net/http/httptest"
	"testing"
	"time"
)

// The store tables are a grid of three, every row with its share as a
// percentage and a bar; with no audio a table is gone and its cell stays
// empty; labels are lower-case.
func TestRenderStore(t *testing.T) {
	const nbsp = " "
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	b := r.Base(req, "ru", "data", []string{"ru"}, time.UTC)
	b.Now = time.Date(2026, 10, 20, 14, 5, 0, 0, time.UTC)
	render := func(data any) string {
		t.Helper()
		w := httptest.NewRecorder()
		if err := r.Render(w, req, "data", data); err != nil {
			t.Fatal(err)
		}
		return w.Body.String()
	}
	checkBody(t, "all", render(SampleData(b)), []string{`<div class="gone">`, `>Adult<`, `>Тип файлов<`, `>Явная пометка<`, `>Всего<`}, []string{
		`<div class="g3">`,
		`<a href="/torrents?kind=movie">фильм</a></td><td class="r nw">312` + nbsp + `940</td><td class="r mu nw">24,4%</td><td class="bar"><i style="width: 24.4%"></i></td>`,
		`<td>adult</td><td class="r nw">2` + nbsp + `114` + nbsp + `820</td><td class="r mu nw">44,5%</td><td class="bar"><i style="width: 44.5%"></i></td>`,
		`<td>тип файлов</td>`, `<td>явная пометка</td>`, `Озвучка по языкам: ru`,
	})
	checkBody(t, "off", render(SampleDataFeatures(b, "off")), []string{`Озвучка по языкам`}, []string{`<div class="gone"></div>`, `<td>adult</td>`})
}
