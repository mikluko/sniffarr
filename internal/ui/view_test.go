package ui

import (
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	libui "sarak.sh/mikluko/libarr/ui"
)

// The magnet link survives html/template's URL filter, with the name escaped.
func TestTorrentMagnetHref(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/t/x", nil)
	b := r.Base(req, "en", "", []string{"ru"}, time.UTC)
	p := SampleTorrent(b, false, "poster")
	p.Torrent.InfoHash = "8f3c2a91d07e4b6c5a1e9f20b37d4c8e6a0f1b52"
	p.Torrent.Name = `Чужой (1979) & "Aliens"+<b>#1`
	w := httptest.NewRecorder()
	if err := r.Render(w, req, "torrent", p); err != nil {
		t.Fatal(err)
	}
	body := w.Body.String()
	want := `href="magnet:?xt=urn:btih:8f3c2a91d07e4b6c5a1e9f20b37d4c8e6a0f1b52&amp;dn=%D0%A7%D1%83%D0%B6%D0%BE%D0%B9%20%281979%29%20%26%20%22Aliens%22%2B%3Cb%3E%231"`
	if !strings.Contains(body, want) {
		t.Errorf("no %s in the page", want)
	}
	if strings.Contains(body, "ZgotmplZ") {
		t.Error("the URL filter blocked a link")
	}
}

// The counts are stale past two of the intervals they are refreshed at:
// the page's own, or the API's when that is longer.
func TestDataStale(t *testing.T) {
	now := time.Date(2026, 10, 20, 14, 5, 0, 0, time.UTC)
	for _, c := range []struct {
		age, interval time.Duration
		stale         bool
	}{
		{time.Minute, 0, false},
		{time.Minute + time.Second, 0, true},
		{3 * time.Minute, 5 * time.Minute, false},
		{10 * time.Minute, 5 * time.Minute, false},
		{12 * time.Minute, 5 * time.Minute, true},
		{90 * time.Second, 10 * time.Second, true},
	} {
		p := DataPage{Base: Base{Locale: libui.Locale{Now: now}}, Stats: Stats{Computed: now.Add(-c.age), Interval: c.interval}}
		if got := p.Stale(); got != c.stale {
			t.Errorf("age %v, interval %v: stale %v, want %v", c.age, c.interval, got, c.stale)
		}
	}
}

// The data page's refresh button, its staleness line, the queue's badges
// and bars, and the meters of the daily limits.
func TestDataMarkup(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	b := r.Base(req, "en", "data", nil, time.UTC)
	b.Now, b.Probes, b.Classifier = time.Date(2026, 10, 20, 14, 5, 0, 0, time.UTC), true, true
	render := func(p DataPage) string {
		t.Helper()
		w := httptest.NewRecorder()
		if err := r.Render(w, req, "data", p); err != nil {
			t.Fatal(err)
		}
		return w.Body.String()
	}
	svg, err := r.shared.Icon("refresh")
	if err != nil {
		t.Fatal(err)
	}
	fresh, stale := render(SampleData(b)), render(SampleDataStale(b))
	for _, want := range []string{
		`hx-trigger="every 30s [document.visibilityState=='visible'], tz"`,
		`<button type="button" class="ic" hx-get="/" hx-target="#data" hx-swap="morph:outerHTML" aria-label="Refresh" aria-describedby="refresh-tip">` + string(svg) + `</button>`,
		`<span class="tc" id="refresh-tip" role="tooltip">Refresh. Data as of Oct 20, 14:02, updated 3 min ago. The page refreshes itself every 30 seconds. The server recomputes the data every 5 min.</span>`,
		`<span class="tt" tabindex="0" aria-label="P0: asked for in the UI"><span class="badge"><span class="sw q0"></span>P0</span><span class="tc" aria-hidden="true">P0: asked for in the UI</span></span>`,
		`<div class="qbar" role="img" aria-label="Probes: P0 1; P1 63; P3 2;"><i class="q0" style="flex-grow: 1"></i><i class="q1" style="flex-grow: 63"></i><i class="q3" style="flex-grow: 2"></i></div>`,
		`<span class="xs dw">≈ 107.2% by 00:00 UTC</span>`,
		`<span class="xs mu">≈ 79.0% by 00:00 UTC</span>`,
		`<i style="width: 82.6%"></i><span class="proj" style="width: 100.0%"></span>`,
		`now 530 an hour`,
	} {
		if !strings.Contains(fresh, want) {
			t.Errorf("no %s", want)
		}
	}
	if strings.Contains(fresh, `class="xs dw">updated`) {
		t.Error("fresh data shows its age outside the tooltip")
	}
	if !strings.Contains(stale, `<span class="xs dw">updated 12 min ago</span>`) {
		t.Error("stale data does not show its age")
	}
	probe := "Downloaded on the UTC day"
	if !strings.Contains(fresh, probe) {
		t.Errorf("no probe meter %q", probe)
	}
	for _, want := range []string{">P1: Radarr and Sonarr, searches<", ">P3: sampled, background probes<"} {
		if !strings.Contains(fresh, want) {
			t.Errorf("no %s while probes are on", want)
		}
	}
	b.Probes = false
	off := render(SampleData(b))
	if strings.Contains(off, probe) {
		t.Error("the probe meter shows while probes are off")
	}
	for _, want := range []string{">P1: Radarr and Sonarr<", ">P3: sampled<"} {
		if !strings.Contains(off, want) {
			t.Errorf("no %s while probes are off", want)
		}
	}
}

