package web

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/audio"
	"github.com/mikluko/sniffarr/internal/auth"
	"github.com/mikluko/sniffarr/internal/database"
	"github.com/mikluko/sniffarr/internal/memconn"
	"github.com/mikluko/sniffarr/internal/names"
	"github.com/mikluko/sniffarr/internal/rules"
	"github.com/mikluko/sniffarr/internal/search"
	"github.com/mikluko/sniffarr/internal/sketch"
	"github.com/mikluko/sniffarr/internal/store"
)

var now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func hash(b byte) store.Hash { return store.Hash{b} }

func hexOf(b byte) string {
	h := hash(b)
	return hex.EncodeToString(h[:])
}

// Names of the seeded releases, and the UI ids of their works.
const (
	alien     = "Alien.1979.Rus.1080p.BluRay"
	severance = "Severance.S02.1080p.WEB-DL"
)

var (
	alienID     = workID(names.Parse(alien).Work)
	severanceID = workID(names.Parse(severance).Work)
)

// peers returns a sketch of n peers numbered from first.
func peers(k sketch.Key, first, n int) []byte {
	s := sketch.New()
	for i := first; i < first+n; i++ {
		s.Add(k.Hash(netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}), 6881)))
	}
	return s.Bytes()
}

// handler serves, with l, the UI over an in-process browse API whose store
// holds a Russian-marked movie with ten files and 100 peers, a classified
// series with 30 peers now and 40 the day before, a torrent of unknown kind
// with 10 peers and an adult one, with the tops computed at now.
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
		{Hash: hash(1), Name: alien, Size: 55 << 30, Files: files, Discovery: "announce", SeenAt: now.Add(-time.Hour),
			Kind: "movie", KindLayer: store.LayerRules, Audio: []store.Audio{{Language: "ru", Marker: true, Probability: new(1.0), Layer: store.LayerRules}}},
		{Hash: hash(2), Name: severance, Size: 40 << 30, Discovery: "sample", SeenAt: now.Add(-30 * time.Hour),
			Audio: []store.Audio{{Language: "ru"}}},
		{Hash: hash(3), Name: "Сборник", Size: 1 << 30, Discovery: "sample", SeenAt: now.Add(-2 * time.Hour),
			Audio: []store.Audio{{Language: "ru", ScriptHint: true}}},
		{Hash: hash(4), Name: "Hidden.Thing", Size: 1 << 30, Discovery: "sample", SeenAt: now.Add(-time.Minute), Kind: "adult", KindLayer: store.LayerRules},
	} {
		p := names.Parse(tr.Name)
		tr.TitleKey, tr.WorkKey, tr.Year, tr.Season = p.Key, p.Work, p.Year, p.Season
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
	k, err := st.HashKey(ctx)
	require.NoError(t, err)
	_, _, err = st.MergePeers(ctx, []store.PeerSketch{
		{Hash: hash(1), Hour: now, Sketch: peers(k, 0, 100)},
		{Hash: hash(2), Hour: now, Sketch: peers(k, 0, 30)},
		{Hash: hash(2), Hour: now.Add(-30 * time.Hour), Sketch: peers(k, 1000, 40)},
		{Hash: hash(3), Hour: now, Sketch: peers(k, 0, 10)},
	})
	require.NoError(t, err)
	require.NoError(t, st.ComputeTop(ctx, now))
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

var untranslated = regexp.MustCompile(`\b(?:kind|layer|source|drop|voiceband|voicemode|script|via|d|card|col|home|sort|nav|f|top|topkind|win|results)\.[a-z_]+\b`)

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
	require.Contains(t, body, `<a href="/top?kind=movie">`)
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
	require.Less(t, strings.Index(body, "Alien.1979"), strings.Index(body, "Severance"), "most peers first")
	require.Less(t, strings.Index(body, "Severance"), strings.Index(body, "Сборник"))
	require.NotContains(t, body, "hx-trigger=\"every", "no auto-refresh")
	require.Contains(t, body, `<a href="/top" class="reset">`)
	require.NotContains(t, body, `<div class="works">`, "no works without a query")

	frag := page(t, get(t, h, "/?kind=series", "results"))
	require.True(t, strings.HasPrefix(strings.TrimSpace(frag), `<section id="results"`))
	require.Contains(t, frag, "Severance")
	require.NotContains(t, frag, "Alien")
	require.Contains(t, frag, "0.90", "a classifier's confidence")
	require.Regexp(t, `<td class="r pe" data-l="peers">\d\d</td>`, frag, "its peers over 7 days")
	require.Contains(t, page(t, get(t, h, "/?q=Сборник", "results")), `<td class="r pe" data-l="peers">10</td>`)

	frag = page(t, get(t, h, "/?q=alien", "results"))
	require.Contains(t, frag, `<div class="works">`)
	require.Contains(t, frag, `<a href="/top/w/eTphbGllbiAxOTc5?open=1">Alien (1979)</a>`, "the work by title and year, opening its releases")
	require.Contains(t, frag, "№1", "its place in the movie top")
	require.NotContains(t, page(t, get(t, h, "/?q=alien&o=50", "results")), `<div class="works">`, "works on the first page only")
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
	require.Equal(t, "/?kind=movie", searchURL("", s0.filters(map[string][]string{"kind": {"movie"}, "source": {"bogus"}, "sort": {"peers"}}), 0, "ru"), "invalid and default values are dropped")
	require.Equal(t, "/?sort=size", searchURL("", s0.filters(map[string][]string{"sort": {"size"}}), 0, "ru"))
}

