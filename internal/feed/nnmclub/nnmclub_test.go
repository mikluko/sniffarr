package nnmclub

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/text/encoding/charmap"

	"github.com/mikluko/sniffarr/internal/feed"
)

// rss is two items as nnmclub.to/forum/rss.php served them on 2026-11-05,
// and one that is not a topic.
const rss = `<?xml version="1.0" encoding="windows-1251" ?>
<rss version="2.0" xmlns:turbo="http://turbo.yandex.ru">
<channel>
<title>NNM-Club</title>
<link>https://nnmclub.to/forum/</link>
<item turbo="true">
<title>Зарубежные Новинки (SD, DVD) :: Вышка 2 / Fall 2: Deadpoint (2026) WEB-DLRip [H.264]</title>
<link>https://nnmclub.to/forum/viewtopic.php?t=1898322</link>
<pubDate>Thu, 05 Nov 2026 18:30:55 GMT</pubDate>
<guid isPermaLink="true">https://nnmclub.to/forum/viewtopic.php?t=1898322</guid>
<turbo:content><![CDATA[Автор: Leon-masl]]></turbo:content>
</item>
<item turbo="true">
<title>Зарубежные Новинки (SD, DVD) :: Горькое Рождество / Amarga Navidad (2026) BDRip [H.264]</title>
<link>https://nnmclub.to/forum/viewtopic.php?t=1898300</link>
<pubDate>Thu, 05 Nov 2026 17:00:00 GMT</pubDate>
</item>
<item turbo="true">
<title>Новости :: Что-то без раздачи</title>
<link>https://nnmclub.to/forum/viewforum.php?f=1</link>
<pubDate>Thu, 05 Nov 2026 16:00:00 GMT</pubDate>
</item>
</channel>
</rss>
`

// topic is the part of a topic page that holds the magnet link.
const topicPage = `<html><head><meta http-equiv="Content-Type" content="text/html; charset=windows-1251"><title>Вышка 2 :: NNM-Club</title></head>
<body><table><tr><td class="gensmall"><a href="profile.php?mode=register">Зарегистрируйтесь</a><br />16.5&nbsp;KB<br />
<!--noindex--><a rel="nofollow" href="magnet:?xt=urn:btih:56DEE5B6B78025762EF3091308DB64CE2C34BD66" title="Примагнититься"><img src="magnet48.png" /></a><!--/noindex--></td></tr></table></body></html>`

// cp1251 encodes s as the forum does.
func cp1251(t *testing.T, s string) []byte {
	t.Helper()
	b, err := charmap.Windows1251.NewEncoder().Bytes([]byte(s))
	require.NoError(t, err)
	return b
}

type memState map[string]bool

func (m memState) Done(_ context.Context, key string) (bool, error) { return m[key], nil }
func (m memState) Mark(_ context.Context, key string) error         { m[key] = true; return nil }

func TestParseRSS(t *testing.T) {
	topics, err := ParseRSS(strings.NewReader(string(cp1251(t, rss))))
	require.NoError(t, err)
	require.Equal(t, []topic{
		{ID: 1898322, Title: "Вышка 2 / Fall 2: Deadpoint (2026) WEB-DLRip [H.264]", Category: "Зарубежные Новинки (SD, DVD)",
			URL: "https://nnmclub.to/forum/viewtopic.php?t=1898322", At: time.Date(2026, 11, 5, 18, 30, 55, 0, time.UTC)},
		{ID: 1898300, Title: "Горькое Рождество / Amarga Navidad (2026) BDRip [H.264]", Category: "Зарубежные Новинки (SD, DVD)",
			URL: "https://nnmclub.to/forum/viewtopic.php?t=1898300", At: time.Date(2026, 11, 5, 17, 0, 0, 0, time.UTC)},
	}, topics, "the charset decoded, the forum split off the title, the item without a topic left out")
}

func TestParseTopic(t *testing.T) {
	h, ok, err := ParseTopic(strings.NewReader(string(cp1251(t, topicPage))))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "56dee5b6b78025762ef3091308db64ce2c34bd66", hex.EncodeToString(h[:]))
	_, ok, err = ParseTopic(strings.NewReader("<html><body><a href=\"viewforum.php?f=1\">no magnet</a></body></html>"))
	require.NoError(t, err)
	require.False(t, ok)
}

// Each topic's page is read once; a page without a magnet link is marked
// read too, one that fails is not.
func TestFetch(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.RequestURI())
		mu.Unlock()
		switch r.URL.RequestURI() {
		case "/forum/rss.php?f=218":
			w.Header().Set("Content-Type", "application/rss+xml;charset=windows-1251")
			_, _ = w.Write(cp1251(t, rss))
		case "/forum/viewtopic.php?t=1898322":
			w.Header().Set("Content-Type", "text/html; charset=windows-1251")
			_, _ = w.Write(cp1251(t, topicPage))
		case "/forum/viewtopic.php?t=1898300":
			_, _ = w.Write([]byte("<html><body>no magnet yet</body></html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	state := memState{}
	src := &Source{Config: Config{URL: srv.URL + "/forum/", Forums: []int{218}}}
	require.Equal(t, "nnmclub", src.Name())
	items, err := src.Fetch(context.Background(), state)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "56dee5b6b78025762ef3091308db64ce2c34bd66", hex.EncodeToString(items[0].Hash[:]))
	require.Equal(t, feed.Item{Hash: items[0].Hash, Title: "Вышка 2 / Fall 2: Deadpoint (2026) WEB-DLRip [H.264]", Category: "Зарубежные Новинки (SD, DVD)",
		URL: "https://nnmclub.to/forum/viewtopic.php?t=1898322", At: time.Date(2026, 11, 5, 18, 30, 55, 0, time.UTC)}, items[0])
	require.Equal(t, memState{"topic:1898322": true, "topic:1898300": true}, state)

	items, err = src.Fetch(context.Background(), state)
	require.NoError(t, err)
	require.Empty(t, items, "nothing new")
	mu.Lock()
	require.Equal(t, []string{"/forum/rss.php?f=218", "/forum/viewtopic.php?t=1898322", "/forum/viewtopic.php?t=1898300", "/forum/rss.php?f=218"}, paths)
	mu.Unlock()

	_, err = (&Source{Config: Config{URL: srv.URL + "/forum"}}).Fetch(context.Background(), memState{})
	require.ErrorContains(t, err, "/forum/rss.php: 404", "no forums reads the RSS of every forum")
}
