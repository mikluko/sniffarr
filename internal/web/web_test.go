package web

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/audio"
	"github.com/mikluko/sniffarr/internal/auth"
	"github.com/mikluko/sniffarr/internal/database"
	"github.com/mikluko/sniffarr/internal/memconn"
	"github.com/mikluko/sniffarr/internal/rules"
	"github.com/mikluko/sniffarr/internal/search"
	"github.com/mikluko/sniffarr/internal/store"
)

var now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func hash(b byte) store.Hash { return store.Hash{b} }

func hexOf(b byte) string {
	h := hash(b)
	return hex.EncodeToString(h[:])
}

// handler serves, with l, the UI over an in-process browse API whose store
// holds a Russian-marked movie with ten files, a classified series, a torrent
// of unknown kind and an adult one.
func handler(t *testing.T, l Locales) http.Handler {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	st, err := store.New(ctx, db, store.Options{Languages: []string{"ru"}})
	require.NoError(t, err)
	var files []rules.File
	for i := range 10 {
		files = append(files, rules.File{Path: "Alien/part" + string(rune('a'+i)) + ".mkv", Size: int64(10-i) << 30})
	}
	for _, tr := range []store.Torrent{
		{Hash: hash(1), Name: "Alien.1979.Rus.1080p.BluRay", Size: 55 << 30, Files: files, Discovery: "announce", SeenAt: now.Add(-time.Hour),
			Kind: "movie", KindLayer: store.LayerRules, Audio: []store.Audio{{Language: "ru", Marker: true, Probability: new(1.0), Layer: store.LayerRules}}},
		{Hash: hash(2), Name: "Severance.S02.1080p.WEB-DL", Size: 40 << 30, Discovery: "sample", SeenAt: now.Add(-30 * time.Hour),
			Audio: []store.Audio{{Language: "ru"}}},
		{Hash: hash(3), Name: "Сборник", Size: 1 << 30, Discovery: "sample", SeenAt: now.Add(-2 * time.Hour),
			Audio: []store.Audio{{Language: "ru", ScriptHint: true}}},
		{Hash: hash(4), Name: "Hidden.Thing", Size: 1 << 30, Discovery: "sample", SeenAt: now.Add(-time.Minute), Kind: "adult", KindLayer: store.LayerRules},
	} {
		_, _, err := st.Insert(ctx, tr)
		require.NoError(t, err)
	}
	r, err := st.Get(ctx, hash(2))
	require.NoError(t, err)
	_, err = st.Classify(ctx, r.ID, store.Classification{Kind: "series", KindConfidence: 0.9, Source: "web", SourceConfidence: 0.8,
		Audio: map[string]float64{"ru": 0.7}}, now)
	require.NoError(t, err)
	_, err = st.Reject(ctx, hash(10), rules.Small, now)
	require.NoError(t, err)
	require.NoError(t, st.AddTokens(ctx, now.Format(time.DateOnly), 250))
	langs, err := audio.Parse("ru")
	require.NoError(t, err)
	h, err := New(Config{Browse: browse(t, st), Languages: langs, Locales: l, Location: time.UTC, Now: func() time.Time { return now }})
	require.NoError(t, err)
	return h
}

// browse serves BrowseService over st behind the token "search" in-process,
// and returns a client holding the token.
func browse(t *testing.T, st *store.Store) sniffarrv1connect.BrowseServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	b := &search.Browser{Store: st, Log: slog.New(slog.DiscardHandler), DailyTokens: 1000, Now: func() time.Time { return now }}
	mux.Handle(sniffarrv1connect.NewBrowseServiceHandler(b, connect.WithInterceptors(auth.NewBearer("search"))))
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	srv := &http.Server{Handler: mux, Protocols: &p}
	l := memconn.Listen()
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { require.NoError(t, srv.Shutdown(context.Background())) })
	return sniffarrv1connect.NewBrowseServiceClient(l.Client(), memconn.URL, connect.WithInterceptors(auth.Client("search")))
}