func TestActsProbes(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/t/x", nil)
	b := r.Base(req, "en", "", []string{"ru"}, time.UTC)
	render := func(b Base) string {
		t.Helper()
		w := httptest.NewRecorder()
		if err := r.Render(w, req, "torrent", SampleTorrent(b, false, "nopeers")); err != nil {
			t.Fatal(err)
		}
		return w.Body.String()
	}
	b.Probes = true
	if on := render(b); !strings.Contains(on, `id="act-probe"`) || !strings.Contains(on, b.T("act.probe_nopeers")) {
		t.Error("no probe button, or no line on why it is disabled, while probes are on")
	}
	b.Probes = false
	off := render(b)
	if strings.Contains(off, `act-probe`) || strings.Contains(off, b.T("act.probe_nopeers")) {
		t.Error("the probe button or its line shows while probes are off")
	}
	if !strings.Contains(off, `id="act-peers"`) {
		t.Error("the peers button goes with it")
	}
}

// Filters.Set counts the filters that narrow the results, not the sort, the
// voice filter's language or its "any".
func TestFiltersSet(t *testing.T) {
	for _, c := range []struct {
		f    Filters
		want int
	}{
		{Filters{}, 0},
		{Filters{VoiceLang: "ru", Voice: "any", Sort: "size"}, 0},
		{Filters{Kind: "movie", Quality: "1080p"}, 2},
		{Filters{Matched: "no", Source: "web", Layer: "rules", MinSizeGB: "1", Voice: "explicit"}, 5},
	} {
		if got := c.f.Set(); got != c.want {
			t.Errorf("%+v: %d, want %d", c.f, got, c.want)
		}
	}
}

