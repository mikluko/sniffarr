package ui

import (
	"net/http/httptest"
	"testing"
	"time"

	libversion "sarak.sh/mikluko/libarr/version"
	"sarak.sh/mikluko/sniffarr/internal/latest"
	"sarak.sh/mikluko/sniffarr/internal/version"
)

var sampleBuild = libversion.Info{Version: "0.44.0-alpha.2", Commit: "1a2b3c4", Built: time.Date(2026, 11, 7, 14, 2, 0, 0, time.UTC)}

// The footer of every page names the build and links the project page; its
// tooltip adds the build time in UTC and the commit while both are known.
func TestRenderFooterVersion(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	render := func(lang string, build libversion.Info, page string, data func(Base) any) string {
		t.Helper()
		req := httptest.NewRequest("GET", "/", nil)
		b := r.Base(req, lang, "", []string{"ru"}, time.FixedZone("CEST", 2*3600))
		b.Build = build
		w := httptest.NewRecorder()
		if err := r.Render(w, req, page, data(b)); err != nil {
			t.Fatal(err)
		}
		return w.Body.String()
	}
	pages := map[string]func(Base) any{
		"torrents": func(b Base) any { return SampleTorrents(b, "", Filters{}) },
		"notfound": func(b Base) any { return NotFoundPage{Base: b, Asked: "/x"} },
	}
	for page, data := range pages {
		checkBody(t, "ru/"+page, render("ru", sampleBuild, page, data), []string{`class="tt upd"`}, []string{
			`<span class="ver"><a class="tt vl" href="https://sarak.sh/mikluko/sniffarr/releases/tag/v0.44.0-alpha.2" rel="noreferrer" target="_blank" aria-label="sniffarr 0.44.0-alpha.2: Заметки к релизу. Сборка 07.11.2026 14:02 UTC, коммит 1a2b3c4.">sniffarr 0.44.0-alpha.2<span class="tc" aria-hidden="true">Заметки к релизу. Сборка 07.11.2026 14:02 UTC, коммит 1a2b3c4.</span></a></span>`,
		})
		checkBody(t, "en/"+page, render("en", sampleBuild, page, data), nil, []string{
			`aria-label="sniffarr 0.44.0-alpha.2: Release notes. Built Nov 7, 2026, 14:02 UTC, commit 1a2b3c4."`,
		})
		checkBody(t, "dev/"+page, render("en", libversion.Info{Version: "dev"}, page, data), nil, []string{
			`<a class="tt vl" href="https://sarak.sh/mikluko/sniffarr" rel="noreferrer" target="_blank" aria-label="sniffarr dev: Project page.">sniffarr dev<span class="tc" aria-hidden="true">Project page.</span></a></span>`,
		})
	}
}

// The footer announces a newer stable release left of the version when
// version.Outdated holds, linking its notes.
func TestRenderFooterUpdate(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	release := func(v string) latest.Release {
		return latest.Release{Version: v, Published: time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC), URL: "https://sarak.sh/mikluko/sniffarr/releases/tag/v" + v}
	}
	renderAs := func(p version.Policy, lang, build string, rel latest.Release) string {
		t.Helper()
		req := httptest.NewRequest("GET", "/", nil)
		b := r.Base(req, lang, "", []string{"ru"}, time.FixedZone("CEST", 2*3600))
		b.Build, b.Latest, b.Notice = libversion.Info{Version: build}, rel, p
		w := httptest.NewRecorder()
		if err := r.Render(w, req, "notfound", NotFoundPage{Base: b, Asked: "/x"}); err != nil {
			t.Fatal(err)
		}
		return w.Body.String()
	}
	render := func(lang, build string, rel latest.Release) string {
		t.Helper()
		return renderAs(version.Lazy, lang, build, rel)
	}
	checkBody(t, "eager, one patch behind", renderAs(version.Eager, "ru", "0.44.0", release("0.44.1")), nil, []string{
		`<i class="mk" aria-hidden="true"></i>вышла 0.44.1<span class="tc" aria-hidden="true">Эта версия отстала от последнего релиза: 0.44.0, последний 0.44.1 от 01.12.2026. Ссылка на заметки к релизу.</span>`,
	})
	checkBody(t, "stable behind", render("ru", "0.44.0", release("0.46.0")), nil, []string{
		`<span class="ver"><a class="tt upd" href="https://sarak.sh/mikluko/sniffarr/releases/tag/v0.46.0" rel="noreferrer" target="_blank" aria-label="вышла 0.46.0: Эта версия отстала от последнего релиза больше чем на одну минорную: 0.44.0, последний 0.46.0 от 01.12.2026. Ссылка на заметки к релизу."><i class="mk" aria-hidden="true"></i>вышла 0.46.0<span class="tc" aria-hidden="true">Эта версия отстала от последнего релиза больше чем на одну минорную: 0.44.0, последний 0.46.0 от 01.12.2026. Ссылка на заметки к релизу.</span></a><a class="tt vl"`,
	})
	checkBody(t, "pre-release", render("en", "0.44.0-alpha.4", release("0.44.0")), nil, []string{
		`<i class="mk" aria-hidden="true"></i>0.44.0 is out<span class="tc" aria-hidden="true">This build is the pre-release 0.44.0-alpha.4, and the stable release 0.44.0 came out on Dec 1, 2026. Links to the release notes.</span>`,
	})
	for name, body := range map[string]string{
		"one minor":  render("ru", "0.44.0", release("0.45.0")),
		"dev":        render("ru", "dev", release("0.46.0")),
		"no release": render("ru", "0.44.0", latest.Release{}),
	} {
		checkBody(t, name, body, []string{`class="tt upd"`}, []string{`class="tt vl"`})
	}
}
