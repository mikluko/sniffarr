package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"sarak.sh/mikluko/libarr/version"
)

// notFound returns the body of a 404 answer, the page in full.
func notFound(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	require.Equal(t, http.StatusNotFound, w.Code)
	body := w.Body.String()
	require.True(t, strings.HasPrefix(body, "<!doctype html>"), "the 404 page in full")
	require.NotContains(t, body, "nf.", "every key is translated")
	require.Equal(t, "text/html; charset=utf-8", w.Header().Get("Content-Type"))
	return body
}

func TestNotFound(t *testing.T) {
	h := handler(t, nil)
	body := notFound(t, get(t, h, "/stats/old", ""))
	require.Contains(t, body, `<span class="code">404</span>`)
	require.Contains(t, body, `<h1>Henlo hetre</h1>`)
	require.Contains(t, body, `<span class="val">/stats/old</span>`)
	require.Contains(t, body, `<a class="btn" href="/">Home</a>`)
	require.NotContains(t, body, `aria-current="page"`, "no tab is chosen")
	require.Contains(t, body, "This product uses the TMDB API")
	require.Contains(t, body, `href="https://sarak.sh/mikluko/sniffarr"`, "the footer links the project page")
	require.Contains(t, body, ">sniffarr "+version.Get().Version+"<span", "the footer names the build")

	body = notFound(t, get(t, h, "/stats/old?lang=ru", ""))
	require.Contains(t, body, `<h1>Дратути</h1>`)
	require.Contains(t, body, `Этой страницы здесь нет.`)
	require.Contains(t, body, `<a class="btn" href="/">На главную</a>`)

	body = notFound(t, get(t, h, "/%3Cscript%3Ealert(1)%3C/script%3E", ""))
	require.Contains(t, body, `<span class="val">/&lt;script&gt;alert(1)&lt;/script&gt;</span>`, "the path is text")
	require.NotContains(t, body, "<script>alert")

	notFound(t, get(t, h, "/data", "data"))
	notFound(t, get(t, h, "/movie/m348", ""))
	notFound(t, get(t, h, "/w/eTphbGllbiAxOTc5", ""))
	notFound(t, get(t, h, "/t/xyz", ""))
	require.Equal(t, http.StatusNotFound, post(t, h, "/t/"+hexOf(1)+"/seed", "act-seed").Code)
}