// The torrents page: the layer that decided the kind with its meaning in a
// tooltip, the first sighting in the last one's tooltip, the placeholders
// for a poster, the filters set bordered and counted, and the phone's list
// with the dub of each configured language.
func TestTorrentsMarkup(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	loc := time.FixedZone("CEST", 2*3600)
	req := httptest.NewRequest("GET", "/torrents", nil)
	b := r.Base(req, "ru", "torrents", []string{"ru"}, loc)
	b.Now = time.Date(2026, 10, 20, 14, 5, 0, 0, loc)
	render := func(p TorrentsPage) string {
		t.Helper()
		w := httptest.NewRecorder()
		if err := r.Render(w, req, "torrents", p); err != nil {
			t.Fatal(err)
		}
		return w.Body.String()
	}
	tip := func(label, text string) string {
		return `<span class="tt" tabindex="0" aria-label="` + template.HTMLEscapeString(label+": "+text) + `">` +
			template.HTMLEscapeString(label) + `<span class="tc" aria-hidden="true">` + template.HTMLEscapeString(text) + `</span></span>`
	}
	ph := func(mark, text string) string {
		return `<span class="th th-ph tt" tabindex="0" aria-label="` + text + `">` + mark + `<span class="tc" aria-hidden="true">` + text + `</span></span>`
	}
	body := render(SampleTorrents(b, "", Filters{}))
	for _, want := range []string{
		tip("фильм", "Вид определил классификатор, уверенность 0,93."),
		tip("фильм", "Вид определён правилами по имени и файлам."),
		tip("фильм", "Вид взят у раздачи с тем же названием."),
		tip("Вид", "Кто решил вид и с какой уверенностью, в тултипе у значения."),
		tip("20.10 14:00", "Впервые: 25.08"),
		ph("?", "Не сопоставлено с фильмом или сериалом."),
		ph("—", "Сопоставлено, постера в TMDB нет."),
		`<span class="tmeta mu">3,0` + " " + `ГБ · 1080p · сериал · web · ru <b>LostFilm</b></span>`,
		`<span class="tmeta mu">400` + " " + `МБ · прочее · прочее</span>`,
		`<span>1–11 из 11</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("no %s", want)
		}
	}
	undecided := SampleTorrents(b, "", Filters{})
	undecided.Results.Rows[0].Kind, undecided.Results.Rows[0].Layer = "unknown", "none"
	if body := render(undecided); !strings.Contains(body, tip("неизвестно", "Вид ещё не определён.")) {
		t.Error("no tooltip on a kind no layer decided")
	}
	bare := SampleTorrents(b, "", Filters{})
	bare.Results.Rows[0].Quality, bare.Results.Rows[0].Source = "", "none"
	zero := 0.0
	bare.Results.Rows[0].Audio = []Audio{{Lang: "ru", Probability: &zero}}
	if body := render(bare); !strings.Contains(body, "<span class=\"tmeta mu\">22,0 ГБ · фильм</span>") {
		t.Error("a meta line with empty fields keeps their separators or a dub nobody asserts")
	}
	for _, not := range []string{`class="set"`, `class="tfreset"`, `<span class="mu"> · `, `· —`} {
		if strings.Contains(body, not) {
			t.Errorf("%s without a filter set", not)
		}
	}

	body = render(SampleTorrents(b, "spider", Filters{Kind: "movie", Quality: "1080p"}))
	for _, want := range []string{
		`<select name="kind" class="set">`,
		`<select name="quality" class="set">`,
		`<select name="matched">`,
		`Фильтры<span class="mu"> · 2</span></button>`,
		`<a class="tfreset" href="/torrents?q=spider"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("filtered: no %s", want)
		}
	}

	opt := `<option value="classifier"`
	if body := render(SampleTorrents(b, "", Filters{})); strings.Contains(body, opt) {
		t.Error("the classifier layer offered while it is off")
	}
	if body := render(SampleTorrents(b, "", Filters{Layer: "classifier"})); !strings.Contains(body, opt) {
		t.Error("the selected classifier layer dropped from the filter")
	}
	b.Classifier = true
	if body := render(SampleTorrents(b, "", Filters{})); !strings.Contains(body, opt) {
		t.Error("no classifier layer while it is on")
	}

	b.VoiceLangs = nil
	if body := render(SampleTorrents(b, "", Filters{})); strings.Contains(body, " · ru ") {
		t.Error("a dub in the phone's list without dub languages")
	}
}

// A release filter's URL keeps the others, drops defaults and the offset.
func TestReleaseURL(t *testing.T) {
	p := WorkPage{Base: Base{Locale: libui.Locale{Lang: "en"}, VoiceLangs: []string{"ru", "uk"}}, Releases: ReleaseTable{Path: "/tv/1",
		Filters: ReleaseFilters{Quality: "1080p", Season: "2", Sort: "peers"}}}
	for _, c := range []struct{ got, want string }{
		{p.ReleaseURL("episode", "4"), "/tv/1?episode=4&quality=1080p&season=2"},
		{p.ReleaseURL("quality", ""), "/tv/1?season=2"},
		{p.ReleaseURL("sort", "size"), "/tv/1?quality=1080p&season=2&sort=size"},
		{p.ReleasePageURL(50), "/tv/1?o=50&quality=1080p&season=2"},
	} {
		if c.got != c.want {
			t.Errorf("got %s, want %s", c.got, c.want)
		}
	}
	if n := p.FiltersSet(); n != 2 {
		t.Errorf("FiltersSet = %d, want 2", n)
	}
	p.Releases.Filters.MinGB, p.Releases.Filters.Episode, p.Releases.Filters.Sort = "4", "5", "size"
	if n := p.FiltersSet(); n != 4 {
		t.Errorf("FiltersSet = %d, want 4, the order not counted", n)
	}
}

