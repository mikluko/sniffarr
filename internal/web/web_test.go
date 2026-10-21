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
	"google.golang.org/protobuf/types/known/timestamppb"

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
	"github.com/mikluko/sniffarr/internal/ui"
)

var now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func hash(b byte) store.Hash { return store.Hash{b} }

func hexOf(b byte) string {
	h := hash(b)
	return hex.EncodeToString(h[:])
}

// Names of the seeded releases.
const (
	alien      = "Alien.1979.Rus.1080p.BluRay"
	chuzhoy    = "Чужой (1979) BDRip 720p"
	severance  = "Severance.S02E05.1080p.WEB-DL"
	season1    = "Severance.S01.2160p.WEB-DL"
	ronin      = "Ronin.1998.1080p.BluRay"
	narcissist = "Narcissists.Playbook.2026.1080p.WEB-DL"
	heat       = "Heat.1995.1080p.BluRay"
)

// alienDetails are Alien's details, with texts in both languages.
var alienDetails = store.Details{Type: "movie", OriginalTitle: "Alien", Year: 1979, PosterPath: "/p.jpg", Rating: 8.176, Votes: 16995, Runtime: 117,
	Countries: []string{"US"}, Language: "en", Texts: map[string]store.Text{
		"en": {Title: "Alien", Overview: "Nostromo intercepts a distress signal.", Genres: []string{"Horror", "Science Fiction"}, PosterPath: "/p.jpg"},
		"ru": {Title: "Чужой", Overview: "Корабль перехватывает сигнал.", Genres: []string{"ужасы", "фантастика"}, PosterPath: "/r.jpg"},
	}}

// peers returns a sketch of n peers numbered from first.
func peers(k sketch.Key, first, n int) []byte {
	s := sketch.New()
	for i := first; i < first+n; i++ {
		s.Add(k.Hash(netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}), 6881)))
	}
	return s.Bytes()
}

// titleSearch answers TMDB searches for "alien" with Alien and Aliens.
type titleSearch struct{ store *store.Store }

func (f titleSearch) SearchWorks(ctx context.Context, typ, query, lang string) ([]int64, error) {
	if typ != store.TypeMovie || query != "alien" {
		return nil, nil
	}
	return f.store.RecordSearch(ctx, typ, query, []store.Hit{
		{Type: typ, TMDBID: 348, Lang: lang, Title: "Alien", Popularity: 40},
		{Type: typ, TMDBID: 679, Lang: lang, Title: "Aliens", OriginalTitle: "Aliens", Year: 1986, PosterPath: "/a.jpg", Popularity: 30},
	}, now)
}

// handler serves, with l, the UI over an in-process browse API whose store
// holds Alien under two title keys, matched with details and a poster, with
// a Russian marker on one; a Severance episode and season pack, matched;
// Ronin, matched without a poster; a movie looked up and not found; Heat,
// never looked up; a torrent of unknown kind, an album and an adult one;
// with peers and the tops computed at now.
func handler(t *testing.T, l Locales) http.Handler {
	t.Helper()
	return handlerLangs(t, l, "ru")
}

