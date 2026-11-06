package rutor

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/feed"
)

// page is two rows as rutor.info/browse/0/0/0/0 served them on 2026-11-06,
// after the pager and the header row, and a row without a magnet link.
const page = `<html><body><p align="center"><b>&lt;&lt;&nbsp;Предв.</b> <a href="/browse/1/0/0/0"><b>След.&nbsp;&gt;&gt;</b></a></p>
<div id="index"><table width="100%"><tr class="backgr"><td width="10px">Добавлен</td><td colspan="2">Название</td><td width="1px">Размер</td><td width="1px">Пиры</td></tr>
<tr class="gai"><td>06&nbsp;Ноя&nbsp;26</td><td ><a class="downgif" href="//d.rutor.info/download/936460"><img src="//cdnbunny.org/i/d.gif" alt="D" /></a><a href="magnet:?xt=urn:btih:57435a36763b0c9177484958060b3a7be33e2243&dn=rutor.info&tr=udp://opentor.net:6969"><img src="//cdnbunny.org/i/m.png" alt="M" /></a>
<a href="/torrent/936460/mjeggi-mury_maggie-moores-2023-bdrip-1080p">Мэгги Мур(ы) / Maggie Moore(s) (2023) BDRip 1080p от DoMiNo &amp; селезень | D | CPI Films </a></td> <td align="right">1<img src="//cdnbunny.org/i/com.gif" alt="C" /></td>
<td align="right">4.92&nbsp;GB</td><td align="center"><span class="green"><img src="//cdnbunny.org/t/arrowup.gif" alt="S" />&nbsp;3</span>&nbsp;<img src="//cdnbunny.org/t/arrowdown.gif" alt="L" /><span class="red">&nbsp;2</span></td></tr>
<tr class="tum"><td>05&nbsp;Ноя&nbsp;26</td><td ><a href="magnet:?xt=urn:btih:ee9b7572e848477bbbcfd00967cdfdfd9efdfcb4&dn=rutor.info"><img src="m.png" alt="M" /></a>
<a href="/torrent/1104062/gejl-dotri">Гейл Дотри и голливудский секс-абонемент / Gail Daughtry and the Celebrity Sex Pass (2026) WEB-DLRip-AVC</a></td>
<td align="right">700&nbsp;MB</td><td align="center">0</td></tr>
<tr class="gai"><td>05&nbsp;Ноя&nbsp;26</td><td><a href="/torrent/1/no-magnet">No magnet</a></td><td align="right">1&nbsp;GB</td></tr>
</table></div></body></html>`

type memState map[string]bool

func (m memState) Done(_ context.Context, key string) (bool, error) { return m[key], nil }
func (m memState) Mark(_ context.Context, key string) error         { m[key] = true; return nil }

func hashOf(t *testing.T, s string) (h [20]byte) {
	t.Helper()
	_, err := hex.Decode(h[:], []byte(s))
	require.NoError(t, err)
	return h
}

func TestParse(t *testing.T) {
	items, err := Parse(strings.NewReader(page), "https://rutor.info")
	require.NoError(t, err)
	require.Equal(t, []feed.Item{
		{Hash: hashOf(t, "57435a36763b0c9177484958060b3a7be33e2243"), Title: "Мэгги Мур(ы) / Maggie Moore(s) (2023) BDRip 1080p от DoMiNo & селезень | D | CPI Films",
			URL: "https://rutor.info/torrent/936460/mjeggi-mury_maggie-moores-2023-bdrip-1080p", Size: 5282809774, At: time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)},
		{Hash: hashOf(t, "ee9b7572e848477bbbcfd00967cdfdfd9efdfcb4"), Title: "Гейл Дотри и голливудский секс-абонемент / Gail Daughtry and the Celebrity Sex Pass (2026) WEB-DLRip-AVC",
			URL: "https://rutor.info/torrent/1104062/gejl-dotri", Size: 734003200, At: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)},
	}, items, "the row without a magnet link left out")
	require.Equal(t, time.Time{}, date("вчера"))
}

// The first pass reads Depth pages, marked done; later ones the first page.
func TestFetch(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/browse/0/0/0/0", "/browse/0/1/0/0":
			_, _ = w.Write([]byte(page))
		case "/browse/1/0/0/0":
			_, _ = w.Write([]byte(strings.ReplaceAll(page, "ee9b7572e848477bbbcfd00967cdfdfd9efdfcb4", "0000000000000000000000000000000000000002")))
		case "/browse/2/0/0/0":
			_, _ = w.Write([]byte("<html><body><table></table></body></html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	state := memState{}
	src := &Source{Config: Config{URL: srv.URL, Depth: 3}}
	require.Equal(t, "rutor", src.Name())
	items, err := src.Fetch(context.Background(), state)
	require.NoError(t, err)
	require.Len(t, items, 3, "two pages of two, one release on both, and an empty third ends the pass")
	require.Equal(t, memState{"deep": true}, state)
	items, err = src.Fetch(context.Background(), state)
	require.NoError(t, err)
	require.Len(t, items, 2, "the first page alone")
	mu.Lock()
	require.Equal(t, []string{"/browse/0/0/0/0", "/browse/1/0/0/0", "/browse/2/0/0/0", "/browse/0/0/0/0"}, paths)
	mu.Unlock()

	items, err = (&Source{Config: Config{URL: srv.URL, Categories: []int{1}}}).Fetch(context.Background(), memState{"deep": true})
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, "1", items[0].Category, "a category asked for is the item's")

	_, err = (&Source{Config: Config{URL: srv.URL, Categories: []int{7}}}).Fetch(context.Background(), memState{"deep": true})
	require.EqualError(t, err, fmt.Sprintf("category 7 page 0: %s/browse/0/7/0/0: 404 Not Found", srv.URL))
}
