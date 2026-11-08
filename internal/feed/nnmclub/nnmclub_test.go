package nnmclub

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/text/encoding/charmap"

	"sarak.sh/mikluko/sniffarr/internal/feed"
	"sarak.sh/mikluko/sniffarr/internal/feed/feedtest"
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

// topicPage is the part of a topic page that holds the magnet link.
const topicPage = `<html><head><meta http-equiv="Content-Type" content="text/html; charset=windows-1251"><title>Вышка 2 :: NNM-Club</title></head>
<body><table><tr><td class="gensmall"><a href="profile.php?mode=register">Зарегистрируйтесь</a><br />16.5&nbsp;KB<br />
<!--noindex--><a rel="nofollow" href="magnet:?xt=urn:btih:56DEE5B6B78025762EF3091308DB64CE2C34BD66" title="Примагнититься"><img src="magnet48.png" /></a><!--/noindex--></td></tr></table></body></html>`

// cp1251 encodes s as the forum does.
func cp1251(t *testing.T, s string) string {
	t.Helper()
	b, err := charmap.Windows1251.NewEncoder().Bytes([]byte(s))
	require.NoError(t, err)
	return string(b)
}

func TestParseRSS(t *testing.T) {
	topics, err := ParseRSS(strings.NewReader(cp1251(t, rss)))
	require.NoError(t, err)
	require.Equal(t, []topic{
		{ID: 1898322, Title: "Вышка 2 / Fall 2: Deadpoint (2026) WEB-DLRip [H.264]", Category: "Зарубежные Новинки (SD, DVD)",
			URL: "https://nnmclub.to/forum/viewtopic.php?t=1898322", At: time.Date(2026, 11, 5, 18, 30, 55, 0, time.UTC)},
		{ID: 1898300, Title: "Горькое Рождество / Amarga Navidad (2026) BDRip [H.264]", Category: "Зарубежные Новинки (SD, DVD)",
			URL: "https://nnmclub.to/forum/viewtopic.php?t=1898300", At: time.Date(2026, 11, 5, 17, 0, 0, 0, time.UTC)},
	}, topics, "the charset decoded, the forum split off the title, the item without a topic left out")
}

func TestParseTopic(t *testing.T) {
	h, ok, err := ParseTopic(strings.NewReader(cp1251(t, topicPage)))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "56dee5b6b78025762ef3091308db64ce2c34bd66", hex.EncodeToString(h[:]))
	_, ok, err = ParseTopic(strings.NewReader("<html><body><a href=\"viewforum.php?f=1\">no magnet</a></body></html>"))
	require.NoError(t, err)
	require.False(t, ok)
}

// Each topic's page is read once; a page without a magnet link counts as
// read too, one that fails is tried again.
func TestFetch(t *testing.T) {
	c := &feedtest.Crawler{Pages: map[string]string{
		"https://club.example/forum/rss.php?f=218":           cp1251(t, rss),
		"https://club.example/forum/viewtopic.php?t=1898322": cp1251(t, topicPage),
		"https://club.example/forum/viewtopic.php?t=1898300": "<html><body>no magnet yet</body></html>",
	}}
	src := &Source{Config{URL: "https://club.example/forum/", Forums: []int{218}}}
	require.Equal(t, "nnmclub", src.Name())
	items, err := src.Fetch(context.Background(), c)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "56dee5b6b78025762ef3091308db64ce2c34bd66", hex.EncodeToString(items[0].Hash[:]))
	require.Equal(t, feed.Item{Hash: items[0].Hash, Title: "Вышка 2 / Fall 2: Deadpoint (2026) WEB-DLRip [H.264]", Category: "Зарубежные Новинки (SD, DVD)",
		URL: "https://nnmclub.to/forum/viewtopic.php?t=1898322", At: time.Date(2026, 11, 5, 18, 30, 55, 0, time.UTC)}, items[0])

	items, err = src.Fetch(context.Background(), c)
	require.NoError(t, err)
	require.Empty(t, items, "nothing new")
	require.Equal(t, []string{"https://club.example/forum/rss.php?f=218", "https://club.example/forum/viewtopic.php?t=1898322", "https://club.example/forum/viewtopic.php?t=1898300",
		"https://club.example/forum/rss.php?f=218"}, c.Calls)

	c = &feedtest.Crawler{Pages: map[string]string{"https://club.example/forum/rss.php": cp1251(t, rss)}}
	items, err = (&Source{Config{URL: "https://club.example/forum"}}).Fetch(context.Background(), c)
	require.NoError(t, err)
	require.Empty(t, items, "the topic pages fail, so nothing yet")
	require.Equal(t, "https://club.example/forum/rss.php", c.Calls[0], "no forums reads the RSS of every forum")
	require.Empty(t, c.Done(), "failed pages are not marked")
}