// handlerLangs is handler with the audio languages langs, a comma-separated
// list, in the store and the UI alike; "" leaves the extension off.
func handlerLangs(t *testing.T, l Locales, langs string) http.Handler {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	languages, err := audio.Parse(langs)
	require.NoError(t, err)
	st, err := store.New(ctx, db, store.Options{Languages: audio.Tags(languages), TMDB: true})
	require.NoError(t, err)
	var files []rules.File
	for i := range 10 {
		files = append(files, rules.File{Path: "Alien/part" + string(rune('a'+i)) + ".mkv", Size: int64(10-i) << 30})
	}
	ru := func(a store.Audio) []store.Audio {
		a.Language = "ru"
		return []store.Audio{a}
	}
	for _, tr := range []store.Torrent{
		{Hash: hash(1), Name: alien, Size: 55 << 30, Files: files, Discovery: "announce", SeenAt: now.Add(-time.Hour), Kind: "movie", KindLayer: store.LayerRules,
			Audio: ru(store.Audio{Marker: true, Mark: "Rus", Probability: new(1.0), Layer: store.LayerRules})},
		{Hash: hash(2), Name: chuzhoy, Size: 8 << 30, Discovery: "sample", SeenAt: now.Add(-2 * time.Hour), Kind: "movie", KindLayer: store.LayerRules,
			Audio: ru(store.Audio{ScriptHint: true})},
		{Hash: hash(3), Name: severance, Size: 3 << 30, Discovery: "sample", SeenAt: now.Add(-30 * time.Hour), Kind: "series", KindLayer: store.LayerRules, Audio: ru(store.Audio{})},
		{Hash: hash(4), Name: season1, Size: 40 << 30, Discovery: "sample", SeenAt: now.Add(-40 * time.Hour), Kind: "series", KindLayer: store.LayerRules, Audio: ru(store.Audio{})},
		{Hash: hash(5), Name: ronin, Size: 9 << 30, Discovery: "sample", SeenAt: now.Add(-3 * time.Hour), Kind: "movie", KindLayer: store.LayerRules},
		{Hash: hash(6), Name: narcissist, Size: 3 << 30, Discovery: "sample", SeenAt: now.Add(-4 * time.Hour), Kind: "movie", KindLayer: store.LayerRules},
		{Hash: hash(7), Name: heat, Size: 2 << 30, Discovery: "sample", SeenAt: now.Add(-50 * time.Hour), Kind: "movie", KindLayer: store.LayerRules},
		{Hash: hash(8), Name: "Сборник", Size: 1 << 30, Discovery: "sample", SeenAt: now.Add(-5 * time.Hour), Audio: ru(store.Audio{ScriptHint: true})},
		{Hash: hash(9), Name: "Moon Safari 1998 FLAC", Size: 1 << 30, Discovery: "sample", SeenAt: now.Add(-6 * time.Hour), Kind: "music", KindLayer: store.LayerRules},
		{Hash: hash(10), Name: "Hidden.Thing", Size: 1 << 30, Discovery: "sample", SeenAt: now.Add(-time.Minute), Kind: "adult", KindLayer: store.LayerRules},
	} {
		p := names.Parse(tr.Name)
		tr.TitleKey, tr.Year, tr.Season, tr.Episode, tr.Quality = p.Key, p.Year, p.Season, p.Episode, p.Quality
		_, _, err := st.Insert(ctx, tr)
		require.NoError(t, err)
	}
	match := func(name, typ string, id int, d *store.Details, ids []store.ExternalID, layer string) {
		w, _, err := st.RecordMatch(ctx, names.Parse(name).Key, store.Found{Hit: &store.Hit{Type: typ, TMDBID: id}, Confidence: 0.8, Layer: layer}, now.Add(-time.Hour))
		require.NoError(t, err)
		if d != nil {
			require.NoError(t, st.RecordDetails(ctx, w, *d, ids, now))
		}
	}
	match(alien, store.TypeMovie, 348, &alienDetails, []store.ExternalID{{Provider: store.ProviderIMDb, ID: "tt0078748"}}, store.LayerJev)
	match(chuzhoy, store.TypeMovie, 348, nil, nil, store.LayerTMDB)
	match(severance, store.TypeTV, 95396, &store.Details{Type: "tv", OriginalTitle: "Severance", Year: 2022, PosterPath: "/s.jpg", Status: "Returning Series", Seasons: 2,
		Texts: map[string]store.Text{"en": {Title: "Severance"}, "ru": {Title: "Разделение"}}}, nil, store.LayerTMDB)
	match(ronin, store.TypeMovie, 8195, &store.Details{Type: "movie", OriginalTitle: "Ronin", Year: 1998, Texts: map[string]store.Text{"en": {Title: "Ronin"}}}, nil, store.LayerTMDB)
	_, _, err = st.RecordMatch(ctx, names.Parse(narcissist).Key, store.Found{}, now.Add(-2*time.Hour))
	require.NoError(t, err)
	r, err := st.Get(ctx, hash(3))
	require.NoError(t, err)
	_, err = st.Classify(ctx, r.ID, store.Classification{Source: "web", SourceConfidence: 0.8, Audio: map[string]float64{"ru": 0.7}}, now)
	require.NoError(t, err)
	_, err = st.Reject(ctx, hash(20), rules.Small, now)
	require.NoError(t, err)
	require.NoError(t, st.AddTokens(ctx, now.Format(time.DateOnly), 250))
	k, err := st.HashKey(ctx)
	require.NoError(t, err)
	_, _, err = st.MergePeers(ctx, []store.PeerSketch{
		{Hash: hash(1), Hour: now, Sketch: peers(k, 0, 100)},
		{Hash: hash(2), Hour: now, Sketch: peers(k, 500, 20)},
		{Hash: hash(3), Hour: now, Sketch: peers(k, 0, 30)},
		{Hash: hash(3), Hour: now.Add(-30 * time.Hour), Sketch: peers(k, 1000, 40)},
		{Hash: hash(5), Hour: now, Sketch: peers(k, 0, 5)},
		{Hash: hash(8), Hour: now, Sketch: peers(k, 0, 10)},
	})
	require.NoError(t, err)
	require.NoError(t, st.ComputeTop(ctx, now))
	h, err := New(Config{Browse: browse(t, st, audio.Tags(languages)), Languages: languages, Locales: l, Location: time.UTC, Now: func() time.Time { return now }})
	require.NoError(t, err)
	return h
}