func TestSort(t *testing.T) {
	h := handler(t, nil)
	body := page(t, get(t, h, "/?q=&sort=size", ""))
	require.Contains(t, body, `<option value="size" selected>`)
	require.NotContains(t, body, `<option value="peers" selected>`)
	require.Less(t, strings.Index(body, "Alien.1979"), strings.Index(body, "Severance"), "largest first")
	body = page(t, get(t, h, "/?q=&sort=last_seen", ""))
	require.Contains(t, body, `<option value="last_seen" selected>`)
	require.Less(t, strings.Index(body, "Сборник"), strings.Index(body, "Severance"), "latest first")
	body = page(t, get(t, h, "/?q=alien", ""))
	require.Contains(t, body, `<option value="peers" selected>`, "the default")
	body = page(t, get(t, h, "/?q=alien&sort=bogus", ""))
	require.Contains(t, body, `<option value="peers" selected>`, "an unknown sort is the default")
	require.Equal(t, "peers", s0.filters(map[string][]string{"sort": {"bogus"}}).Sort)
	require.Equal(t, "size", s0.filters(map[string][]string{"sort": {"size"}}).Sort)
}

func TestTop(t *testing.T) {
	h := handler(t, nil)
	body := page(t, get(t, h, "/top", ""))
	require.True(t, strings.HasPrefix(body, "<!doctype html>"))
	require.Contains(t, body, `aria-current="page">movies</a>`, "movies without a kind")
	require.Contains(t, body, `<tbody id="work-`+alienID+`">`)
	require.Contains(t, body, `>Alien</a> <span class="mu">1979</span>`, "a movie's title and year")
	require.Contains(t, body, `<td class="r mu">1</td>`, "its place")
	require.Contains(t, body, `<td class="r dl">new</td>`, "no peers the week before")
	require.Contains(t, body, `>explicit</td>`, "the marker of its release")
	require.NotContains(t, body, "Severance")
	require.Contains(t, body, `href="/top/w/`+alienID+`?open=1&amp;win=7d"`, "the toggle opens")
	require.Contains(t, body, `hx-target="#work-`+alienID+`"`)
	require.Contains(t, body, ">show all 100</a>", "the first 25 of 100")
	require.Contains(t, body, "Oct 20 12:00", "computed at now")
	require.NotContains(t, body, `class="open"`)

	body = page(t, get(t, h, "/top?kind=series&win=24h&voice=p50&all=1", ""))
	require.Contains(t, body, `aria-current="page">series</a>`)
	require.Contains(t, body, `>Severance</a> <span class="mu">S02</span>`, "a series' title and season")
	require.Contains(t, body, `href="/top?all=1&amp;kind=series&amp;voice=p50&amp;win=24h" hx-get="/top?all=1&amp;kind=series&amp;voice=p50&amp;win=24h" hx-target="#top" hx-swap="morph:outerHTML" hx-push-url="true" aria-current="true">24 h</a>`)
	require.Regexp(t, `<td class="r dl">−\d+%</td>`, body, "fewer peers than the day before")
	require.Contains(t, body, "WEB", "its source")
	require.NotContains(t, body, ">show all 100</a>", "every work is listed")

	body = page(t, get(t, h, "/top?kind=music", ""))
	require.Contains(t, body, "Empty: the top has not been computed yet.")

	frag := page(t, get(t, h, "/top?kind=series", "top"))
	require.True(t, strings.HasPrefix(strings.TrimSpace(frag), `<section id="top"`))
	require.Contains(t, frag, "Severance")

	for _, tt := range []struct{ path, want string }{
		{"/top?kind=book", `aria-current="page">movies</a>`},
		{"/top?kind=", `aria-current="page">movies</a>`},
		{"/top?win=1y", `aria-current="true">7 days</a>`},
		{"/top?voice=loud", `href="/top?kind=movie&amp;win=7d" hx-get="/top?kind=movie&amp;win=7d" hx-target="#top" hx-swap="morph:outerHTML" hx-push-url="true" aria-current="true">any</a>`},
		{"/top?all=yes", ">show all 100</a>"},
	} {
		require.Contains(t, page(t, get(t, h, tt.path, "")), tt.want, "%s falls back to the default", tt.path)
	}
}

