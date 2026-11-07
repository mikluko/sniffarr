package web

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// headerOf is the header of a full page.
func headerOf(t *testing.T, body string) string {
	t.Helper()
	i, j := strings.Index(body, "<header"), strings.Index(body, "</header>")
	require.Positive(t, i, "a header")
	require.Greater(t, j, i)
	return body[i:j]
}

func TestHeaderTabs(t *testing.T) {
	h := handler(t, nil)
	const torrents, top = `<a href="/torrents" class="on" aria-current="page">`, `<a href="/top" class="on" aria-current="page">`
	for _, c := range []struct{ path, on string }{
		{"/", ""},
		{"/torrents", torrents},
		{"/top", top},
		{"/movie/348", ""},
		{"/tv/95396", ""},
		{"/t/" + hexOf(1), torrents},
		{"/t/" + hexOf(7) + "?lang=ru", torrents},
		{"/stats/old", ""},
	} {
		w := get(t, h, c.path, "")
		body := w.Body.String()
		hd := headerOf(t, body)
		require.Contains(t, hd, `<a href="/torrents"`, c.path)
		require.Contains(t, hd, `<a href="/top"`, c.path)
		require.Equal(t, strings.Count(c.on, "class"), strings.Count(hd, `class="on"`), c.path, "no tab selected, or only its own")
		if c.on != "" {
			require.Contains(t, hd, c.on, c.path)
		}
	}
	hd := headerOf(t, get(t, h, "/", "").Body.String())
	require.Contains(t, hd, `<a href="/" class="brand" aria-current="page">sniffarr`, "the logo is the way home")
	require.NotContains(t, hd, "Data", "the data page is no tab")
	require.NotContains(t, headerOf(t, get(t, h, "/top", "").Body.String()), `aria-current="page">sniffarr`, "the logo marks only the data page")
}

func TestWorkBack(t *testing.T) {
	h := handler(t, nil)
	const movie, series = "/movie/348", "/tv/95396"
	hash := hexOf(1)
	for _, c := range []struct {
		name, path string
		header     []string
		back       string // the link, "" for none
	}{
		{"unknown", movie, nil, ""},
		{"from the top", movie + "?from=%2Ftop%3Fkind%3Dmovie%26win%3D30d", nil, `<a href="/top?kind=movie&amp;win=30d">← Top movies</a>`},
		{"from the top of series", series + "?from=%2Ftop%3Fkind%3Dseries", nil, `<a href="/top?kind=series">← Top series</a>`},
		{"from the torrents", movie + "?from=%2Ftorrents%3Fq%3Dalien%26kind%3Dmovie", nil, `<a href="/torrents?q=alien&amp;kind=movie">← Back to results</a>`},
		{"from a card", movie + "?from=%2Ft%2F" + hash, nil, `<a href="/t/` + hash + `">← Back to the release</a>`},
		{"referer, the top", movie, []string{"Referer", "http://example.com/top?kind=movie"}, `<a href="/top?kind=movie">← Top movies</a>`},
		{"referer, the torrents", movie, []string{"Referer", "http://example.com/torrents?q=alien"}, `<a href="/torrents?q=alien">← Back to results</a>`},
		{"referer, a card", movie, []string{"Referer", "http://example.com/t/" + hash}, `<a href="/t/` + hash + `">← Back to the release</a>`},
		{"from wins over the referer", movie + "?from=%2Ftorrents", []string{"Referer", "http://example.com/top"}, `<a href="/torrents">← Back to results</a>`},
		{"foreign referer", movie, []string{"Referer", "http://evil.example/top?kind=movie"}, ""},
		{"foreign referer, from", movie + "?from=%2Ftop", []string{"Referer", "http://evil.example/torrents"}, `<a href="/top">← Top movies</a>`},
		{"referer, the data page", movie, []string{"Referer", "http://example.com/"}, ""},
		{"referer, another title", movie, []string{"Referer", "http://example.com/tv/95396"}, ""},
		{"from with a host", movie + "?from=https%3A%2F%2Fevil.example%2Ftop", nil, ""},
		{"from, scheme-relative", movie + "?from=%2F%2Fevil.example%2Ftop", nil, ""},
		{"from, a backslash", movie + "?from=%2F%5Cevil.example%2Ftop", nil, ""},
		{"from, a title", movie + "?from=%2Ftv%2F95396", nil, ""},
		{"from, not an infohash", movie + "?from=%2Ft%2Fnothex", nil, ""},
		{"from, javascript", movie + "?from=javascript%3Aalert(1)", nil, ""},
	} {
		body := page(t, get(t, h, c.path, "", c.header...))
		if c.back == "" {
			require.NotContains(t, body, `class="head"`, c.name)
			continue
		}
		require.Contains(t, body, `<div class="head">
  `+c.back+`
</div>`, c.name)
	}
}

func TestWorkBackKept(t *testing.T) {
	h := handler(t, nil)
	body := page(t, get(t, h, "/movie/348?from=%2Ftop%3Fkind%3Dmovie&quality=1080p", ""))
	require.Contains(t, body, `<input type="hidden" name="from" value="/top?kind=movie">`, "the filter form carries it")
	require.Contains(t, body, `href="/movie/348?from=%2Ftop%3Fkind%3Dmovie&amp;quality=2160p"`, "a segment keeps it")
	require.Contains(t, body, `<a href="/movie/348?from=%2Ftop%3Fkind%3Dmovie" hx-get="/movie/348?from=%2Ftop%3Fkind%3Dmovie" hx-target="#releases" hx-swap="morph:outerHTML" hx-push-url="true" class="fx">Reset filters</a>`, "so does the reset")

	body = page(t, get(t, h, "/movie/348?quality=1080p", "", "Referer", "http://example.com/torrents?q=alien"))
	require.Contains(t, body, `<input type="hidden" name="from" value="/torrents?q=alien">`, "what the referer named goes on in the page's own links")

	frag := fragment(t, get(t, h, "/movie/348?from=%2Ftop&quality=1080p", "releases", "Referer", "http://example.com/movie/348"), "releases")
	require.Contains(t, frag, `<input type="hidden" name="from" value="/top">`, "a fragment too")
}

func TestLinksToWorkCarryTheirPage(t *testing.T) {
	h := handler(t, nil)
	require.Contains(t, page(t, get(t, h, "/top?kind=movie&win=30d&lang=en", "")), `<a href="/movie/348?from=%2Ftop%3Fkind%3Dmovie%26win%3D30d">Alien</a>`, "the top's page, but not its language")
	require.Contains(t, page(t, get(t, h, "/torrents?q=alien", "", "Referer", "http://example.com/top")), `href="/movie/348?from=%2Ftorrents%3Fq%3Dalien"`)
	require.Contains(t, page(t, get(t, h, "/t/"+hexOf(1)+"?lang=ru", "")), `href="/movie/348?from=%2Ft%2F`+hexOf(1)+`"`)
}