// browse serves BrowseService over st behind the token "search" in-process,
// and returns a client holding the token.
func browse(t *testing.T, st *store.Store, tags []string) sniffarrv1connect.BrowseServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	b := &search.Browser{Store: st, TMDB: titleSearch{st}, Log: slog.New(slog.DiscardHandler), DailyTokens: 1000, Languages: tags, Now: func() time.Time { return now }}
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

var untranslated = regexp.MustCompile(`\b(?:(?:kind|layer|source|drop|voiceband|voicemode|relvoice|relsort|quality|script|via|d|card|col|home|sort|nav|f|top|topkind|win|results|unit|attr|work|rel|search|matched|audio|dub|langname)\.[a-z_]+` +
	`|tmdb\.(?:votes|noposter|unmatched|unchecked|search|checked|tv|seasons|episode|method|low|key|conf)|tvstatus\.[A-Z][a-zA-Z ]*)\b`)

// ruleLine is a card's fired rule: its id is not a translation key.
var ruleLine = regexp.MustCompile(`<div class="mono">[a-z0-9.]+ ← «[^»]*»</div>`)

// page returns the body of an OK answer, failing on an untranslated key.
func page(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := w.Body.String()
	require.Empty(t, untranslated.FindAllString(ruleLine.ReplaceAllString(body, ""), -1), "every key is translated")
	return body
}

// fragment returns the body of an htmx answer for target, failing unless it
// is the fragment alone.
func fragment(t *testing.T, w *httptest.ResponseRecorder, target string) string {
	t.Helper()
	body := page(t, w)
	require.False(t, strings.HasPrefix(body, "<!doctype html>"), "not a full page")
	require.Contains(t, body, `id="`+target+`"`)
	return body
}

func TestHome(t *testing.T) {
	h := handler(t, nil)
	body := page(t, get(t, h, "/", ""))
	require.True(t, strings.HasPrefix(body, "<!doctype html>"))
	require.Contains(t, body, `<div class="home">`)
	require.Contains(t, body, "<span>10 torrents</span>", "every stored release, adult ones too")
	require.Contains(t, body, "<span>+7 in 24 h</span>", "first seen within the last 24 hours")
	require.Contains(t, body, "<span>classifier pending: 1</span>")
	require.Contains(t, body, `<a href="/top?kind=movie">`)
	require.Contains(t, body, `<a href="/torrents">`)
	require.Contains(t, body, `<img src="/static/vendor/tmdb.svg" alt="TMDB" height="12">`, "the attribution")
	require.Contains(t, body, "This product uses the TMDB API but is not endorsed or certified by TMDB.")
	require.Equal(t, http.StatusOK, get(t, h, "/static/vendor/tmdb.svg", "").Code)
	require.Contains(t, page(t, get(t, h, "/?q=", "")), `id="results"`, "q alone, empty, is a search")
	require.NotContains(t, page(t, get(t, h, "/?lang=ru", "")), `id="results"`, "lang alone is not a search")
}