func TestWork(t *testing.T) {
	h := handler(t, nil)
	frag := page(t, get(t, h, "/top/w/"+alienID+"?open=1", "work-"+alienID))
	require.True(t, strings.HasPrefix(frag, `<tbody id="work-`+alienID+`" class="open">`), "the tbody alone: %s", frag)
	require.Contains(t, frag, `<td class="r mu">1</td>`, "its place in the top")
	require.Contains(t, frag, `<td class="r dl">new</td>`)
	require.Regexp(t, `<td class="r pe" data-l="peers">\d+</td>`, frag)
	require.Contains(t, frag, `<a href="/t/`+hexOf(1)+`">`+alien+`</a>`, "its release")
	require.Contains(t, frag, `href="/top/w/`+alienID+`?win=7d"`, "the toggle closes")
	require.Contains(t, frag, `aria-expanded="true"`)

	frag = page(t, get(t, h, "/top/w/"+alienID+"?win=7d", "work-"+alienID))
	require.True(t, strings.HasPrefix(frag, `<tbody id="work-`+alienID+`">`))
	require.NotContains(t, frag, hexOf(1), "closed")
	require.Contains(t, frag, `aria-expanded="false"`)

	frag = page(t, get(t, h, "/top/w/"+severanceID+"?open=1&win=24h&voice=p50", "work-"+severanceID))
	require.Regexp(t, `<td class="r dl">−\d+%</td>`, frag, "the change over the window asked for")
	require.Contains(t, frag, `href="/top/w/`+severanceID+`?voice=p50&amp;win=24h"`)
	require.Contains(t, frag, severance)
	frag = page(t, get(t, h, "/top/w/"+severanceID+"?open=1&voice=explicit", "work-"+severanceID))
	require.NotContains(t, frag, severance, "no release of the work has the marker")

	body := page(t, get(t, h, "/top/w/"+alienID+"?open=1", ""))
	require.True(t, strings.HasPrefix(body, "<!doctype html>"), "a full page without htmx")
	require.Contains(t, body, `aria-current="page">movies</a>`)
	require.Contains(t, body, alien)
	require.NotContains(t, body, "Severance")

	require.Equal(t, http.StatusNotFound, get(t, h, "/top/w/"+workID("y:nothing 2000"), "work-x").Code)
	require.Equal(t, http.StatusNotFound, get(t, h, "/top/w/%21%21", "").Code, "not an id")
}

