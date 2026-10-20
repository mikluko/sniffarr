package torznab

import (
	"context"
	"encoding/xml"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/database"
	"github.com/mikluko/sniffarr/internal/search"
	"github.com/mikluko/sniffarr/internal/store"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

type stored struct {
	name, kind, key string
}

func serve(t *testing.T, torrents ...stored) (*httptest.Server, *store.Store) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	s, err := store.New(ctx, db, store.Options{TMDB: true})
	require.NoError(t, err)
	for i, x := range torrents {
		tr := store.Torrent{Hash: store.Hash{byte(i + 1)}, Name: x.name, Size: int64(i+1) << 30, Discovery: "sample",
			SeenAt: t0.Add(time.Duration(i) * time.Minute), Kind: x.kind, TitleKey: x.key}
		if x.kind != "" {
			tr.KindLayer = store.LayerRules
		}
		_, _, err := s.Insert(ctx, tr)
		require.NoError(t, err)
	}
	h := &Handler{Search: &search.Service{Store: s, Log: slog.New(slog.DiscardHandler)}, Token: "key", Log: slog.New(slog.DiscardHandler)}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, s
}

func get(t *testing.T, srv *httptest.Server, params ...string) string {
	t.Helper()
	q := url.Values{"apikey": {"key"}}
	for i := 0; i < len(params); i += 2 {
		q.Set(params[i], params[i+1])
	}
	res, err := http.Get(srv.URL + "/torznab/api?" + q.Encode())
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, "application/xml; charset=utf-8", res.Header.Get("Content-Type"))
	b, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	return string(b)
}

type parsedItem struct {
	Title     string `xml:"title"`
	GUID      string `xml:"guid"`
	Link      string `xml:"link"`
	PubDate   string `xml:"pubDate"`
	Enclosure struct {
		URL    string `xml:"url,attr"`
		Length int64  `xml:"length,attr"`
		Type   string `xml:"type,attr"`
	} `xml:"enclosure"`
	Attrs []struct {
		Name  string `xml:"name,attr"`
		Value string `xml:"value,attr"`
	} `xml:"http://torznab.com/schemas/2015/feed attr"`
}

func (i parsedItem) attrs() map[string]string {
	out := map[string]string{}
	for _, a := range i.Attrs {
		out[a.Name] = a.Value
	}
	return out
}

func items(t *testing.T, body string) []parsedItem {
	t.Helper()
	var f struct {
		XMLName xml.Name     `xml:"rss"`
		Items   []parsedItem `xml:"channel>item"`
	}
	require.NoError(t, xml.Unmarshal([]byte(body), &f), body)
	return f.Items
}

func titles(is []parsedItem) []string {
	var out []string
	for _, i := range is {
		out = append(out, i.Title)
	}
	return out
}

func parseError(t *testing.T, body string) (int, string) {
	t.Helper()
	var e struct {
		XMLName     xml.Name `xml:"error"`
		Code        int      `xml:"code,attr"`
		Description string   `xml:"description,attr"`
	}
	require.NoError(t, xml.Unmarshal([]byte(body), &e), body)
	return e.Code, e.Description
}

func TestCaps(t *testing.T) {
	srv, _ := serve(t)
	var c struct {
		Server struct {
			Title string `xml:"title,attr"`
		} `xml:"server"`
		Limits struct {
			Default int `xml:"default,attr"`
			Max     int `xml:"max,attr"`
		} `xml:"limits"`
		Search      mode `xml:"searching>search"`
		TVSearch    mode `xml:"searching>tv-search"`
		MovieSearch mode `xml:"searching>movie-search"`
		MusicSearch mode `xml:"searching>music-search"`
		Categories  []struct {
			ID      int      `xml:"id,attr"`
			Name    string   `xml:"name,attr"`
			Subcats []subcat `xml:"subcat"`
		} `xml:"categories>category"`
	}
	require.NoError(t, xml.Unmarshal([]byte(get(t, srv, "t", "caps")), &c))
	require.Equal(t, "sniffarr", c.Server.Title)
	require.Equal(t, 100, c.Limits.Default)
	require.Equal(t, 500, c.Limits.Max)
	require.Equal(t, "yes", c.Search.Available)
	require.Equal(t, "q", c.Search.Params)
	require.Equal(t, "q,tvdbid,tmdbid,season,ep", c.TVSearch.Params)
	require.Equal(t, "q,tmdbid,imdbid", c.MovieSearch.Params)
	require.Equal(t, "no", c.MusicSearch.Available)
	require.Len(t, c.Categories, 2)
	require.Equal(t, 2000, c.Categories[0].ID)
	require.Equal(t, []subcat{{2030, "Movies/SD"}, {2040, "Movies/HD"}, {2045, "Movies/UHD"}}, c.Categories[0].Subcats)
	require.Equal(t, 5000, c.Categories[1].ID)
	require.Equal(t, []subcat{{5030, "TV/SD"}, {5040, "TV/HD"}, {5045, "TV/UHD"}}, c.Categories[1].Subcats)
}