func TestSearch(t *testing.T) {
	h := handler(t, nil)
	body := page(t, get(t, h, "/?q=alien", ""))
	require.True(t, strings.HasPrefix(body, "<!doctype html>"))
	require.Contains(t, body, `<a class="wrow-a" href="/w/m348">`, "ours, the merged Alien")
	require.Contains(t, body, `<li class="wrow only-tmdb">`, "found on TMDB only")
	require.Contains(t, body, `href="/w/m679"`)
	require.Less(t, strings.Index(body, "/w/m348"), strings.Index(body, "/w/m679"), "ours first")
	require.Contains(t, body, "<span>2 releases</span>", "both title keys")
	require.Contains(t, body, "ours: 1 · TMDB only: 1")
	require.Contains(t, body, `<img class="th th-l" src="https://image.tmdb.org/t/p/w92/p.jpg"`, "its poster")
	require.Contains(t, body, `<a href="/torrents?q=alien">`, "the same words among torrents")
	require.Contains(t, page(t, get(t, h, "/?q=alien&lang=ru", "")), `<span class="wrow-title">Чужой</span>`, "the title in the page's language")

	frag := fragment(t, get(t, h, "/?q=alien&kind=series", "results"), "results")
	require.Contains(t, frag, "Nothing found.", "no series by that name")
	require.Contains(t, frag, `aria-current="true">series</a>`)
	frag = fragment(t, get(t, h, "/?q=severance&kind=series", "results"), "results")
	require.Contains(t, frag, `href="/w/tv95396"`)
	frag = fragment(t, get(t, h, "/?q=", "results"), "results")
	require.Contains(t, frag, "Nothing found.")
}

func TestTorrents(t *testing.T) {
	h := handler(t, nil)
	body := page(t, get(t, h, "/torrents", ""))
	require.True(t, strings.HasPrefix(body, "<!doctype html>"))
	require.Contains(t, body, `aria-current="page">Torrents</a>`)
	for _, name := range []string{alien, chuzhoy, severance, season1, ronin, narcissist, heat, "Сборник", "Moon Safari 1998 FLAC"} {
		require.Contains(t, body, name)
	}
	require.NotContains(t, body, "Hidden.Thing", "adult releases are hidden")
	require.Less(t, strings.Index(body, alien), strings.Index(body, severance), "most peers first")
	require.Contains(t, body, `<a href="/w/m348" class="thl" title="Alien"><img class="th" src="https://image.tmdb.org/t/p/w92/p.jpg"`, "a matched row's poster leads to its work")
	require.Contains(t, body, `<a href="/w/m8195" class="thl" title="Ronin"><span class="th th-ph" aria-label="Ronin">—</span></a>`, "matched without a poster")
	require.Contains(t, body, `<span class="th th-ph" title="not matched" aria-label="not matched">?</span>`, "unmatched")
	require.Contains(t, body, `<a class="orig mu" href="/w/m348">Alien · 1979</a>`)
	require.Contains(t, body, "<b>Rus</b>", "the mark")
	require.Contains(t, body, `<td>1080p</td>`, "the quality")
	require.Contains(t, body, `<option value="other">other</option>`)

	for _, tt := range []struct {
		path string
		want []string
		not  []string
	}{
		{"/torrents?q=alien", []string{alien}, []string{chuzhoy, severance}},
		{"/torrents?q=" + hexOf(5), []string{ronin}, []string{alien}},
		{"/torrents?kind=unknown", []string{"Сборник"}, []string{alien, "Moon Safari"}},
		{"/torrents?kind=other", []string{"Moon Safari"}, []string{alien, "Сборник"}},
		{"/torrents?matched=no", []string{narcissist, heat, "Сборник"}, []string{alien, ronin}},
		{"/torrents?matched=yes", []string{alien, chuzhoy, ronin, severance}, []string{narcissist, heat}},
		{"/torrents?quality=720p", []string{chuzhoy}, []string{alien}},
		{"/torrents?vl=ru&voice=explicit", []string{alien}, []string{severance, chuzhoy}},
		{"/torrents?voice=p50", []string{alien, severance}, []string{chuzhoy, ronin}},
		{"/torrents?source=web", []string{severance}, []string{alien}},
		{"/torrents?layer=rules", []string{alien}, []string{"Сборник"}},
		{"/torrents?min=41,5", []string{alien}, []string{season1}},
	} {
		t.Run(tt.path, func(t *testing.T) {
			frag := fragment(t, get(t, h, tt.path, "torrents"), "torrents")
			for _, s := range tt.want {
				require.Contains(t, frag, s)
			}
			for _, s := range tt.not {
				require.NotContains(t, frag, s)
			}
		})
	}
	body = page(t, get(t, h, "/torrents?sort=size", ""))
	require.Contains(t, body, `<option value="size" selected>`)
	require.Less(t, strings.Index(body, alien), strings.Index(body, season1), "largest first")
	body = page(t, get(t, h, "/torrents?sort=bogus&kind=book&quality=4k", ""))
	require.Contains(t, body, `<option value="peers" selected>`, "an unknown value is the default")
	require.Contains(t, body, "Moon Safari", "an unknown kind is every kind")

	require.Equal(t, "/torrents?kind=movie&o=50&q=alien&voice=p50", link("/torrents", 50, "q", "alien", "", "kind", "movie", "", "vl", "ru", "ru", "voice", "p50", "any", "sort", "peers", "peers"))
	require.Equal(t, "/torrents", link("/torrents", 0, "q", "", "", "sort", "peers", "peers"))
}