var s0 = &server{tags: []string{"ru"}}

func TestTorrent(t *testing.T) {
	h := handler(t, nil)
	body := page(t, get(t, h, "/t/"+hexOf(1), "", "Referer", "http://example.com/?q=alien"))
	require.True(t, strings.HasPrefix(body, "<!doctype html>"))
	require.Contains(t, body, "magnet:?xt=urn:btih:"+hexOf(1))
	require.Contains(t, body, `<td class="r">Rus</td>`, "the matched word")
	require.Contains(t, body, `<a href="https://www.themoviedb.org/search?query=alien&#43;1979" rel="noreferrer" target="_blank">TMDB</a> · <a href="https://www.imdb.com/find/?q=alien&#43;1979" rel="noreferrer" target="_blank">IMDb</a>`, "searches when no ID is known")
	require.Regexp(t, `<th>peers</th><td>\d+ in 7 days <span class="mu">· \d+ in 24 h · \d+ in 30 days</span></td>`, body)
	require.Regexp(t, `<a href="/top/w/[A-Za-z0-9_-]+\?open=1">Alien \(1979\)</a> <span class="mu">· №1 among movies · 1 releases, this one by peers #1</span>`, body, "the work link opens the work's releases")
	require.NotContains(t, page(t, get(t, h, "/t/"+hexOf(3), "")), "<th>work</th>", "a release whose name yields no work")
	require.Equal(t, 8, strings.Count(body, `<td class="mono wrapc">`), "the first eight files")
	require.Contains(t, body, "?files=all")

	frag := page(t, get(t, h, "/t/"+hexOf(1)+"?files=all", "files"))
	require.True(t, strings.HasPrefix(strings.TrimSpace(frag), `<div id="files">`))
	require.Equal(t, 10, strings.Count(frag, `<td class="mono wrapc">`), "every stored file")

	require.Equal(t, http.StatusNotFound, get(t, h, "/t/"+hexOf(9), "").Code)
	require.Equal(t, http.StatusNotFound, get(t, h, "/t/xyz", "").Code)
}

func TestLookups(t *testing.T) {
	ids := []*sniffarrv1.ExternalId{{Provider: "tmdb", Id: "348"}, {Provider: "imdb", Id: "tt0078748"}}
	for _, tt := range []struct {
		name       string
		r          *sniffarrv1.Release
		tmdb, imdb string
	}{
		{"movie by ID", &sniffarrv1.Release{Name: "Alien.1979.1080p", Kind: sniffarrv1.Kind_KIND_MOVIE, Ids: ids},
			"https://www.themoviedb.org/movie/348", "https://www.imdb.com/title/tt0078748/"},
		{"series by ID", &sniffarrv1.Release{Name: "Severance S02E01", Kind: sniffarrv1.Kind_KIND_SERIES, Ids: ids},
			"https://www.themoviedb.org/tv/348", "https://www.imdb.com/title/tt0078748/"},
		{"search without IDs", &sniffarrv1.Release{Name: "Alien.1979.1080p", Kind: sniffarrv1.Kind_KIND_MOVIE},
			"https://www.themoviedb.org/search?query=alien+1979", "https://www.imdb.com/find/?q=alien+1979"},
		{"one ID, the other searched", &sniffarrv1.Release{Name: "Alien.1979.1080p", Ids: ids[:1]},
			"https://www.themoviedb.org/movie/348", "https://www.imdb.com/find/?q=alien+1979"},
		{"no title", &sniffarrv1.Release{Name: "1080p"}, "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tmdb, imdb := lookups(tt.r)
			require.Equal(t, tt.tmdb, tmdb)
			require.Equal(t, tt.imdb, imdb)
		})
	}
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
