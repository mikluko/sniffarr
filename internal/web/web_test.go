package web

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"sarak.sh/mikluko/libarr/database"
	libui "sarak.sh/mikluko/libarr/ui"
	"sarak.sh/mikluko/libarr/version"
	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"sarak.sh/mikluko/sniffarr/internal/audio"
	"sarak.sh/mikluko/sniffarr/internal/bus"
	"sarak.sh/mikluko/sniffarr/internal/bus/bustest"
	"sarak.sh/mikluko/sniffarr/internal/memconn"
	"sarak.sh/mikluko/sniffarr/internal/names"
	"sarak.sh/mikluko/sniffarr/internal/orders"
	"sarak.sh/mikluko/sniffarr/internal/rules"
	"sarak.sh/mikluko/sniffarr/internal/search"
	"sarak.sh/mikluko/sniffarr/internal/sketch"
	"sarak.sh/mikluko/sniffarr/internal/store"
	"sarak.sh/mikluko/sniffarr/internal/ui"
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

// titleMatcher matches Heat's title key to the movie 949 and any other to
// nothing.
type titleMatcher struct{ store *store.Store }

func (f titleMatcher) Matchable(ctx context.Context, keys []string) ([]store.TitleKey, error) {
	return f.store.UnmatchedTitles(ctx, keys, now.Add(time.Second), len(keys))
}

func (f titleMatcher) Match(ctx context.Context, k store.TitleKey, trigger string) error {
	found := store.Found{Trigger: trigger}
	if k.Key == names.Parse(heat).Key {
		found.Hit, found.Confidence, found.Layer = &store.Hit{Type: store.TypeMovie, TMDBID: 949, Lang: "en", Title: "Heat", Year: 1995}, 1, store.LayerTMDB
	}
	_, _, err := f.store.RecordMatch(ctx, k.Key, found, now)
	return err
}

// failingBrowse fails RequestMatch with code.
type failingBrowse struct {
	sniffarrv1connect.BrowseServiceClient
	code connect.Code
}

func (f failingBrowse) RequestMatch(context.Context, *connect.Request[sniffarrv1.RequestMatchRequest]) (*connect.Response[sniffarrv1.RequestMatchResponse], error) {
	return nil, connect.NewError(f.code, errors.New("failed"))
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
	return handlerWith(t, "ru", Config{Locales: l, Probes: true, Classifier: true})
}

// handlerLangs is handler with the audio languages langs, a comma-separated
// list, in the store and the UI alike; "" leaves the extension off.
func handlerLangs(t *testing.T, l Locales, langs string) http.Handler {
	t.Helper()
	return handlerWith(t, langs, Config{Locales: l, Probes: true, Classifier: true})
}

// handlerWith is handlerLangs taking Locales, Probes, Classifier, and the
// Seerr, Radarr and Sonarr URLs from cfg; the features go to the API too.
func handlerWith(t *testing.T, langs string, cfg Config) http.Handler {
	t.Helper()
	h, _ := handlerTasks(t, langs, cfg)
	return h
}

// grabs is how many grabs Radarr reported of hash(22), a torrent never
// stored, each in a row of its own.
const grabs = 7

// longJournal is how many grabs Radarr reported of hash(23), a torrent never
// stored: a page of its journal and 3 more entries, on one day.
const longJournal = ui.JournalPageSize + 3

// handlerTasks is handlerWith returning the API's count of probers as well.
func handlerTasks(t *testing.T, langs string, cfg Config) (*Handler, *store.Probers) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{Service: "sniffarr"})
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
		{Hash: hash(5), Name: ronin, Size: 9 << 30, Discovery: "sample", SeenAt: now.Add(-3 * time.Hour), Kind: "movie", KindLayer: store.LayerRules, Source: "remux"},
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
	match(alien, store.TypeMovie, 348, &alienDetails, []store.ExternalID{{Provider: store.ProviderIMDb, ID: "tt0078748"}}, store.LayerClassifier)
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
	_, err = st.Reject(ctx, hash(20), rules.Small, "small", 1<<20, now)
	require.NoError(t, err)
	grab := func(h store.Hash, key string, at time.Time) {
		t.Helper()
		_, err := st.RecordArr(ctx, store.ArrEvent{App: store.AppRadarr, Event: store.EventGrab, Source: "history", Key: key, Hash: &h, Title: key, At: at}, at)
		require.NoError(t, err)
	}
	for i := range grabs {
		grab(hash(22), fmt.Sprintf("Grab.%d", i+1), now.Add(time.Duration(i-grabs)*time.Hour))
	}
	for i := range longJournal {
		grab(hash(23), fmt.Sprintf("Long.%d", i+1), now.Add(time.Duration(i-longJournal)*time.Second))
	}
	require.NoError(t, st.AddTokens(ctx, now.Format(time.DateOnly), 250))
	require.NoError(t, st.AddProbeTraffic(ctx, now.Format(time.DateOnly), store.ProbeTraffic{Rx: 61 << 30, Tx: 38 << 30}))
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
	probers := &store.Probers{}
	js, err := jetstream.New(bustest.Conn(t))
	require.NoError(t, err)
	fetches := orders.New(orders.Config{Stream: bus.StreamFetchOrders, Lanes: bus.FetchLanes})
	require.NoError(t, fetches.Start(ctx, js))
	probes := orders.New(orders.Config{Stream: bus.StreamProbeOrders, Lanes: bus.ProbeLanes})
	require.NoError(t, probes.Start(ctx, js))
	for p, bs := range map[int][]byte{store.PriorityArr: {30}, store.PrioritySample: {31, 32}} {
		l, _ := fetches.Lane(store.TaskFetch, p)
		for _, b := range bs {
			_, err := fetches.Publish(ctx, l, []orders.Item{{Hash: hash(b)}})
			require.NoError(t, err)
		}
	}
	for i := range 10 {
		fetches.Finished(store.TaskFetch, now.Add(-time.Duration(i+1)*time.Minute))
	}
	fetches.Finished(store.TaskPeers, now.Add(-time.Minute))
	h, err := New(Config{Browse: browse(t, st, probers, fetches, probes, audio.Tags(languages), cfg), Languages: languages, Probes: cfg.Probes, Classifier: cfg.Classifier,
		Locales: cfg.Locales, SeerrURL: cfg.SeerrURL, RadarrURL: cfg.RadarrURL, SonarrURL: cfg.SonarrURL, Location: time.UTC, JournalRetention: 30 * 24 * time.Hour, Now: func() time.Time { return now }})
	require.NoError(t, err)
	return h, probers
}