func TestTop(t *testing.T) {
	h := handler(t, nil)
	body := page(t, get(t, h, "/top", ""))
	require.True(t, strings.HasPrefix(body, "<!doctype html>"))
	require.Contains(t, body, `aria-current="page">movies</a>`, "movies without a kind")
	require.Contains(t, body, `<a href="/w/m348">Alien</a> <span class="mu">1979</span>`, "a row leads to its work")
	require.Contains(t, body, `<img class="th" src="https://image.tmdb.org/t/p/w92/p.jpg" width="28" height="42"`, "its poster")
	require.Contains(t, body, `<td class="r mu">1</td>`, "its place")
	require.Contains(t, body, `<td class="r dl">new</td>`, "no peers the week before")
	require.Contains(t, body, `<td class="r rl" data-l="releases">2</td>`, "both title keys")
	require.Contains(t, body, "explicit in 1 of 2 <span class=\"mu\">(Rus)</span>", "the audio summary")
	require.Less(t, strings.Index(body, "/w/m348"), strings.Index(body, "/w/m8195"), "more peers first")
	require.NotContains(t, body, "Severance")
	require.NotContains(t, body, narcissist, "an unmatched release is in no top")
	require.Contains(t, body, ">show all 100</a>", "the first 25 of 100")
	require.Contains(t, body, "Oct 20 12:00", "computed at now")
	require.Contains(t, page(t, get(t, h, "/top?lang=ru", "")), `<a href="/w/m348">Чужой</a>`, "the title in the page's language")

	body = page(t, get(t, h, "/top?kind=series&win=24h&all=1", ""))
	require.Contains(t, body, `aria-current="page">series</a>`)
	require.Contains(t, body, `<a href="/w/tv95396">Severance</a>`)
	require.Regexp(t, `<td class="r dl">−\d+%</td>`, body, "fewer peers than the day before")
	require.Contains(t, body, "web", "its source")
	require.NotContains(t, body, ">show all 100</a>", "every work is listed")

	body = page(t, get(t, h, "/top?voice=explicit", ""))
	require.Contains(t, body, "/w/m348")
	require.NotContains(t, body, "/w/m8195", "no marker on Ronin")
	require.Contains(t, body, "only peers of releases whose name carries an explicit marker", "the filter's hint")
	require.Contains(t, page(t, get(t, h, "/top?voice=p50", "")), "/w/m348")

	frag := fragment(t, get(t, h, "/top?kind=series", "top"), "top")
	require.True(t, strings.HasPrefix(strings.TrimSpace(frag), `<section id="top"`))
	require.Contains(t, frag, "Severance")

	for _, tt := range []struct{ path, want string }{
		{"/top?kind=music", `aria-current="page">movies</a>`},
		{"/top?kind=", `aria-current="page">movies</a>`},
		{"/top?win=1y", `aria-current="true">7 days</a>`},
		{"/top?voice=loud", `aria-current="true">any</a>`},
		{"/top?all=yes", ">show all 100</a>"},
	} {
		require.Contains(t, page(t, get(t, h, tt.path, "")), tt.want, "%s falls back to the default", tt.path)
	}
	w := get(t, h, "/top/w/eTphbGllbiAxOTc5?open=1", "")
	require.Equal(t, http.StatusMovedPermanently, w.Code, "the old work rows are gone")
	require.Equal(t, "/w/eTphbGllbiAxOTc5", w.Header().Get("Location"))
}