func TestSearchByID(t *testing.T) {
	ctx := context.Background()
	srv, s := serve(t,
		stored{"Alien.1979.2160p.UHD.BluRay", "movie", "y:alien 1979"},
		stored{"Alien.1979.1080p.BluRay", "movie", "y:alien 1979"},
		stored{"Alien.S01E01.720p", "series", "s:alien"},
		stored{"Alien.1979.DVDRip", "movie", "y:alien 1979"},
	)
	require.NoError(t, s.RecordTitle(ctx, "y:alien 1979", store.Title{Year: 1979, Confidence: 1, Layer: store.LayerTMDB,
		IDs: []store.ExternalID{{Provider: "tmdb", ID: "348"}, {Provider: "imdb", ID: "tt0078748"}}}, t0))
	require.NoError(t, s.RecordTitle(ctx, "s:alien", store.Title{Confidence: 1, Layer: store.LayerTMDB,
		IDs: []store.ExternalID{{Provider: "tmdb", ID: "348"}, {Provider: "tvdb", ID: "81189"}}}, t0))

	got := items(t, get(t, srv, "t", "movie", "tmdbid", "348"))
	require.Equal(t, []string{"Alien.1979.DVDRip", "Alien.1979.1080p.BluRay", "Alien.1979.2160p.UHD.BluRay"}, titles(got),
		"movies only: the series' TMDB ID is another work's")
	uhd := got[2]
	magnet := "magnet:?xt=urn:btih:0100000000000000000000000000000000000000&dn=Alien.1979.2160p.UHD.BluRay"
	require.Equal(t, magnet, uhd.GUID)
	require.Equal(t, magnet, uhd.Link)
	require.Equal(t, magnet, uhd.Enclosure.URL)
	require.EqualValues(t, 1<<30, uhd.Enclosure.Length)
	require.Equal(t, "application/x-bittorrent", uhd.Enclosure.Type)
	require.Equal(t, t0.Format(time.RFC1123Z), uhd.PubDate)
	require.Equal(t, map[string]string{
		"category": "2045", "size": "1073741824", "infohash": "0100000000000000000000000000000000000000", "magneturl": magnet,
		"tmdbid": "348", "imdbid": "0078748",
	}, uhd.attrs())
	require.Equal(t, "2040", got[1].attrs()["category"])
	require.Equal(t, "2030", got[0].attrs()["category"])

	for _, imdb := range []string{"tt0078748", "0078748", "78748"} {
		require.Len(t, items(t, get(t, srv, "t", "movie", "imdbid", imdb)), 3, imdb)
	}
	tv := items(t, get(t, srv, "t", "tvsearch", "tvdbid", "81189", "season", "1", "ep", "1"))
	require.Equal(t, []string{"Alien.S01E01.720p"}, titles(tv))
	require.Equal(t, "5040", tv[0].attrs()["category"])
	require.Equal(t, "81189", tv[0].attrs()["tvdbid"])
	require.Empty(t, items(t, get(t, srv, "t", "tvsearch", "tmdbid", "348", "cat", "2000")), "a movie category in a TV search")
	require.Len(t, items(t, get(t, srv, "t", "search", "q", "alien", "cat", "5000,5040")), 1)
}

func TestSearchOnlyMoviesAndSeries(t *testing.T) {
	srv, _ := serve(t,
		stored{"Heat.1995.1080p", "movie", "y:heat 1995"},
		stored{"Moon.Safari.1998.FLAC", "music", "y:moon safari 1998"},
		stored{"Collection", "", ""},
		stored{"Severance.S02E01", "series", "s:severance"},
	)
	require.Equal(t, []string{"Severance.S02E01", "Heat.1995.1080p"}, titles(items(t, get(t, srv, "t", "search"))))
}

func TestOffsetLimit(t *testing.T) {
	var torrents []stored
	for _, n := range []string{"A.2001.1080p", "B.2002.1080p", "C.2003.1080p", "D.2004.1080p", "E.2005.1080p"} {
		torrents = append(torrents, stored{n, "movie", ""})
	}
	srv, _ := serve(t, torrents...)
	require.Equal(t, []string{"D.2004.1080p", "C.2003.1080p"}, titles(items(t, get(t, srv, "t", "movie", "offset", "1", "limit", "2"))))
	require.Empty(t, items(t, get(t, srv, "t", "movie", "offset", "10")))
	require.Len(t, items(t, get(t, srv, "t", "movie")), 5)
}

func TestEncoding(t *testing.T) {
	name := `Чужой & "Co" <1979> 1080p`
	srv, _ := serve(t, stored{name, "movie", ""})
	body := get(t, srv, "t", "search", "q", "чужой")
	require.Contains(t, body, "Чужой &amp; ")
	got := items(t, body)
	require.Len(t, got, 1)
	require.Equal(t, name, got[0].Title)
	require.Contains(t, got[0].Link, "dn=%D0%A7%D1%83%D0%B6%D0%BE%D0%B9%20%26%20")
}

func TestErrors(t *testing.T) {
	srv, _ := serve(t)
	res, err := http.Get(srv.URL + "/torznab/api?t=caps&apikey=wrong")
	require.NoError(t, err)
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	code, desc := parseError(t, string(b))
	require.Equal(t, 100, code)
	require.Equal(t, "Incorrect user credentials", desc)

	for _, tt := range []struct {
		params []string
		code   int
	}{
		{nil, 200},
		{[]string{"t", "music"}, 202},
		{[]string{"t", "movie", "limit", "many"}, 201},
		{[]string{"t", "movie", "tmdbid", "abc"}, 201},
		{[]string{"t", "search", "cat", "movies"}, 201},
	} {
		code, _ := parseError(t, get(t, srv, tt.params...))
		require.Equal(t, tt.code, code, tt.params)
	}
}