// browse serves BrowseService over st with the probes and classifier cfg
// turns on, behind the token "search" in-process, and returns a client
// holding the token.
func browse(t *testing.T, st *store.Store, probers *store.Probers, fetches, probes *orders.Queue, tags []string, cfg Config) sniffarrv1connect.BrowseServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	b := &search.Browser{Store: st, Fetches: fetches, ProbeOrders: probes, Probers: probers, TMDB: titleSearch{st}, Matcher: titleMatcher{st}, Log: slog.New(slog.DiscardHandler), DailyTokens: 1000, ProbeDailyBytes: 100 << 30,
		Probes: cfg.Probes, Classifier: cfg.Classifier, Languages: tags, Now: func() time.Time { return now },
		Counts: &search.Counts{Store: st, Now: func() time.Time { return now.Add(-7 * time.Minute) }, Interval: 5 * time.Minute}}
	mux.Handle(sniffarrv1connect.NewBrowseServiceHandler(b))
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	srv := &http.Server{Handler: mux, Protocols: &p}
	l := memconn.Listen()
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { require.NoError(t, srv.Shutdown(context.Background())) })
	return sniffarrv1connect.NewBrowseServiceClient(l.Client(), memconn.URL)
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

var untranslated = regexp.MustCompile(`\b(?:(?:act|kind|layer|source|drop|voiceband|voicemode|relvoice|relsort|quality|script|via|d|card|col|home|sort|nav|f|top|topkind|win|results|unit|attr|work|rel|search|matched|audio|dub|langname|q|journal)\.[a-z_][a-z0-9_]*` +
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

func TestRoot(t *testing.T) {
	h := handler(t, nil)
	body := page(t, get(t, h, "/", ""))
	require.True(t, strings.HasPrefix(body, "<!doctype html>"))
	require.Contains(t, body, `<main id="data"`, "the data page is the first page")
	require.Contains(t, body, `<a href="/" class="brand" aria-current="page">sniffarr`, "the logo leads to it")
	require.Contains(t, body, `<img src="/static/vendor/tmdb.svg?v=`, "the attribution, the image linked at its version")
	require.Contains(t, body, "This product uses the TMDB API but is not endorsed or certified by TMDB.")
	require.Contains(t, body, `href="https://sarak.sh/mikluko/sniffarr"`, "the footer links the project page")
	require.Contains(t, body, ">sniffarr "+version.Get().Version+"<span", "the footer names the build")
	require.Equal(t, http.StatusOK, get(t, h, "/static/vendor/tmdb.svg", "").Code)
	require.NotContains(t, page(t, get(t, h, "/?q=alien", "")), "wrow", "no search of titles")
	require.Equal(t, http.StatusNotFound, get(t, h, "/data", "").Code, "the data page lives at / only")
}

func TestServerHeader(t *testing.T) {
	h := handler(t, nil)
	want := version.Get().Server("sniffarr")
	for path, code := range map[string]int{
		"/":                       http.StatusOK,
		"/static/app.css":         http.StatusOK,
		"/ui/app.js":              http.StatusOK,
		"/nowhere":                http.StatusNotFound,
		"/t/" + hexOf(99):         http.StatusNotFound,
		"/static/no-such-file.js": http.StatusNotFound,
	} {
		w := get(t, h, path, "")
		require.Equal(t, code, w.Code, path)
		require.Equal(t, want, w.Header().Get("Server"), path)
	}
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
	require.Contains(t, body, `<a href="/movie/348?from=%2Ftorrents" class="thl"><img class="th" src="https://image.tmdb.org/t/p/w92/p.jpg"`, "a matched row's poster leads to its work")
	require.Contains(t, body, `<span class="th th-ph tt" tabindex="0" aria-label="Matched; TMDB has no poster.">—<span class="tc"`, "matched without a poster")
	require.Contains(t, body, `<span class="th th-ph tt" tabindex="0" aria-label="Not matched to a movie or series.">?<span class="tc"`, "unmatched")
	require.Contains(t, body, `<a class="orig" href="/movie/348?from=%2Ftorrents">Alien · 1979</a>`)
	require.NotContains(t, body, `class="tfreset"`, "no reset without a filter")
	require.Contains(t, body, `aria-expanded="false">Filters</button>`, "no count without a filter")
	require.Contains(t, body, "<b>Rus</b>", "the mark")
	require.Contains(t, body, `<td>1080p</td>`, "the quality")
	require.Contains(t, body, `<option value="other">other</option>`)

	for _, tt := range []struct {
		path string
		want []string
		not  []string
	}{
		{"/torrents?q=alien", []string{alien, chuzhoy}, []string{severance}},
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
	require.NotContains(t, body, `class="tfreset"`, "an unknown value sets no filter")

	body = page(t, get(t, h, "/torrents?q=alien&kind=movie&min=1&voice=p50&sort=size", ""))
	require.Contains(t, body, `<select name="kind" class="set">`)
	require.Contains(t, body, `class="num set"`)
	require.Contains(t, body, `<select name="voice" aria-label="Voice-over threshold" class="set">`)
	require.Contains(t, body, `<select name="quality">`, "a filter left at all is not set")
	require.Contains(t, body, `<select name="sort">`, "the sort is no filter")
	require.Contains(t, body, `Filters<span class="mu"> · 3</span></button>`)
	require.Contains(t, body, `<a class="tfreset" href="/torrents?q=alien&amp;sort=size"`, "reset keeps the query and the sort")

	require.Equal(t, "/torrents?kind=movie&o=50&q=alien&voice=p50", link("/torrents", 50, "q", "alien", "", "kind", "movie", "", "vl", "ru", "ru", "voice", "p50", "any", "sort", "peers", "peers"))
	require.Equal(t, "/torrents", link("/torrents", 0, "q", "", "", "sort", "peers", "peers"))
}

func TestTop(t *testing.T) {
	h := handler(t, nil)
	body := page(t, get(t, h, "/top", ""))
	require.True(t, strings.HasPrefix(body, "<!doctype html>"))
	require.Contains(t, body, `aria-current="page">Movies</a>`, "movies without a kind")
	require.Contains(t, body, `<a href="/movie/348?from=%2Ftop">Alien</a> <span class="mu">1979</span>`, "a row leads to its work")
	require.Contains(t, body, `<img class="th" src="https://image.tmdb.org/t/p/w92/p.jpg" width="32" height="48"`, "its poster")
	require.Contains(t, body, `<td class="r mu">1</td>`, "its place")
	require.Contains(t, body, `<td class="r dl mu"><span class="tt" tabindex="0" aria-label="—: In the top 100 since the history began, Oct 20, 2026;`,
		"no peers the week before, and in the top at the first computation: new or back is not known")
	require.Contains(t, body, "<td class=\"r db mu\"><span class=\"dk\"><span class=\"tt\" tabindex=\"0\" aria-label=\"≤\u00a0Oct 20: ", "its debut is the history's start or before")
	require.Contains(t, body, `<td class="r rl"><span class="dk">2</span>`, "both title keys")
	require.Contains(t, body, `<td class="r pk"><span class="mb">peak </span><b>#1</b></td>`, "at its peak")
	require.NotContains(t, body, "Marked in the name", "no audio column")
	require.Less(t, strings.Index(body, "/movie/348"), strings.Index(body, "/movie/8195"), "more peers first")
	require.NotContains(t, body, "Severance")
	require.NotContains(t, body, narcissist, "an unmatched release is in no top")
	require.Contains(t, body, ">Show all 100</a>", "the first 25 of 100")
	require.Contains(t, body, "Oct 20, 12:00", "computed at now")
	require.Contains(t, page(t, get(t, h, "/top?lang=ru", "")), `<a href="/movie/348?from=%2Ftop">Чужой</a>`, "the title in the page's language")

	body = page(t, get(t, h, "/top?kind=series&win=24h&all=1", ""))
	require.Contains(t, body, `aria-current="page">Series</a>`)
	require.Contains(t, body, `<a href="/tv/95396?from=%2Ftop%3Fall%3D1%26kind%3Dseries%26win%3D24h">Severance</a>`, "the page it leaves")
	require.Contains(t, body, `<td class="r dl mu">=</td>`, "first the day before too, with more peers then")
	require.NotContains(t, body, `class="src`, "no sources column")
	require.NotContains(t, body, ">Show all 100</a>", "every work is listed")

	body = page(t, get(t, h, "/top?voice=explicit", ""))
	require.Contains(t, body, "/movie/348")
	require.NotContains(t, body, "/movie/8195", "no marker on Ronin")
	require.Contains(t, body, `<span class="tc" aria-hidden="true">By unique peers in the window, summed over releases that pass the voice-over filter. “explicit”: only peers of releases whose name carries an explicit marker`, "the caption and the filter's hint in one tooltip")
	require.NotContains(t, body, `class="hint"`)
	require.Contains(t, page(t, get(t, h, "/top?voice=p50", "")), "/movie/348")

	frag := fragment(t, get(t, h, "/top?kind=series", "top"), "top")
	require.True(t, strings.HasPrefix(strings.TrimSpace(frag), `<section id="top"`))
	require.Contains(t, frag, "Severance")

	for _, tt := range []struct{ path, want string }{
		{"/top?kind=music", `aria-current="page">Movies</a>`},
		{"/top?kind=", `aria-current="page">Movies</a>`},
		{"/top?win=1y", `aria-current="true">7 days</a>`},
		{"/top?voice=loud", `aria-current="true">any</a>`},
		{"/top?all=yes", ">Show all 100</a>"},
	} {
		require.Contains(t, page(t, get(t, h, tt.path, "")), tt.want, "%s falls back to the default", tt.path)
	}
	w := get(t, h, "/top/w/m348?open=1", "")
	require.Equal(t, http.StatusMovedPermanently, w.Code, "the old work rows are gone")
	require.Equal(t, "/movie/348", w.Header().Get("Location"))
	w = get(t, h, "/w/tv95396", "")
	require.Equal(t, http.StatusMovedPermanently, w.Code, "the old title pages are gone")
	require.Equal(t, "/tv/95396", w.Header().Get("Location"))
	require.Equal(t, http.StatusNotFound, get(t, h, "/w/eTphbGllbiAxOTc5", "").Code, "an id of no version")
}

func TestWork(t *testing.T) {
	h := handler(t, nil)
	body := page(t, get(t, h, "/movie/348", ""))
	require.True(t, strings.HasPrefix(body, "<!doctype html>"))
	require.NotContains(t, body, `class="head"`, "no back link without a known origin")
	require.Contains(t, body, `<source media="(max-width:640px)" srcset="https://image.tmdb.org/t/p/w185/p.jpg">`, "a phone's 96 px poster is the 185 px file")
	require.Contains(t, body, `<img class="hero-ph" src="https://image.tmdb.org/t/p/w185/p.jpg" srcset="https://image.tmdb.org/t/p/w342/p.jpg 2x" width="185" height="278"`)
	require.Contains(t, body, `<h1>Alien <span class="mu">1979</span></h1>`)
	require.Contains(t, body, "Movie · 1 h 57 min · US · en", "the year is the title's")
	require.Contains(t, body, "<div>horror, science fiction<span class=\"mu\"> · TMDB 8.2 · 16,995\u00a0votes</span></div>", "the rating on the genres' line")
	require.Contains(t, body, `<a href="https://www.themoviedb.org/movie/348" rel="noreferrer" target="_blank">TMDB</a> · <a href="https://www.imdb.com/title/tt0078748/"`)
	require.NotContains(t, body, "Seerr", "no Seerr configured")
	require.Contains(t, body, `<span>Chart position</span>
    <span>24 h #1 · 7 days <b>#1</b> · 30 days #1</span>`, "its places in the three tops, the 7 days' in bold")
	require.Contains(t, body, `<span class="tt" tabindex="0" aria-label="Peak: `, "the peak's tooltip on its label")
	require.Regexp(t, `<span>Peers</span>\s*<span><b>1\d\d</b> in 7 days`, body, "the peers of both title keys")
	require.Contains(t, body, `<span>Releases</span>
    <span>2</span>`)
	require.Contains(t, body, `<span class="mu">ru</span> <b><span class="tt" tabindex="0" aria-label="explicit in 1 of 2: Marked in the name: Rus">explicit in 1 of 2<span class="tc"`,
		"the dub on a line, its marks in the tooltip")
	require.Contains(t, body, `<form method="post" action="/movie/348/peers"`, "the actions on every release")
	require.Contains(t, body, `<section id="releases" class="rels">`)
	require.Contains(t, body, `<input type="checkbox" id="relf"`, "the phone's filter toggle outside the fragment")
	require.Contains(t, body, `Releases <span class="mu">2</span>`, "no filter set: the count alone")
	require.Contains(t, body, `<a href="/movie/348" hx-get="/movie/348" hx-target="#releases" hx-swap="morph:outerHTML" hx-push-url="true" class="o on" aria-current="true">all</a><a href="/movie/348?quality=2160p"`,
		"the quality as segments")
	require.NotContains(t, body, `name="source"`, "no source filter")
	require.NotContains(t, body, `aria-label="Source"`, "no source filter")
	require.NotContains(t, body, `name="voice"`, "no dub filter")
	require.NotContains(t, body, `<th>Source</th>`, "no source column")
	require.NotContains(t, body, `<th>Voice-over</th>`, "no dub column")
	require.NotContains(t, body, "Reset filters", "nothing to reset")
	require.Contains(t, body, `<a href="/t/`+hexOf(1)+`">`+alien+`</a>`)
	require.Contains(t, body, `<a href="/t/`+hexOf(2)+`">`+chuzhoy+`</a>`, "the release under the Russian title")
	require.Less(t, strings.Index(body, alien), strings.Index(body, chuzhoy), "most peers first")
	require.NotContains(t, body, `name="season"`, "a movie has no seasons")
	ru := page(t, get(t, h, "/movie/348?lang=ru", ""))
	require.Contains(t, ru, `<div class="mu">Alien</div>`, "the original title when it differs")
	require.Contains(t, ru, `src="https://image.tmdb.org/t/p/w185/r.jpg"`, "the poster of the page's language")

	frag := fragment(t, get(t, h, "/movie/348?quality=1080p&source=remux&voice=explicit", "releases"), "releases")
	require.True(t, strings.HasPrefix(strings.TrimSpace(frag), `<section id="releases"`))
	require.Contains(t, frag, alien)
	require.NotContains(t, frag, chuzhoy, "720p")
	require.Contains(t, frag, `class="o on" aria-current="true">1080p</a>`)
	require.Contains(t, frag, `<input type="hidden" name="quality" value="1080p">`, "the form keeps the segment's value")
	require.NotContains(t, frag, `value="remux"`, "the source is no filter, so a link's is dropped")
	require.NotContains(t, frag, `voice=`, "nor the dub's")
	require.Contains(t, frag, `Releases <span class="mu">1 of 2</span>`, "a filter set: matched of all")
	require.Contains(t, frag, `<label for="relf" class="rtog">Filters<span class="mu"> · 1</span></label>`)
	require.Contains(t, frag, `<a href="/movie/348?quality=2160p"`, "a segment of another filter keeps the others")
	require.Contains(t, frag, ">Reset filters</a>")
	frag = fragment(t, get(t, h, "/movie/348?quality=sd&min=10", "releases"), "releases")
	require.Contains(t, frag, "No releases match the filters.")
	frag = fragment(t, get(t, h, "/movie/348?min=10&max=60", "releases"), "releases")
	require.Contains(t, frag, alien)
	require.NotContains(t, frag, chuzhoy, "8 GB is under the bound")
	frag = fragment(t, get(t, h, "/movie/348?sort=size&o=1", "releases"), "releases")
	require.Contains(t, frag, chuzhoy, "the second largest")
	require.Contains(t, frag, `<a class="btn" href="/movie/348?sort=size" hx-get="/movie/348?sort=size"`, "the previous page")

	body = page(t, get(t, h, "/tv/95396", ""))
	require.Contains(t, body, "Series · returning · 2 seasons")
	require.Contains(t, body, `class="o">1</a><a href="/tv/95396?season=2"`, "the seasons as segments")
	require.Contains(t, body, `<th>Episode</th>`)
	require.Contains(t, body, `<td class="l1">S02E05</td>`)
	require.Contains(t, body, `<td class="l1">S01</td>`, "a season pack")
	frag = fragment(t, get(t, h, "/tv/95396?season=2&episode=E05", "releases"), "releases")
	require.Contains(t, frag, severance)
	require.NotContains(t, frag, season1)
	require.Contains(t, frag, `class="o on" aria-current="true">2</a>`)
	require.Contains(t, frag, `<input name="episode" type="text" inputmode="numeric" value="5" class="ep set">`, "E05 is episode 5, bordered as set")

	body = page(t, get(t, h, "/movie/8195", ""))
	require.Contains(t, body, `<div class="hero-ph hero-none" aria-hidden="true">No poster</div>`)

	require.Equal(t, http.StatusNotFound, get(t, h, "/movie/1", "").Code)
	require.Equal(t, http.StatusNotFound, get(t, h, "/movie/m348", "").Code, "not a number")
	require.Equal(t, http.StatusNotFound, get(t, h, "/tv/348", "").Code, "a series is not the movie of its number")

	seerr := handlerWith(t, "ru", Config{SeerrURL: "https://seerr.example.com/"})
	require.Contains(t, page(t, get(t, seerr, "/movie/348", "")), `<a href="https://www.imdb.com/title/tt0078748/" rel="noreferrer" target="_blank">IMDb</a> · <a href="https://seerr.example.com/movie/348" rel="noreferrer" target="_blank">Seerr</a>`,
		"a Seerr link after the others")
	require.Contains(t, page(t, get(t, seerr, "/tv/95396", "")), `<a href="https://seerr.example.com/tv/95396" rel="noreferrer" target="_blank">Seerr</a>`, "a series under /tv")
	require.Contains(t, page(t, get(t, seerr, "/t/"+hexOf(1), "")), `<a href="https://seerr.example.com/movie/348" rel="noreferrer" target="_blank">Seerr</a>`, "the release card too")
	require.NotContains(t, page(t, get(t, seerr, "/movie/348", "")), "Radarr", "no Radarr configured")

	arr := handlerWith(t, "ru", Config{SeerrURL: "https://seerr.example.com", RadarrURL: "https://radarr.example.com", SonarrURL: "https://sonarr.example.com"})
	require.Contains(t, page(t, get(t, arr, "/movie/348", "")), `<a href="https://seerr.example.com/movie/348" rel="noreferrer" target="_blank">Seerr</a> · <a href="https://radarr.example.com/add/new?term=tmdb%3A348" rel="noreferrer" target="_blank">Radarr</a>`,
		"a movie in Radarr, after Seerr")
	require.NotContains(t, page(t, get(t, arr, "/movie/348", "")), "Sonarr", "a movie is not in Sonarr")
	require.Contains(t, page(t, get(t, arr, "/tv/95396", "")), `<a href="https://sonarr.example.com/add/new?term=tmdb%3A95396" rel="noreferrer" target="_blank">Sonarr</a>`, "a series in Sonarr")
	require.Contains(t, page(t, get(t, arr, "/t/"+hexOf(1), "")), `<a href="https://radarr.example.com/add/new?term=tmdb%3A348" rel="noreferrer" target="_blank">Radarr</a>`, "the release card too")
}

func TestTorrent(t *testing.T) {
	h := handler(t, nil)
	body := page(t, get(t, h, "/t/"+hexOf(1), "", "Referer", "http://example.com/torrents?q=alien"))
	require.True(t, strings.HasPrefix(body, "<!doctype html>"))
	require.Contains(t, body, `<a href="/torrents?q=alien">← Back to results</a>`, "back where the card was opened from")
	require.Contains(t, body, "magnet:?xt=urn:btih:"+hexOf(1))
	require.Contains(t, body, `<td><b>Rus</b></td>`, "the mark")
	require.Regexp(t, `<th>Peers</th><td><b>\d+</b> in 7 days<br><span class="mu">\d+ in 24 h · \d+ in 30 days</span></td>`, body)
	require.Contains(t, body, "<th>Size</th><td>55.0 GB · 10 files</td>", "the size with the count of files")
	require.Contains(t, body, `<section class="tm tm-matched tm-card" aria-label="TMDB">`, "the match")
	require.Contains(t, body, `<img class="tm-poster" src="https://image.tmdb.org/t/p/w342/p.jpg" width="116" height="174"`)
	require.Contains(t, body, `<a class="tm-stretch" href="/movie/348?from=%2Ft%2F`+hexOf(1)+`">Alien</a> <span class="mu">1979</span>`, "the title leads to its work")
	require.Contains(t, body, "Movie · 1 h 57 min · US · en · horror, science fiction", "the genres on the facts' line")
	require.Contains(t, body, "#1 among movies · 2 releases, this one by peers #1 ·</span>")
	require.Contains(t, body, `<span class="tt" tabindex="0" aria-label="Picked by the classifier: By the classifier among TMDB candidates, confidence 0.80, Oct 20.">Picked by the classifier<span class="tc"`,
		"how it was matched, the details in the tooltip")
	require.Contains(t, body, `<h2>Files <span class="mu">10</span></h2>`)
	require.Contains(t, page(t, get(t, h, "/t/"+hexOf(1), "", "Referer", "http://example.com/data")), `<a href="/movie/348">← Back to results</a>`,
		"from elsewhere, back to its work")

	require.Contains(t, body, "Rules fired")
	for _, hit := range []string{"year ← «1979»", "kind.movie ← «BluRay»", "quality.1080p ← «1080p»", "audio.ru ← «Rus»"} {
		require.Contains(t, body, hit)
	}
	require.Contains(t, body, `<details class="jrow" id="ev-1">`, "a row anchored by its entry")
	require.Contains(t, body, `<time datetime="2026-10-20T11:00:00Z" title="Oct 20, 2026, 11:00:00">11:00</time>`, "the time to the second in its title")
	require.Contains(t, body, "<span class=\"jw\">Metadata fetched</span>\n  <span class=\"jo\">stored</span>\n  <span class=\"jk\">10 files · 55.0 GB</span>",
		"the journal, its kind, outcome and main particulars in words")
	require.Contains(t, body, "<span>discovered by</span><span>announce</span><span>files</span><span>10</span>", "every particular under the open row")
	require.Contains(t, body, `<span class="jw">Title matched</span>`)
	require.Contains(t, body, "2 events · Oct 20, 11:00</span>", "how many, and when")
	require.Contains(t, body, "Entries are kept for 30 days", "the retention in the heading's tooltip")
	ru := page(t, get(t, h, "/t/"+hexOf(1)+"?lang=ru", ""))
	require.Contains(t, ru, "<span class=\"jw\">Метаданные получены</span>\n  <span class=\"jo\">сохранены</span>", "in Russian, agreeing with the event")
	require.Contains(t, ru, "Записи хранятся 30 дней")

	body = page(t, get(t, h, "/t/"+hexOf(2), ""))
	require.Contains(t, body, `this one by peers #2`)
	require.Contains(t, body, ">Matched uniquely<", "the match of its own title key")
	require.Contains(t, body, `<td class="hm">Cyrillic</td>`, "the script hint")

	body = page(t, get(t, h, "/t/"+hexOf(5), ""))
	require.Contains(t, body, `<div class="tm-poster tm-none" aria-hidden="true">No poster</div>`, "matched without a poster")
	require.Contains(t, body, `<tr><td>Source</td><td>remux</td><td>rules</td><td class="r">—</td></tr>`, "a source not classified is the rules'")

	body = page(t, get(t, h, "/t/"+hexOf(6), ""))
	require.Contains(t, body, `<section class="tm tm-unmatched" aria-label="TMDB">`, "looked up and not found")
	require.Contains(t, body, "Not found on TMDB.")
	require.Contains(t, body, `<a href="https://www.themoviedb.org/search?query=narcissists&#43;playbook&#43;2026" rel="noreferrer" target="_blank">Search TMDB</a>`)
	require.Contains(t, body, "Checked Oct 20")
	require.Contains(t, body, `<a href="/torrents">← Back to results</a>`, "back to the torrents")
	body = page(t, get(t, h, "/t/"+hexOf(7), ""))
	require.Contains(t, body, `<section class="tm tm-unchecked" aria-label="TMDB">`, "never looked up")
	require.Contains(t, body, "Not checked against TMDB yet.")
	require.NotContains(t, body, "Checked Oct")

	body = page(t, get(t, h, "/t/"+hexOf(1), ""))
	require.Equal(t, 8, strings.Count(body, `<td class="mono wrapc">`), "the first eight files")
	require.Contains(t, body, "?files=all")
	frag := fragment(t, get(t, h, "/t/"+hexOf(1)+"?files=all", "files"), "files")
	require.True(t, strings.HasPrefix(strings.TrimSpace(frag), `<div id="files">`))
	require.Equal(t, 10, strings.Count(frag, `<td class="mono wrapc">`), "every stored file")

	body = page(t, get(t, h, "/t/"+hexOf(20), ""))
	require.Contains(t, body, `<h1 class="mono">`+hexOf(20)+`</h1>`, "a rejected torrent, not stored: its hash")
	require.Contains(t, body, `<i class="m bad" aria-hidden="true"></i><b>No torrent: rejected Oct 20, 12:00</b>, reason: under 50 MB.</span>`, "what ended it")
	require.Contains(t, body, "The torrent was never stored, so only its journal is left. Metadata: “small”, 1.0\u00a0MB.")
	require.Contains(t, body, `<details class="jrow" id="ev-`, "and its journal")
	require.Contains(t, body, `<span class="jo o-bad"><i class="m bad" aria-hidden="true"></i><span class="vh">final failure: </span>rejected</span>`, "a final failure marked, in words too")
	require.NotContains(t, body, `id="files"`, "nothing of a stored torrent")
	require.Contains(t, page(t, get(t, h, "/t/"+hexOf(20)+"?lang=ru", "")), "<b>Раздачи нет: отброшена ")

	require.Equal(t, http.StatusNotFound, get(t, h, "/t/"+hexOf(99), "").Code, "neither a torrent nor a journal")
	require.Equal(t, http.StatusNotFound, get(t, h, "/t/xyz", "").Code)
}

func TestTorrentJournal(t *testing.T) {
	h := handler(t, nil)
	card := "/t/" + hexOf(22)
	rows := func(body string) int { return strings.Count(body, `<details class="jrow"`) }

	body := page(t, get(t, h, card, ""))
	require.Equal(t, ui.JournalCardRows, rows(body), "the card's newest rows")
	require.Contains(t, body, "Showing the last 5 of 7 rows")
	require.Contains(t, body, `<a class="btn" href="/t/`+hexOf(22)+`?journal=all#journal" hx-get="/t/`+hexOf(22)+`/journal" hx-target="#jlist" hx-swap="morph:keepOpen">Show the whole journal</a>`)

	frag := fragment(t, get(t, h, card+"/journal", "jlist"), "jlist")
	require.True(t, strings.HasPrefix(strings.TrimSpace(frag), `<div class="jlist" id="jlist">`))
	require.Equal(t, grabs, rows(frag), "every row")
	require.NotContains(t, frag, "Show the whole journal")

	w := get(t, h, card+"/journal", "")
	require.Equal(t, http.StatusSeeOther, w.Code, "without htmx, the card with every row")
	require.Equal(t, card+"?journal=all#journal", w.Header().Get("Location"))
	require.Equal(t, grabs, rows(page(t, get(t, h, card+"?journal=all", ""))))

	require.Equal(t, http.StatusNotFound, get(t, h, "/t/xyz/journal", "jlist").Code)
}

func TestTorrentJournalPages(t *testing.T) {
	h := handler(t, nil)
	card := "/t/" + hexOf(23)
	more := regexp.MustCompile(`<div class="jfoot" id="jpage-(\d+)">`)

	body := page(t, get(t, h, card+"?journal=all", ""))
	require.Contains(t, body, "Showing the last 200 of 203 events", "a page of the journal, and how many it holds")
	m := more.FindStringSubmatch(body)
	require.NotNil(t, m, "a button for the page before")
	before := m[1]
	day := now.Format(time.DateOnly)
	require.Contains(t, body, `hx-get="/t/`+hexOf(23)+`/journal?before=`+before+`&amp;day=`+day+`" hx-target="#jpage-`+before+`" hx-swap="outerHTML">3 more</a>`)

	frag := page(t, get(t, h, card+"/journal?before="+before+"&day="+day, "jpage-"+before))
	require.False(t, strings.HasPrefix(frag, "<!doctype html>"), "the rows alone")
	require.Equal(t, 3, strings.Count(frag, `<details class="jrow"`), "the older grabs, a row each")
	require.NotContains(t, frag, `class="jday"`, "the day the rows above end on is not headed again")
	require.NotContains(t, frag, `class="jfoot"`, "nothing older")
	frag = page(t, get(t, h, card+"/journal?before="+before+"&day=2026-01-01", "jpage-"+before))
	require.Contains(t, frag, `class="jday"`, "another day is headed")

	w := get(t, h, card+"/journal?before="+before, "")
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Equal(t, card+"?before="+before+"&journal=all#journal", w.Header().Get("Location"))
	body = page(t, get(t, h, card+"?before="+before+"&journal=all", ""))
	require.Contains(t, body, `<details class="jrow"`, "without htmx, the card with that page")
	require.NotContains(t, body, "No torrent", "which is not the newest, so says nothing of the end")
	require.NotContains(t, body, `class="jfoot"`)
}

// post serves a POST of the path, as htmx asking for target when it is not
// "".
func post(t *testing.T, h http.Handler, path, target string, header ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
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

func TestRequests(t *testing.T) {
	h, probers := handlerTasks(t, "ru", Config{Probes: true})
	body := page(t, get(t, h, "/t/"+hexOf(1), ""))
	require.Contains(t, body, `<form method="post" action="/t/`+hexOf(1)+`/peers" hx-post="/t/`+hexOf(1)+`/peers" hx-target="#act-answer" hx-swap="outerHTML" hx-disabled-elt="find button">`,
		"the answer goes to the line under the buttons, the button disabled meanwhile")
	require.Contains(t, body, `<button id="act-probe" type="submit">Probe tracks</button>`)
	require.Contains(t, body, `<div id="act-answer" class="act-ans" role="status"></div>`, "no answer yet")
	body = page(t, get(t, h, "/t/"+hexOf(7), ""))
	require.Contains(t, body, `<button id="act-probe" type="submit" disabled>Probe tracks</button>`, "no probe without peers")
	require.Contains(t, body, `<div id="act-answer" class="act-ans" role="status">Cannot probe tracks: no peers over 7 days.</div>`, "and the line says why")
	body = page(t, get(t, h, "/movie/348", ""))
	require.Contains(t, body, `<form method="post" action="/movie/348/probe"`)

	queue := ` Queue on the <a href="/">Data</a> page.</div>`
	frag := fragment(t, post(t, h, "/t/"+hexOf(1)+"/peers", "act-answer"), "act-answer")
	require.Equal(t, `<div id="act-answer" class="act-ans" role="status">Peer lookup requested at priority P0.`+queue+"\n"+
		`<button id="act-peers" type="submit" disabled hx-swap-oob="true">Refresh peers</button>`, strings.TrimSpace(frag), "the pressed button stays disabled")
	frag = fragment(t, post(t, h, "/movie/348/probe", "act-answer"), "act-answer")
	require.Contains(t, frag, `<div id="act-answer" class="act-ans dw" role="status"><i class="m"></i>Track probe requested at priority P0. No prober is connected now: the probe starts once one is.</div>`,
		"in amber while no prober is connected")
	probers.Seen("a", now)
	frag = fragment(t, post(t, h, "/movie/348/probe", "act-answer"), "act-answer")
	require.Contains(t, frag, `<div id="act-answer" class="act-ans" role="status">Track probe requested at priority P0.`+queue, "the one release of the title a probe can read")
	frag = fragment(t, post(t, h, "/t/"+hexOf(2)+"/probe", "act-answer"), "act-answer")
	require.Contains(t, frag, `>Cannot probe tracks: no file a probe can read.</div>`)
	require.Contains(t, frag, `<button id="act-probe" type="submit" disabled hx-swap-oob="true">Probe tracks</button>`)
	frag = fragment(t, post(t, h, "/tv/95396/peers", "act-answer"), "act-answer")
	require.Contains(t, frag, "Peer lookup requested for 2 releases at priority P0.")

	w := post(t, h, "/t/"+hexOf(1)+"/probe", "")
	require.Equal(t, http.StatusSeeOther, w.Code, "without htmx, back to the card")
	require.Equal(t, "/t/"+hexOf(1), w.Header().Get("Location"))
	require.Equal(t, http.StatusForbidden, post(t, h, "/t/"+hexOf(1)+"/probe", "act-probe", "Sec-Fetch-Site", "cross-site").Code)
	require.Equal(t, http.StatusNotFound, post(t, h, "/t/"+hexOf(99)+"/peers", "act-peers").Code)
	require.Equal(t, http.StatusNotFound, post(t, h, "/t/"+hexOf(1)+"/seed", "act-seed").Code)
	require.Equal(t, http.StatusNotFound, post(t, h, "/movie/1/peers", "act-peers").Code)
}

func TestProbesOff(t *testing.T) {
	h := handlerWith(t, "ru", Config{})
	for _, path := range []string{"/t/" + hexOf(1), "/t/" + hexOf(7), "/movie/348"} {
		body := page(t, get(t, h, path, ""))
		require.Contains(t, body, `<button id="act-peers" type="submit">Refresh peers</button>`, path)
		require.NotContains(t, body, `act-probe`, "%s: no probe button", path)
		require.NotContains(t, body, "Cannot probe tracks", "%s: nor a line about probes", path)
	}
	require.Equal(t, http.StatusNotFound, post(t, h, "/t/"+hexOf(1)+"/probe", "act-answer").Code)
	require.Equal(t, http.StatusNotFound, post(t, h, "/movie/348/probe", "act-answer").Code)

	body := page(t, get(t, h, "/", ""))
	require.NotContains(t, body, "Downloaded on the UTC day", "no probe meter")
	require.NotContains(t, body, `<h3>Probes`, "nor probe queue")
	require.Contains(t, body, `<h3>Metadata fetches</h3>`, "the other queues stay")

	st := &sniffarrv1.StatsResponse{QueueFetches: &sniffarrv1.TaskQueue{}, QueueLookups: &sniffarrv1.TaskQueue{},
		QueueProbes: &sniffarrv1.TaskQueue{Levels: []*sniffarrv1.TaskLevel{{Priority: 1, Tasks: 3}}}}
	kinds := func(qs []ui.TaskQueue) (out []string) {
		for _, q := range qs {
			out = append(out, q.Kind)
		}
		return out
	}
	require.Equal(t, []string{"fetch", "peers", "probe"}, kinds(taskQueues(st, true)))
	require.Equal(t, []string{"fetch", "peers"}, kinds(taskQueues(st, false)), "probes queued before they went off are not shown")
}

// The data page's block of features, with every feature on and with every
// one off, and the amber states of a feature on and not working.
func TestFeatures(t *testing.T) {
	h, probers := handlerTasks(t, "ru", Config{Probes: true, Classifier: true})
	body := page(t, get(t, h, "/", ""))
	for _, want := range []string{
		`<span>Classifier</span>`, `<span>working</span>`, `25.0% of the daily limit`,
		`<span class="dw"><i class="m"></i>no probers</span>`, "The probe queue grows: 0\u00a0tasks.",
		`<span>on: ru</span>`, `>P2: announced<`,
	} {
		require.Contains(t, body, want)
	}
	require.Contains(t, body, `aria-label="Classifier: `, "the classifier's meter")
	require.Contains(t, body, `/torrents?layer=classifier`, "and its layer")
	probers.Seen("a", now)
	require.Contains(t, page(t, get(t, h, "/", "")), "They read tracks from the first pieces of a release.", "a prober connected")

	body = page(t, get(t, handlerWith(t, "", Config{}), "/", ""))
	for _, want := range []string{
		`<span class="mu">off</span>`, "Turned on by SNIFFARR_CLASSIFY_API_KEY at start.",
		"Turned on by the -probe flag at start.", "Turned on at start, by the languages in SNIFFARR_AUDIO_LANGUAGES.",
	} {
		require.Contains(t, body, want)
	}
	for _, s := range []string{`aria-label="Classifier: `, `/torrents?layer=classifier`, "Voice-over: "} {
		require.NotContains(t, body, s)
	}
}

func TestRequestMatch(t *testing.T) {
	h := handler(t, nil)
	button := `<button id="act-match" type="submit">Match now</button>`
	require.Contains(t, page(t, get(t, h, "/t/"+hexOf(7), "")), button, "a title never looked up")
	require.Contains(t, page(t, get(t, h, "/t/"+hexOf(6), "")), button, "a title TMDB found nothing for")
	require.NotContains(t, page(t, get(t, h, "/t/"+hexOf(1), "")), button, "not a matched one")
	require.NotContains(t, page(t, get(t, h, "/movie/348", "")), button, "nor a work's")

	frag := fragment(t, post(t, h, "/t/"+hexOf(6)+"/match", "act-answer"), "act-answer")
	require.Equal(t, `<div id="act-answer" class="act-ans" role="status">TMDB still finds nothing.</div>`+"\n"+
		`<button id="act-match" type="submit" disabled hx-swap-oob="true">Match now</button>`, strings.TrimSpace(frag))
	frag = fragment(t, post(t, h, "/t/"+hexOf(7)+"/match", "act-answer"), "act-answer")
	require.Contains(t, frag, `>Matched to <a href="/movie/949">Heat</a> 1995.</div>`)
	body := page(t, get(t, h, "/t/"+hexOf(7), ""))
	require.NotContains(t, body, button, "matched now")
	require.Contains(t, body, `<a class="tm-stretch" href="/movie/949?from=%2Ft%2F`+hexOf(7)+`">Heat</a>`)
	require.Contains(t, body, `<span class="jw">Title matched</span>`, "journaled")
	require.Contains(t, page(t, get(t, h, "/t/"+hexOf(7)+"?lang=ru", "")), "<span>кто запросил</span><span>запрос</span>", "with the trigger")
	w := post(t, h, "/t/"+hexOf(6)+"/match", "")
	require.Equal(t, http.StatusSeeOther, w.Code, "without htmx, back to the card")

	for code, want := range map[connect.Code]string{connect.CodeResourceExhausted: "budget", connect.CodeFailedPrecondition: "off", connect.CodeUnavailable: "unavailable"} {
		s := &server{cfg: Config{Browse: failingBrowse{code: code}}}
		got, err := s.requestMatch(context.Background(), nil, "en")
		require.NoError(t, err)
		require.Equal(t, want, got.Failure)
	}
	_, err := (&server{cfg: Config{Browse: failingBrowse{code: connect.CodeInternal}}}).requestMatch(context.Background(), nil, "en")
	require.Error(t, err, "any other failure is the page's")
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
	body := page(t, get(t, h, "/", ""))
	require.True(t, strings.HasPrefix(body, "<!doctype html>"))
	require.Contains(t, body, `hx-get="/"`, "it refreshes itself from /")
	require.Contains(t, body, `<a href="/torrents?kind=movie">movie</a></td><td class="r nw">5</td>`)
	require.Contains(t, body, `<a href="/torrents?kind=other">other</a></td><td class="r nw">1</td>`, "music counts as other")
	require.Contains(t, body, `<a href="/torrents?kind=unknown">unknown</a></td><td class="r nw">1</td>`)
	require.Contains(t, body, "Tokens used on the UTC day: 250 of 1,000. On pace for 50.0% by the end of the day. Awaiting the classifier: 1.", "tokens used of the daily limit, doubled by midnight at noon")
	require.Contains(t, body, `<b class="big">25.0%</b><span class="xs mu">≈ 50.0% by 00:00 UTC</span>`)
	require.Contains(t, body, `<b class="big">61.0%</b><span class="xs dw">≈ 122.0% by 00:00 UTC</span>`, "the probe bytes outrun the limit")
	require.Contains(t, body, `<span class="proj" style="width: 100.0%"></span>`, "the outline stops at the limit")
	require.Contains(t, body, "Downloaded on the UTC day: 61.0 GB of 100 GB; uploaded 38.0 GB.", "piece data probes downloaded and the fetchers uploaded today")
	require.NotContains(t, body, `<span class="xs dw">updated`, "counts 7 minutes old are fresh at the API's 5-minute interval")
	require.Contains(t, body, "Refresh. Data as of Oct 20, 11:53, updated 7 min ago.", "when the API computed the counts")
	require.Contains(t, body, "New torrents per hour, 48 hours")
	require.Equal(t, 3, strings.Count(body, `<i class="bar partial"`), "the current hour, day and week are unfinished")
	require.Contains(t, body, " · in progress, on pace for ")
	require.Contains(t, body, `<i class="proj" style="height: `)
	require.Contains(t, body, `hx-trigger="every 30s [document.visibilityState=='visible'], tz"`, "the script's event once the tz cookie is set")
	body = page(t, get(t, h, "/", "", "Cookie", "tz=Asia/Tokyo"))
	require.Contains(t, body, `<span class="tc" aria-hidden="true">Oct 20, 20:00: `, "the hour of the computation, 11:53 UTC, in the cookie's zone")
	require.Equal(t, http.StatusOK, get(t, h, "/", "", "Cookie", "tz=Mars/Olympus").Code, "an unknown zone is the process's")
	require.Contains(t, body, "Voice-over: ru")
	require.Contains(t, body, `<h2><span class="tt" tabindex="0" aria-label="Task queue: A priority is done once`, "the note in a tooltip on the heading")
	require.Contains(t, body, `</span></span> <span class="mu">3</span></h2>`, "the count after the heading")
	require.NotContains(t, body, `<p class="mu">A priority is done`, "the note is not a paragraph")
	require.Contains(t, body, `<h3>Metadata fetches</h3><span class="mu">3 queued</span>`)
	require.NotContains(t, body, `<h3>Probes`, "no probes queued and none done")
	require.Contains(t, body, `<h3>Peer lookups</h3><span class="mu">0 queued</span>`, "none queued, one done")
	require.Contains(t, body, `<i class="q1" style="flex-grow: 1"></i><i class="q3" style="flex-grow: 2"></i>`, "a segment per priority with tasks")
	require.Contains(t, body, "<td class=\"r\">1\u00a0min</td>", "one fetch at P1 at a pace of one a minute")
	require.Contains(t, body, "<td class=\"r\">3\u00a0min</td>", "three at P3 or ahead")
	require.Contains(t, body, `<span class="mu">—</span>`, "nothing at P0")
	require.NotContains(t, body, "Grafana")
	frag := fragment(t, get(t, h, "/", "data"), "data")
	require.True(t, strings.HasPrefix(strings.TrimSpace(frag), `<main id="data"`))
	require.Equal(t, http.StatusOK, get(t, h, "/ui/vendor/htmx.min.js", "").Code)
}

func TestCharts(t *testing.T) {
	// A Tuesday, 12:00 UTC, 15:00 in a zone three hours ahead.
	hours := make([]int64, 48)
	hours[0] = 1  // 15:00 local, today
	hours[15] = 1 // 00:00 local, today still
	hours[16] = 1 // 23:00 local yesterday, this week
	hours[40] = 1 // 23:00 local on Sunday, last week
	east := time.FixedZone("east", 3*3600)
	hourly, daily, weekly := charts(ui.Base{Locale: libui.Locale{Lang: "ru", Loc: east, Now: now}}, now, hours)

	require.Len(t, hourly.Bars, 48)
	last := hourly.Bars[47]
	require.Equal(t, ui.Bar{Label: "20.10 15:00", Value: 1, Partial: true}, ui.Bar{Label: last.Label, Value: last.Value, Partial: last.Partial})
	require.Zero(t, last.Projected, "the hour has just begun and the hour before was empty")
	require.Equal(t, []ui.XLabel{
		{Text: "18.10 16:00", X: 0}, {Text: "19.10 04:00", X: 26}, {Text: "19.10 16:00", X: 51}, {Text: "20.10 04:00", X: 76}, {Text: "20.10 15:00", X: 100},
	}, hourly.XLabels, "each label under its bar, the first and the last at the edges")

	require.Len(t, daily.Bars, 30)
	require.Equal(t, "20.10", daily.Bars[29].Label)
	require.True(t, daily.Bars[29].Partial)
	require.EqualValues(t, 2, daily.Bars[29].Value, "today in the zone runs from 21:00 UTC yesterday")
	require.EqualValues(t, 3, daily.Bars[29].Projected, "2 over the last 24 hours at 0.16, 2 by 15:00, 3.2 by midnight, at 0.84")
	require.Equal(t, int64(5), daily.YTicks[0], "the scale holds the projection, 3 over the bars' 2")
	require.EqualValues(t, 1, daily.Bars[28].Value)
	require.EqualValues(t, 1, daily.Bars[27].Value, "Sunday")
	require.Equal(t, "18.10", daily.Bars[27].Label)
	require.Equal(t, "20.10", daily.XLabels[len(daily.XLabels)-1].Text)
	require.Equal(t, "12.10", daily.XLabels[len(daily.XLabels)-2].Text, "the day before the last is not labelled beside it")

	require.Len(t, weekly.Bars, 26)
	require.Equal(t, "19.10", weekly.Bars[25].Label, "the Monday of this week")
	require.True(t, weekly.Bars[25].Partial)
	require.EqualValues(t, 3, weekly.Bars[25].Value)
	require.EqualValues(t, 7, weekly.Bars[25].Projected, "3 over the last 7 days at 0.55, 3 a day and five eighths in, 13 a week, at 0.45")
	require.EqualValues(t, 1, weekly.Bars[24].Value)
	require.Equal(t, "12.10", weekly.Bars[24].Label)
	require.False(t, weekly.Bars[24].Partial)
	require.Equal(t, int64(1), weekly.Max.Value, "the unfinished week is out of max and min")

	hourly, daily, weekly = charts(ui.Base{Locale: libui.Locale{Lang: "en", Loc: east, Now: now}}, now, hours)
	require.Equal(t, "Oct 20, 15:00", hourly.Bars[47].Label, "labels in the page's locale")
	require.Equal(t, "Oct 20", daily.Bars[29].Label)
	require.Equal(t, "Oct 19", weekly.Bars[25].Label)
}

func TestProjection(t *testing.T) {
	require.Zero(t, periodWeight(0))
	require.InDelta(t, 0.75, periodWeight(0.5), 1e-9, "the period outweighs the window by the middle")
	require.InDelta(t, 1, periodWeight(1), 1e-9)
	require.InDelta(t, 1, periodWeight(2), 1e-9, "past the end is the end")
	require.Zero(t, periodWeight(-1))
	for d := 0.05; d < 1; d += 0.05 {
		require.Greater(t, periodWeight(d), periodWeight(d-0.05))
	}
	require.Equal(t, 5.0, projection(5, 1, 0), "nothing of the period has passed")
	require.InDelta(t, 0.25*4+0.75*6, projection(4, 3, 0.5), 1e-9)
	require.InDelta(t, 9.0/8*math.Log(9), projection(0, 1, 1e-9), 1e-6, "one sighting in the first instant stays bounded")
	require.InDelta(t, 7, projection(100, 7, 1), 1e-9, "a finished period is its count")
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
	require.Contains(t, page(t, get(t, h, "/", "", TokenHeader, "ru-token")), `<html lang="en">`, "no lookup without Locales")
	h = handler(t, locales{})
	require.Contains(t, page(t, get(t, h, "/", "")), `<html lang="en">`)
	require.Contains(t, page(t, get(t, h, "/", "", TokenHeader, "ru-token")), `<html lang="ru">`, "the person's locale")
	require.Contains(t, page(t, get(t, h, "/", "", TokenHeader, "other")), `<html lang="en">`, "a failed lookup falls back")
	require.Contains(t, page(t, get(t, h, "/?lang=en", "", TokenHeader, "ru-token")), `<html lang="en">`, "the language picked wins")

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
	for _, path := range []string{"/torrents", "/torrents?vl=ru&voice=explicit", "/top", "/top?voice=explicit", "/movie/348", "/movie/348?voice=explicit", "/t/" + hexOf(1), "/"} {
		body := page(t, get(t, h, path, ""))
		forbidden := []string{`name="vl"`, `name="voice"`, "<th>Voice-over</th>", "Voice-over: ", "<b>Rus</b>"}
		if !strings.Contains(path, "voice=") {
			forbidden = append(forbidden, "voice=")
		}
		for _, s := range forbidden {
			require.NotContains(t, body, s, "%s has %q", path, s)
		}
	}
	require.Contains(t, page(t, get(t, h, "/torrents?vl=ru&voice=explicit", "")), severance, "a voice filter in the query narrows nothing")
	require.Contains(t, page(t, get(t, h, "/movie/348?voice=explicit", "")), chuzhoy)
	require.Contains(t, page(t, get(t, h, "/top", "")), `<a href="/movie/348?from=%2Ftop">Alien</a>`)
	require.Contains(t, page(t, get(t, h, "/t/"+hexOf(1), "")), "magnet:?xt=urn:btih:"+hexOf(1))
}

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