func TestWork(t *testing.T) {
	h := handler(t, nil)
	body := page(t, get(t, h, "/w/m348", ""))
	require.True(t, strings.HasPrefix(body, "<!doctype html>"))
	require.Contains(t, body, `<a href="/top?kind=movie">← top movies</a>`)
	require.Contains(t, body, `<img class="tm-poster" src="https://image.tmdb.org/t/p/w342/p.jpg" width="150" height="225"`)
	require.Contains(t, body, `<h1 class="tm-title">Alien <span class="mu">1979</span></h1>`)
	require.Contains(t, body, "movie · 1979 · 1 h 57 min · US · en")
	require.Contains(t, body, "Horror, Science Fiction")
	require.Contains(t, body, `TMDB 8.2 <span class="mu">· 16,995 votes</span>`)
	require.Contains(t, body, `<a href="https://www.themoviedb.org/movie/348" rel="noreferrer" target="_blank">TMDB</a> · <a href="https://www.imdb.com/title/tt0078748/"`)
	require.Contains(t, body, `24 h <b>№1</b> · 7 days <b>№1</b> · 30 days <b>№1</b>`, "its places in the three tops")
	require.Regexp(t, `2 releases · 1\d\d peers in 7 days`, body, "the peers of both title keys")
	require.Contains(t, body, `<section id="releases" class="rels">`)
	require.Contains(t, body, `Releases <span class="mu">2 of 2</span>`)
	require.Contains(t, body, `<a href="/t/`+hexOf(1)+`">`+alien+`</a>`)
	require.Contains(t, body, `<a href="/t/`+hexOf(2)+`">`+chuzhoy+`</a>`, "the release under the Russian title")
	require.Less(t, strings.Index(body, alien), strings.Index(body, chuzhoy), "most peers first")
	require.NotContains(t, body, `name="season"`, "a movie has no seasons")
	ru := page(t, get(t, h, "/w/m348?lang=ru", ""))
	require.Contains(t, ru, `<div class="tm-orig">Alien</div>`, "the original title when it differs")
	require.Contains(t, ru, `src="https://image.tmdb.org/t/p/w342/r.jpg"`, "the poster of the page's language")

	frag := fragment(t, get(t, h, "/w/m348?vl=ru&voice=explicit", "releases"), "releases")
	require.True(t, strings.HasPrefix(strings.TrimSpace(frag), `<section id="releases"`))
	require.Contains(t, frag, alien)
	require.NotContains(t, frag, chuzhoy, "no marker")
	require.Contains(t, frag, `<option value="explicit" selected>`)
	frag = fragment(t, get(t, h, "/w/m348?quality=sd&source=remux", "releases"), "releases")
	require.Contains(t, frag, "No releases match the filters.")
	frag = fragment(t, get(t, h, "/w/m348?min=10&max=60", "releases"), "releases")
	require.Contains(t, frag, alien)
	require.NotContains(t, frag, chuzhoy, "8 GB is under the bound")
	frag = fragment(t, get(t, h, "/w/m348?sort=size&o=1", "releases"), "releases")
	require.Contains(t, frag, chuzhoy, "the second largest")
	require.Contains(t, frag, `<a href="/w/m348?sort=size" hx-get="/w/m348?sort=size"`, "the previous page")

	body = page(t, get(t, h, "/w/tv95396", ""))
	require.Contains(t, body, "series · 2022 · returning · 2 seasons")
	require.Contains(t, body, `<select name="season">`)
	require.Contains(t, body, `<option value="1">1</option><option value="2">2</option>`)
	require.Contains(t, body, `<td class="mono">S02E05</td>`)
	require.Contains(t, body, `<td class="mono">S01</td>`, "a season pack")
	frag = fragment(t, get(t, h, "/w/tv95396?season=2&episode=5", "releases"), "releases")
	require.Contains(t, frag, severance)
	require.NotContains(t, frag, season1)
	require.Contains(t, frag, `<option value="2" selected>`)

	body = page(t, get(t, h, "/w/m8195", ""))
	require.Contains(t, body, `<div class="tm-poster tm-none" aria-hidden="true">no poster</div>`)

	get(t, h, "/?q=alien", "")
	body = page(t, get(t, h, "/w/m679", ""))
	require.Contains(t, body, "No releases with us yet: found on TMDB only.")
	require.NotContains(t, body, `id="releases"`)

	require.Equal(t, http.StatusNotFound, get(t, h, "/w/m1", "").Code)
	require.Equal(t, http.StatusNotFound, get(t, h, "/w/348", "").Code, "not an id")
	require.Equal(t, http.StatusNotFound, get(t, h, "/w/tv348", "").Code, "a series is not the movie of its number")
}