// get serves the path, as htmx asking for target when it is not "".
func get(t *testing.T, h http.Handler, path, target string, header ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if target != "" {
		req.Header.Set("HX-Request", "true")
		req.Header.Set("HX-Target", target)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

var untranslated = regexp.MustCompile(`\b(?:kind|layer|source|drop|voiceband|voicemode|script|via|d|card|col|home|sort|nav|f)\.[a-z_]+\b`)

func page(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := w.Body.String()
	require.Empty(t, untranslated.FindAllString(body, -1), "every key is translated")
	return body
}

func TestHome(t *testing.T) {
	h := handler(t, nil)
	body := page(t, get(t, h, "/", ""))
	require.True(t, strings.HasPrefix(body, "<!doctype html>"))
	require.Contains(t, body, `<div class="home">`)
	require.Contains(t, body, "<span>4 torrents</span>", "every stored release, adult ones too")
	require.Contains(t, body, "<span>+3 in 24 h</span>", "first seen within the last 24 hours")
	require.Contains(t, body, "<span>classifier pending: 1</span>")
	require.NotContains(t, body, "Alien.1979")
	require.NotContains(t, body, `href="/top`, "the top is not served yet")
	for _, path := range []string{"/?q=", "/?kind=movie", "/?vl=ru", "/?voice=any", "/?source=", "/?layer=", "/?min=", "/?sort=size", "/?o=50"} {
		require.Contains(t, page(t, get(t, h, path, "")), `id="results"`, "%s is a search", path)
	}
	require.NotContains(t, page(t, get(t, h, "/?lang=ru", "")), `id="results"`, "lang alone is not a search")
}

func TestSearch(t *testing.T) {
	h := handler(t, nil)
	body := page(t, get(t, h, "/?q=", ""))
	require.True(t, strings.HasPrefix(body, "<!doctype html>"))
	for _, name := range []string{"Alien.1979.Rus.1080p.BluRay", "Severance.S02.1080p.WEB-DL", "Сборник"} {
		require.Contains(t, body, name)
	}
	require.NotContains(t, body, "Hidden.Thing", "adult releases are hidden")
	require.Less(t, strings.Index(body, "Alien.1979"), strings.Index(body, "Сборник"), "latest first")
	require.NotContains(t, body, "hx-trigger=\"every", "no auto-refresh")
	require.NotContains(t, body, `href="/top`, "the top is not served yet")
	require.Contains(t, body, `<a href="/" class="reset">`)

	frag := page(t, get(t, h, "/?kind=series", "results"))
	require.True(t, strings.HasPrefix(strings.TrimSpace(frag), `<section id="results"`))
	require.Contains(t, frag, "Severance")
	require.NotContains(t, frag, "Alien")
	require.Contains(t, frag, "0.90", "a classifier's confidence")
}

func TestSearchFilters(t *testing.T) {
	h := handler(t, nil)
	for _, tt := range []struct {
		path string
		want []string
		not  []string
	}{
		{path: "/?q=alien", want: []string{"Alien"}, not: []string{"Severance", "Сборник"}},
		{path: "/?kind=unknown", want: []string{"Сборник"}, not: []string{"Alien", "Severance"}},
		{path: "/?vl=ru&voice=explicit", want: []string{"Alien"}, not: []string{"Severance", "Сборник"}},
		{path: "/?vl=ru&voice=p50", want: []string{"Alien", "Severance"}, not: []string{"Сборник"}},
		{path: "/?source=web", want: []string{"Severance"}, not: []string{"Alien", "Сборник"}},
		{path: "/?layer=classifier", want: []string{"Severance"}, not: []string{"Alien", "Сборник"}},
		{path: "/?min=41,5", want: []string{"Alien"}, not: []string{"Severance", "Сборник"}},
	} {
		t.Run(tt.path, func(t *testing.T) {
			body := page(t, get(t, h, tt.path, "results"))
			for _, s := range tt.want {
				require.Contains(t, body, s)
			}
			for _, s := range tt.not {
				require.NotContains(t, body, s)
			}
		})
	}
}

func TestSearchURL(t *testing.T) {
	require.Equal(t, "/?q=", searchURL("", s0.filters(nil), 0, "ru"), "a search, not the home page")
	require.Equal(t, "/?kind=movie&o=50&q=alien&voice=p50", searchURL("alien", s0.filters(map[string][]string{"kind": {"movie"}, "voice": {"p50"}, "vl": {"ru"}}), 50, "ru"))
	require.Equal(t, "/?kind=movie", searchURL("", s0.filters(map[string][]string{"kind": {"movie"}, "source": {"bogus"}, "sort": {"last_seen"}}), 0, "ru"), "invalid and default values are dropped")
	require.Equal(t, "/?sort=size", searchURL("", s0.filters(map[string][]string{"sort": {"size"}}), 0, "ru"))
}

func TestSort(t *testing.T) {
	h := handler(t, nil)
	body := page(t, get(t, h, "/?q=&sort=size", ""))
	require.Contains(t, body, `<option value="size" selected>`)
	require.NotContains(t, body, `<option value="last_seen" selected>`)
	body = page(t, get(t, h, "/?q=alien", ""))
	require.Contains(t, body, `<option value="last_seen" selected>`, "the default")
	body = page(t, get(t, h, "/?q=alien&sort=peers", ""))
	require.Contains(t, body, `<option value="last_seen" selected>`, "an unknown sort is the default")
	require.Equal(t, "last_seen", s0.filters(map[string][]string{"sort": {"peers"}}).Sort)
	require.Equal(t, "size", s0.filters(map[string][]string{"sort": {"size"}}).Sort)
}

var s0 = &server{tags: []string{"ru"}}

func TestTorrent(t *testing.T) {
	h := handler(t, nil)
	body := page(t, get(t, h, "/t/"+hexOf(1), "", "Referer", "http://example.com/?q=alien"))
	require.True(t, strings.HasPrefix(body, "<!doctype html>"))
	require.Contains(t, body, "magnet:?xt=urn:btih:"+hexOf(1))
	require.Contains(t, body, `<td class="r">Rus</td>`, "the matched word")
	require.Contains(t, body, "alien 1979", "the TMDB query")
	require.Equal(t, 8, strings.Count(body, `<td class="mono wrapc">`), "the first eight files")
	require.Contains(t, body, "?files=all")

	frag := page(t, get(t, h, "/t/"+hexOf(1)+"?files=all", "files"))
	require.True(t, strings.HasPrefix(strings.TrimSpace(frag), `<div id="files">`))
	require.Equal(t, 10, strings.Count(frag, `<td class="mono wrapc">`), "every stored file")

	require.Equal(t, http.StatusNotFound, get(t, h, "/t/"+hexOf(9), "").Code)
	require.Equal(t, http.StatusNotFound, get(t, h, "/t/xyz", "").Code)
}

func TestData(t *testing.T) {
	h := handler(t, nil)
	body := page(t, get(t, h, "/data", ""))
	require.True(t, strings.HasPrefix(body, "<!doctype html>"))
	require.Contains(t, body, `<a href="/?kind=movie">`)
	require.Contains(t, body, "25.0%", "tokens used of the daily limit")
	frag := page(t, get(t, h, "/data", "data"))
	require.True(t, strings.HasPrefix(strings.TrimSpace(frag), `<main id="data"`))
	require.Equal(t, http.StatusOK, get(t, h, "/static/vendor/htmx.min.js", "").Code)
}

// locales knows the token "ru-token" as a person with the locale ru.
type locales struct{}

func (locales) Locale(_ context.Context, token string) (string, error) {
	if token == "ru-token" {
		return "ru", nil
	}
	return "", errors.New("unknown token")
}

func TestLanguage(t *testing.T) {
	h := handler(t, nil)
	require.Contains(t, page(t, get(t, h, "/data", "", TokenHeader, "ru-token")), `<html lang="en">`, "no lookup without Locales")
	h = handler(t, locales{})
	require.Contains(t, page(t, get(t, h, "/data", "")), `<html lang="en">`)
	require.Contains(t, page(t, get(t, h, "/data", "", TokenHeader, "ru-token")), `<html lang="ru">`, "the person's locale")
	require.Contains(t, page(t, get(t, h, "/data", "", TokenHeader, "other")), `<html lang="en">`, "a failed lookup falls back")
	require.Contains(t, page(t, get(t, h, "/data?lang=C", "", TokenHeader, "ru-token")), `<html lang="en">`, "the language picked wins")

	w := get(t, h, "/?lang=ru", "")
	require.Contains(t, page(t, w), `<html lang="ru">`)
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "lang" {
			cookie = c
		}
	}
	require.NotNil(t, cookie, "the pick is remembered")
	require.Equal(t, "ru", cookie.Value)
	require.Equal(t, 365*24*3600, cookie.MaxAge, "for a year")
	require.Contains(t, page(t, get(t, h, "/", "", "Cookie", "lang=ru")), `<html lang="ru">`, "the cookie")
	require.Contains(t, page(t, get(t, h, "/", "", "Cookie", "lang=en", TokenHeader, "ru-token")), `<html lang="en">`, "the cookie beats the person's locale")
	require.Contains(t, page(t, get(t, h, "/?lang=en", "", "Cookie", "lang=ru")), `<html lang="en">`, "the pick beats the cookie")
	require.Empty(t, get(t, h, "/", "", "Cookie", "lang=ru").Result().Cookies(), "no cookie without a pick")
}
