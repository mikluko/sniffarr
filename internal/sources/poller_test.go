package sources

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"sarak.sh/mikluko/sniffarr/internal/store"
)

// listingServer serves total listings, newest first, the nth with the
// infohash of n, and an item without an infohash first; it records the
// offset of every request.
type listingServer struct {
	total int
	mu    sync.Mutex
	asked []int
}

func (s *listingServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	s.mu.Lock()
	s.asked = append(s.asked, offset)
	s.mu.Unlock()
	var b strings.Builder
	b.WriteString(`<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>`)
	if offset == 0 {
		b.WriteString(`<item><title>no hash</title><link>https://x/t.torrent</link></item>`)
	}
	for i := offset; i < min(offset+limit, s.total); i++ {
		n := s.total - 1 - i
		h := store.Hash{byte(n >> 8), byte(n)}
		fmt.Fprintf(&b, `<item><title>r%d</title><torznab:attr name="infohash" value="%s"/></item>`, n, hex.EncodeToString(h[:]))
	}
	b.WriteString(`</channel></rss>`)
	_, _ = w.Write([]byte(b.String()))
}

func (s *listingServer) offsets() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.asked...)
}

func poller(t *testing.T, f *fixture, srv *listingServer) *Poller {
	t.Helper()
	hs := httptest.NewServer(srv)
	t.Cleanup(hs.Close)
	return &Poller{Client: &Client{Source: Source{Name: "spidarr", URL: hs.URL, Mode: Passive}}, Recorder: f.rec, Pause: time.Millisecond,
		Log: slog.New(slog.DiscardHandler)}
}

func TestRoundPages(t *testing.T) {
	f := setup(t)
	srv := &listingServer{total: 250}
	p := poller(t, f, srv)
	require.NoError(t, p.Round(context.Background()))
	require.Equal(t, []int{0, 100, 200}, srv.offsets(), "pages until one comes back short")
	require.Equal(t, 250, len(f.queue.queued()))

	srv.total = 260
	srv.asked = nil
	require.NoError(t, p.Round(context.Background()))
	require.Equal(t, []int{0}, srv.offsets(), "a page with a listing recorded before ends the round")
	require.Equal(t, 260, len(f.queue.queued()), "though every listing of it is recorded")
	sums := f.sums(t)
	require.Equal(t, int64(4), sums["sniffarr.source.requests spidarr ok"])
	require.Equal(t, int64(260), sums["sniffarr.source.listings spidarr queued"])
}

func TestRoundMaxPages(t *testing.T) {
	f := setup(t)
	srv := &listingServer{total: 10 * Page}
	require.NoError(t, poller(t, f, srv).Round(context.Background()))
	require.Equal(t, []int{0, 100, 200, 300, 400}, srv.offsets())
	require.Equal(t, MaxPages*Page, len(f.queue.queued()))
}

func TestRoundFails(t *testing.T) {
	f := setup(t)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<error code="100" description="Incorrect user credentials"/>`))
	}))
	t.Cleanup(hs.Close)
	p := &Poller{Client: &Client{Source: Source{Name: "spidarr", URL: hs.URL}}, Recorder: f.rec, Log: slog.New(slog.DiscardHandler)}
	require.True(t, isRefusal(p.Round(context.Background())))
	require.Equal(t, int64(1), f.sums(t)["sniffarr.source.requests spidarr refused"])
}

func TestNext(t *testing.T) {
	p := &Poller{Client: &Client{Source: Source{Name: "spidarr"}}, Interval: time.Hour, Node: []byte{1, 2, 3}}
	off := Offset("spidarr", p.Node, time.Hour)
	require.Less(t, off, time.Hour)
	require.Equal(t, off.Truncate(time.Second), off, "whole seconds")
	hour := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	require.Equal(t, hour.Add(off), p.Next(hour.Add(off-time.Second)))
	require.Equal(t, hour.Add(time.Hour+off), p.Next(hour.Add(off)), "a round at its moment schedules the next interval's")
	require.NotEqual(t, off, Offset("jackett", p.Node, time.Hour), "another source, another moment")
	require.NotEqual(t, off, Offset("spidarr", []byte{9}, time.Hour), "another node, another moment")
}