func TestTorrent(t *testing.T) {
	h := handler(t, nil)
	body := page(t, get(t, h, "/t/"+hexOf(1), "", "Referer", "http://example.com/torrents?q=alien"))
	require.True(t, strings.HasPrefix(body, "<!doctype html>"))
	require.Contains(t, body, `<a href="/torrents?q=alien">← back to results</a>`, "back where the card was opened from")
	require.Contains(t, body, "magnet:?xt=urn:btih:"+hexOf(1))
	require.Contains(t, body, `<td><b>Rus</b></td>`, "the mark")
	require.Regexp(t, `<th>peers</th><td>\d+ in 7 days <span class="mu">· \d+ in 24 h · \d+ in 30 days</span></td>`, body)
	require.Contains(t, body, `<section class="tm tm-matched" aria-label="TMDB">`, "the match")
	require.Contains(t, body, `<img class="tm-poster" src="https://image.tmdb.org/t/p/w342/p.jpg" width="150" height="225"`)
	require.Contains(t, body, `<a class="tm-stretch" href="/w/m348">Alien</a> <span class="mu">1979</span>`, "the title leads to its work")
	require.Contains(t, body, `№1 among movies · 2 releases, this one by peers #1`)
	require.Contains(t, body, `picked by the classifier among candidates · confidence 0.80 · Oct 20`, "how it was matched")
	require.Contains(t, page(t, get(t, h, "/t/"+hexOf(1), "", "Referer", "http://example.com/data")), `<a href="/w/m348">← back to results</a>`,
		"from elsewhere, back to its work")

	require.Contains(t, body, "Rules fired")
	for _, hit := range []string{"year ← «1979»", "kind.movie ← «1080p»", "quality.1080p ← «1080p»", "audio.ru ← «Rus»"} {
		require.Contains(t, body, hit)
	}

	body = page(t, get(t, h, "/t/"+hexOf(2), ""))
	require.Contains(t, body, `this one by peers #2`)
	require.Contains(t, body, "matched by TMDB search, unique hit", "the match of its own title key")
	require.Contains(t, body, `<td>Cyrillic</td>`, "the script hint")

	body = page(t, get(t, h, "/t/"+hexOf(5), ""))
	require.Contains(t, body, `<div class="tm-poster tm-none" aria-hidden="true">no poster</div>`, "matched without a poster")

	body = page(t, get(t, h, "/t/"+hexOf(6), ""))
	require.Contains(t, body, `<section class="tm tm-unmatched" aria-label="TMDB">`, "looked up and not found")
	require.Contains(t, body, "Not found on TMDB.")
	require.Contains(t, body, `<a href="https://www.themoviedb.org/search?query=narcissists&#43;playbook&#43;2026" rel="noreferrer" target="_blank">search TMDB</a>`)
	require.Contains(t, body, "checked Oct 20")
	require.Contains(t, body, `<a href="/torrents">← back to results</a>`, "back to the torrents")
	body = page(t, get(t, h, "/t/"+hexOf(7), ""))
	require.Contains(t, body, `<section class="tm tm-unchecked" aria-label="TMDB">`, "never looked up")
	require.Contains(t, body, "Not checked against TMDB yet.")
	require.NotContains(t, body, "checked Oct")

	body = page(t, get(t, h, "/t/"+hexOf(1), ""))
	require.Equal(t, 8, strings.Count(body, `<td class="mono wrapc">`), "the first eight files")
	require.Contains(t, body, "?files=all")
	frag := fragment(t, get(t, h, "/t/"+hexOf(1)+"?files=all", "files"), "files")
	require.True(t, strings.HasPrefix(strings.TrimSpace(frag), `<div id="files">`))
	require.Equal(t, 10, strings.Count(frag, `<td class="mono wrapc">`), "every stored file")

	require.Equal(t, http.StatusNotFound, get(t, h, "/t/"+hexOf(99), "").Code)
	require.Equal(t, http.StatusNotFound, get(t, h, "/t/xyz", "").Code)
}