// The work page hides the probe button while probes are off and the dub
// fact without languages; the reset shows only with a filter set; there is
// no source or dub filter or column.
func TestWorkMarkup(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/movie/900001", nil)
	render := func(b Base, f ReleaseFilters) string {
		t.Helper()
		w := httptest.NewRecorder()
		if err := r.Render(w, req, "work", SampleWork(b, SampleSpiderID, f)); err != nil {
			t.Fatal(err)
		}
		return w.Body.String()
	}
	b := r.Base(req, "en", "", []string{"ru"}, time.UTC)
	b.Probes = true
	on := render(b, ReleaseFilters{})
	for _, want := range []string{`id="act-probe"`, `<span>Voice-over</span>`, `Releases <span class="mu">95</span>`, `<th>Quality</th>`, `<th class="r">First seen</th>`,
		`role="group" aria-label="Quality"`, `name="min"`, `name="sort"`,
		`<img class="hero-ph" src="https://image.tmdb.org/t/p/w185/`, ` 2x" width="185" height="278"`,
		`<span>24 h #2 · 7 days <b>#1</b> · 30 days #3</span>`, `<span>24 h #1 · 7 days #1 · 30 days #2</span>`} {
		if !strings.Contains(on, want) {
			t.Errorf("no %s", want)
		}
	}
	if strings.Contains(on, "Reset filters") {
		t.Error("a reset with no filter set")
	}
	if set := render(b, ReleaseFilters{Quality: "sd"}); !strings.Contains(set, ">Reset filters</a>") || !strings.Contains(set, `<span class="mu"> · 1</span>`) {
		t.Error("no reset or count with a filter set")
	}
	b.Probes = false
	if off := render(b, ReleaseFilters{}); strings.Contains(off, "act-probe") {
		t.Error("the probe button shows while probes are off")
	}
	for _, s := range []string{`aria-label="Source"`, `aria-label="Voice-over"`, `name="source"`, `name="voice"`, `<th>Source</th>`, `<th>Voice-over</th>`, `name="episode"`, `<th>Episode</th>`} {
		if strings.Contains(on, s) {
			t.Errorf("a movie has %s", s)
		}
	}
	b.VoiceLangs = nil
	none := render(b, ReleaseFilters{})
	if strings.Contains(none, `<span>Voice-over</span>`) {
		t.Error("the dub fact shows without languages")
	}
}

// A series adds season and episode filters and an episode column to the
// movie's, and has no source or dub filter or column either.
func TestWorkSeriesMarkup(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/tv/106379", nil)
	w := httptest.NewRecorder()
	b := r.Base(req, "en", "", []string{"ru"}, time.UTC)
	if err := r.Render(w, req, "work", SampleWork(b, SampleFalloutID, ReleaseFilters{Season: "2"})); err != nil {
		t.Fatal(err)
	}
	body := w.Body.String()
	for _, want := range []string{`aria-label="Season"`, `aria-label="Quality"`, `name="min"`, `name="episode"`, `name="sort"`,
		`<th>Release</th>`, `<th>Episode</th>`, `<th class="r">Peers 7 d</th>`, `<th class="r">Size</th>`, `<th>Quality</th>`, `<th class="r">First seen</th>`} {
		if !strings.Contains(body, want) {
			t.Errorf("no %s", want)
		}
	}
	for _, s := range []string{`aria-label="Source"`, `aria-label="Voice-over"`, `name="source"`, `name="voice"`, `<th>Source</th>`, `<th>Voice-over</th>`} {
		if strings.Contains(body, s) {
			t.Errorf("has %s", s)
		}
	}
}

// The peak row shows a dash for a window the title has no best place in,
// which is every window before the top's history begins, and its label
// carries the tooltip saying what the row is.
func TestWorkPeakWithoutHistory(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/movie/900001", nil)
	b := r.Base(req, "en", "", []string{"ru"}, time.UTC)
	p := SampleWork(b, SampleSpiderID, ReleaseFilters{})
	p.Peaks = [3]int{}
	w := httptest.NewRecorder()
	if err := r.Render(w, req, "work", p); err != nil {
		t.Fatal(err)
	}
	body := w.Body.String()
	for _, want := range []string{
		`<span class="tt" tabindex="0" aria-label="Peak: `,
		`<span>24 h <span class="mu">—</span> · 7 days <span class="mu">—</span> · 30 days <span class="mu">—</span></span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("no %s", want)
		}
	}
}

// A release whose discovery the API does not name has no discovery row.
func TestRenderDiscoveryUnnamed(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/t/x", nil)
	b := r.Base(req, "en", "", []string{"ru"}, time.UTC)
	b.Now = time.Date(2026, 10, 20, 14, 5, 0, 0, time.UTC)
	render := func(via string) string {
		t.Helper()
		p := SampleTorrent(b, false, "poster")
		p.Torrent.DiscoveredVia = via
		w := httptest.NewRecorder()
		if err := r.Render(w, req, "torrent", p); err != nil {
			t.Fatal(err)
		}
		return w.Body.String()
	}
	row := "<th>" + b.T("card.via") + "</th><td>" + b.T("via.announce") + "</td>"
	if !strings.Contains(render("announce"), row) {
		t.Errorf("no %q", row)
	}
	if strings.Contains(render(""), b.T("card.via")) {
		t.Error("a discovery row for an unnamed discovery")
	}
}

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
