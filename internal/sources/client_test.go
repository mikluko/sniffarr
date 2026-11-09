package sources

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"sarak.sh/mikluko/libarr/torznab"

	"sarak.sh/mikluko/sniffarr/internal/store"
)

// spidarrFeed is a feed as spidarr answers it, its spidarr:listing elements
// included.
const spidarrFeed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom" xmlns:torznab="http://torznab.com/schemas/2015/feed" xmlns:spidarr="https://sarak.sh/mikluko/spidarr">
  <channel>
    <title>spidarr</title>
    <item>
      <title>Мой папа – мэр / Сезон: 1 / Серии: 1-8 из 8 [2026, WEBRip 1080p]</title>
      <guid isPermaLink="false">magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&amp;dn=x</guid>
      <link>magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&amp;dn=x</link>
      <comments>https://rutracker.org/forum/viewtopic.php?t=6925915</comments>
      <pubDate>Sun, 08 Nov 2026 10:00:00 +0000</pubDate>
      <size>9223372036</size>
      <category>5040</category>
      <enclosure url="magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&amp;dn=x" length="9223372036" type="application/x-bittorrent"/>
      <torznab:attr name="category" value="5040"/>
      <torznab:attr name="category" value="5000"/>
      <torznab:attr name="size" value="9223372036"/>
      <torznab:attr name="infohash" value="0123456789abcdef0123456789abcdef01234567"/>
      <torznab:attr name="magneturl" value="magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&amp;dn=x"/>
      <torznab:attr name="source" value="rutracker"/>
      <torznab:attr name="imdb" value="0078748"/>
      <torznab:attr name="tmdbid" value="348"/>
      <torznab:attr name="tvdbid" value="121361"/>
      <spidarr:listing source="rutracker" url="https://rutracker.org/forum/viewtopic.php?t=6925915" title="x"/>
    </item>
  </channel>
</rss>`

func TestLatest(t *testing.T) {
	var mu sync.Mutex
	var got []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r)
		mu.Unlock()
		_, _ = w.Write([]byte(spidarrFeed))
	}))
	t.Cleanup(srv.Close)
	token := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(token, []byte("first\n"), 0o600))
	c := &Client{Source: Source{Name: "spidarr", URL: srv.URL + "/api?x=1", APIKey: "k", TokenFile: token}}

	items, err := c.Latest(t.Context(), 100, 50)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NoError(t, os.WriteFile(token, []byte("second"), 0o600))
	_, err = c.Latest(t.Context(), 0, 50)
	require.NoError(t, err)

	require.Len(t, got, 2)
	require.Equal(t, "/api", got[0].URL.Path)
	require.Equal(t, url.Values{"x": {"1"}, "t": {"search"}, "offset": {"100"}, "limit": {"50"}, "apikey": {"k"}}, got[0].URL.Query(),
		"the latest listings: a search without q")
	require.Equal(t, "Bearer first", got[0].Header.Get("Authorization"))
	require.Equal(t, "Bearer second", got[1].Header.Get("Authorization"), "the token file is read for each request")

	l, ok := Listing("spidarr", items[0])
	require.True(t, ok)
	h, _ := hexHash("0123456789abcdef0123456789abcdef01234567")
	require.Equal(t, store.Listing{Source: "spidarr", Hash: h, Title: "Мой папа – мэр / Сезон: 1 / Серии: 1-8 из 8 [2026, WEBRip 1080p]",
		Comments: "https://rutracker.org/forum/viewtopic.php?t=6925915", Categories: []int{5040, 5000}, Size: 9223372036,
		IMDb: "tt0078748", TMDBID: 348, TVDBID: 121361, PublishedAt: time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)}, l)
}

func TestLatestFailures(t *testing.T) {
	for _, c := range []struct {
		name    string
		status  int
		body    string
		refusal bool
	}{
		{"a Newznab error", http.StatusOK, `<?xml version="1.0"?><error code="100" description="Incorrect user credentials"/>`, true},
		{"a status other than 2xx", http.StatusBadGateway, "", false},
		{"no feed", http.StatusOK, `<html><body>login</body></html>`, false},
		{"no XML", http.StatusOK, `{}`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			t.Cleanup(srv.Close)
			_, err := (&Client{Source: Source{URL: srv.URL}}).Latest(t.Context(), 0, 10)
			require.Error(t, err)
			require.Equal(t, c.refusal, isRefusal(err))
		})
	}
}

func TestLatestTokenFileMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("no request without the token") }))
	t.Cleanup(srv.Close)
	_, err := (&Client{Source: Source{URL: srv.URL, TokenFile: filepath.Join(t.TempDir(), "none")}}).Latest(t.Context(), 0, 10)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestListing(t *testing.T) {
	const hexed = "0123456789ABCDEF0123456789ABCDEF01234567"
	h, _ := hexHash(hexed)
	magnet := "magnet:?xt=urn:btih:" + hexed
	for _, c := range []struct {
		name string
		it   torznab.Item
		want store.Listing
		ok   bool
	}{
		{"by the magneturl attr", torznab.Item{Title: "a", Attrs: []torznab.Attr{{Name: "magneturl", Value: magnet}, {Name: "size", Value: "42"}}},
			store.Listing{Source: "s", Hash: h, Title: "a", Size: 42}, true},
		{"by the GUID", torznab.Item{GUID: torznab.GUID{Value: magnet}}, store.Listing{Source: "s", Hash: h}, true},
		{"by the enclosure", torznab.Item{Link: "https://x/t.torrent", Enclosure: torznab.Enclosure{URL: magnet}}, store.Listing{Source: "s", Hash: h}, true},
		{"an infohash attr that is none", torznab.Item{Attrs: []torznab.Attr{{Name: "infohash", Value: "xyz"}}, Link: magnet}, store.Listing{Source: "s", Hash: h}, true},
		{"imdbid with its tt", torznab.Item{Link: magnet, Attrs: []torznab.Attr{{Name: "imdbid", Value: "tt0113277"}}},
			store.Listing{Source: "s", Hash: h, IMDb: "tt0113277"}, true},
		{"an ID that is no number", torznab.Item{Link: magnet, Attrs: []torznab.Attr{{Name: "imdb", Value: "n/a"}, {Name: "tmdbid", Value: "-1"}}},
			store.Listing{Source: "s", Hash: h}, true},
		{"a link to a torrent file", torznab.Item{Link: "https://x/t.torrent"}, store.Listing{}, false},
		{"a magnet link of v2 only", torznab.Item{Link: "magnet:?xt=urn:btmh:1220" + hexed + hexed[:24]}, store.Listing{}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Listing("s", c.it)
			require.Equal(t, c.ok, ok)
			require.Equal(t, c.want, got)
		})
	}
}