func TestTMDBWork(t *testing.T) {
	require.Nil(t, tmdbWork(nil), "unchecked")
	at := timestamppb.New(now)
	require.Equal(t, &ui.TMDBWork{State: ui.TMDBUnmatched, CheckedAt: now}, tmdbWork(&sniffarrv1.TMDBWork{CheckedAt: at}), "checked, nothing matched")
	got := tmdbWork(&sniffarrv1.TMDBWork{Id: 1399, Type: "tv", ImdbId: "tt0944947", Title: "Игра престолов", OriginalTitle: "Game of Thrones", Year: 2011,
		PosterPath: "/g.jpg", Overview: "Winter.", Genres: []string{"драма"}, Rating: 8.5, Votes: 25000, Runtime: 60, Status: "Ended", Seasons: 8,
		Countries: []string{"US"}, Language: "en", Layer: sniffarrv1.TMDBLayer_TMDB_LAYER_SEARCH, Confidence: 0.9, CheckedAt: at})
	require.Equal(t, &ui.TMDBWork{State: ui.TMDBMatched, CheckedAt: now, ID: 1399, Type: "tv", IMDbID: "tt0944947", Title: "Игра престолов", OriginalTitle: "Game of Thrones",
		Year: 2011, PosterPath: "/g.jpg", Overview: "Winter.", Genres: []string{"драма"}, Rating: 8.5, Votes: 25000, Runtime: 60, Status: "Ended", Seasons: 8,
		Countries: []string{"US"}, Language: "en", Method: "search", Confidence: float64(float32(0.9))}, got, "matched")
	require.True(t, tmdbWork(&sniffarrv1.TMDBWork{Id: 1}).CheckedAt.IsZero(), "a work's own TMDBWork has no check")
	require.Equal(t, "alien 1979", title("Alien.1979.1080p"))
	require.Empty(t, title("1080p"), "no title")
}

func TestData(t *testing.T) {
	h := handler(t, nil)
	body := page(t, get(t, h, "/data", ""))
	require.True(t, strings.HasPrefix(body, "<!doctype html>"))
	require.Contains(t, body, `<a href="/torrents?kind=movie">movie</a></td><td class="r">5</td>`)
	require.Contains(t, body, `<a href="/torrents?kind=other">other</a></td><td class="r">1</td>`, "music counts as other")
	require.Contains(t, body, `<a href="/torrents?kind=unknown">unknown</a></td><td class="r">1</td>`)
	require.Contains(t, body, "25.0%", "tokens used of the daily limit")
	require.Contains(t, body, "Dub by language")
	require.NotContains(t, body, "Grafana")
	frag := fragment(t, get(t, h, "/data", "data"), "data")
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

func TestNoLanguages(t *testing.T) {
	h := handlerLangs(t, nil, "")
	for _, path := range []string{"/torrents", "/torrents?vl=ru&voice=explicit", "/top", "/top?voice=explicit", "/w/m348", "/w/m348?voice=explicit", "/t/" + hexOf(1), "/data"} {
		body := page(t, get(t, h, path, ""))
		forbidden := []string{`name="vl"`, `name="voice"`, "Languages", "Dub by language", "<b>Rus</b>"}
		if !strings.Contains(path, "voice=") {
			forbidden = append(forbidden, "voice=")
		}
		for _, s := range forbidden {
			require.NotContains(t, body, s, "%s has %q", path, s)
		}
	}
	require.Contains(t, page(t, get(t, h, "/torrents?vl=ru&voice=explicit", "")), severance, "a voice filter in the query narrows nothing")
	require.Contains(t, page(t, get(t, h, "/w/m348?voice=explicit", "")), chuzhoy)
	require.Contains(t, page(t, get(t, h, "/top", "")), `<a href="/w/m348">Alien</a>`)
	require.Contains(t, page(t, get(t, h, "/t/"+hexOf(1), "")), "magnet:?xt=urn:btih:"+hexOf(1))
}
