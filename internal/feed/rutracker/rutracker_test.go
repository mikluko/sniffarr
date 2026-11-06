package rutracker

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/feed"
	"github.com/mikluko/sniffarr/internal/store"
)

// atom is two entries as feed.rutracker.cc serves them on 2026-11-04, one
// without a magnet link, and one with a malformed one.
const atom = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
	<title>Зарубежное кино (UHD Video)</title>
	<link href="https://rutracker.org/forum/viewforum.php?f=1457"/>
	<id>tag:rto.feed,2000:/f/1457</id>
	<updated>2026-11-06T15:39:01+00:00</updated>
	<entry>
		<title>Вышка 2 / Fall 2: Deadpoint (Майкл Спириг / Michael Spierig) [2026, США, триллер, WEB-DL 2160p, Dolby Vision TV] Dub (CPI Films) + Original Eng + Sub (Rus, Eng) [10.58 GB]</title>
		<author><name>HANSMER</name></author>
		<link href="https://rutracker.org/forum/viewtopic.php?t=6933014"/>
		<link rel="enclosure" href="magnet:?xt=urn:btih:5524C43F5A956A7EB5C3D6F9B0874F9143EDA6BF&amp;tr=http%3A%2F%2Fbt.t-ru.org%2Fann%3Fmagnet" type="application/x-bittorrent"/>
		<id>tag:rto.feed,2026-11-04:/t/6933014</id>
		<updated>2026-11-04T21:50:09+00:00</updated>
		<category term="f-1457" label="Зарубежное кино (UHD Video)"/>
	</entry>
	<entry>
		<title>A Dog&#039;s Perfect Christmas (Cathryn Michon) [2026, Канада, драма, WEB-DL 2160p] [9.45 GB]</title>
		<link href="https://rutracker.org/forum/viewtopic.php?t=6932892"/>
		<link rel="enclosure" href="magnet:?xt=urn:btih:925E3FCDD454CC67A734826D17C3DAED5E22FE3C&amp;tr=http%3A%2F%2Fbt4.t-ru.org%2Fann%3Fmagnet" type="application/x-bittorrent"/>
		<updated>2026-11-04T21:45:07+00:00</updated>
		<category term="f-1457" label="Зарубежное кино (UHD Video)"/>
	</entry>
	<entry>
		<title>No magnet [1 GB]</title>
		<link href="https://rutracker.org/forum/viewtopic.php?t=1"/>
		<updated>2026-11-04T21:00:00+00:00</updated>
	</entry>
	<entry>
		<title>Bad magnet</title>
		<link href="https://rutracker.org/forum/viewtopic.php?t=2"/>
		<link rel="enclosure" href="magnet:?xt=urn:btih:ZZZZ" type="application/x-bittorrent"/>
		<updated>2026-11-04T20:00:00+00:00</updated>
	</entry>
</feed>
`

func hashOf(t *testing.T, s string) store.Hash {
	t.Helper()
	var h store.Hash
	_, err := hex.Decode(h[:], []byte(s))
	require.NoError(t, err)
	return h
}

func TestParse(t *testing.T) {
	items, err := Parse(strings.NewReader(atom))
	require.NoError(t, err)
	require.Equal(t, []feed.Item{
		{Hash: hashOf(t, "5524C43F5A956A7EB5C3D6F9B0874F9143EDA6BF"),
			Title:    "Вышка 2 / Fall 2: Deadpoint (Майкл Спириг / Michael Spierig) [2026, США, триллер, WEB-DL 2160p, Dolby Vision TV] Dub (CPI Films) + Original Eng + Sub (Rus, Eng) [10.58 GB]",
			URL:      "https://rutracker.org/forum/viewtopic.php?t=6933014",
			Category: "f-1457", Size: 11360188497, At: time.Date(2026, 11, 4, 21, 50, 9, 0, time.UTC)},
		{Hash: hashOf(t, "925E3FCDD454CC67A734826D17C3DAED5E22FE3C"),
			Title:    "A Dog's Perfect Christmas (Cathryn Michon) [2026, Канада, драма, WEB-DL 2160p] [9.45 GB]",
			URL:      "https://rutracker.org/forum/viewtopic.php?t=6932892",
			Category: "f-1457", Size: 10146860236, At: time.Date(2026, 11, 4, 21, 45, 7, 0, time.UTC)},
	}, items, "entries without a usable magnet link are left out")
	_, err = Parse(strings.NewReader("<feed>"))
	require.ErrorContains(t, err, "atom:")
}

func TestSize(t *testing.T) {
	for _, x := range []struct {
		title string
		want  int64
	}{
		{"Alien [1979, horror] [8.5 GB]", 9126805504},
		{"Alien [700 MB]", 734003200},
		{"Alien [1 TB]", 1 << 40},
		{"Alien [1979, horror]", 0},
		{"Alien", 0},
		{"Alien [x GB]", 0},
	} {
		require.Equal(t, x.want, size(x.title), x.title)
	}
}

func TestFetch(t *testing.T) {
	var forums atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forums.Add(1)
		switch r.URL.Path {
		case "/atom/f/0.atom", "/atom/f/1457.atom":
			w.Header().Set("Content-Type", "application/atom+xml")
			_, _ = w.Write([]byte(atom))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	src := &Source{Config: Config{URL: srv.URL + "/", Forums: []int{0, 1457}}}
	require.Equal(t, "rutracker", src.Name())
	items, err := src.Fetch(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 2, "a release listed by both forums once")
	require.Equal(t, int32(2), forums.Load())

	_, err = (&Source{Config: Config{URL: srv.URL, Forums: []int{9}}}).Fetch(context.Background())
	require.ErrorContains(t, err, "forum 9: ")
	require.ErrorContains(t, err, "404")

	items, err = (&Source{Config: Config{URL: srv.URL}}).Fetch(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 2, "no forums reads forum 0")
}

// A forum the whole-tracker feed names in a category is read once per
// Source for its month of releases; one whose feed fails is logged and not
// tried again.
func TestFetchDiscovers(t *testing.T) {
	deep := strings.Replace(atom, "t=6932892", "t=1", 1)
	deep = strings.Replace(deep, "925E3FCDD454CC67A734826D17C3DAED5E22FE3C", "0000000000000000000000000000000000000001", 1)
	var paths []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/atom/f/0.atom":
			_, _ = w.Write([]byte(strings.Replace(atom, `term="f-1457"`, `term="f-77"`, 1)))
		case "/atom/f/1457.atom":
			_, _ = w.Write([]byte(deep))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	src := &Source{Config: Config{URL: srv.URL}}
	items, err := src.Fetch(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 3, "forum 0's two and the one more forum 1457 holds; forum 77's 404 is skipped")
	items, err = src.Fetch(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 2, "the second round reads forum 0 alone")
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"/atom/f/0.atom", "/atom/f/77.atom", "/atom/f/1457.atom", "/atom/f/0.atom"}, paths)
}
