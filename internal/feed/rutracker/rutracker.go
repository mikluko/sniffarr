// Package rutracker reads rutracker's Atom feeds, which list a forum's
// latest releases, each with a magnet link, to anyone.
package rutracker

import (
	"context"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mikluko/sniffarr/internal/feed"
	"github.com/mikluko/sniffarr/internal/store"
	"github.com/mikluko/sniffarr/internal/telemetry"
)

// Name is the source's name.
const Name = "rutracker"

// DefaultURL serves the feeds; a forum's is atom/f/<forum>.atom under it,
// and forum 0 is the whole tracker.
const DefaultURL = "https://feed.rutracker.cc"

// Config is which feeds a Source reads. Zero fields take the default named
// in their doc.
type Config struct {
	// URL is where the feeds are served; DefaultURL.
	URL string
	// Forums are the forums read, 0 the whole tracker; []int{0}.
	Forums []int
}

// Source reads the feeds of Config.Forums, and once per Source the feed of
// every other forum those name in their categories: a forum's feed holds
// about a month, the whole tracker's a few hours, so the first Fetch of a
// process fills a store from each forum the tracker is active in.
type Source struct {
	Config
	// HTTP sends the requests, each traced as a client span;
	// http.DefaultClient. It is read on the first request.
	HTTP *http.Client
	// Log discards everything when nil.
	Log *slog.Logger

	once   sync.Once
	traced *http.Client
	mu     sync.Mutex
	read   map[int]bool // forums read once for their categories
}

// pause is the wait between the feeds of forums read for their categories.
const pause = time.Second

// Name returns Name.
func (s *Source) Name() string { return Name }

// Fetch reads every forum's feed and returns their releases, a release
// listed by several forums once. An entry without a magnet link, or with
// one naming no v1 infohash, is left out. A forum of a category that is
// not among Forums is read too, the first time it is named; its feed
// failing is logged and not retried.
func (s *Source) Fetch(ctx context.Context) ([]feed.Item, error) {
	forums := s.Forums
	if len(forums) == 0 {
		forums = []int{0}
	}
	var items []feed.Item
	seen := map[store.Hash]bool{}
	add := func(got []feed.Item) {
		for _, it := range got {
			if !seen[it.Hash] {
				seen[it.Hash] = true
				items = append(items, it)
			}
		}
	}
	for _, f := range forums {
		got, err := s.fetch(ctx, f)
		if err != nil {
			return nil, fmt.Errorf("forum %d: %w", f, err)
		}
		add(got)
	}
	for _, f := range s.unread(forums, items) {
		if err := sleep(ctx, pause); err != nil {
			return nil, err
		}
		got, err := s.fetch(ctx, f)
		if err != nil {
			s.log().WarnContext(ctx, "read forum feed", "forum", f, "err", err)
			continue
		}
		add(got)
	}
	return items, nil
}

// unread returns the forums the items' categories name that are neither
// among forums nor read before, marking them read.
func (s *Source) unread(forums []int, items []feed.Item) []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.read == nil {
		s.read = map[int]bool{}
	}
	for _, f := range forums {
		s.read[f] = true
	}
	var out []int
	for _, it := range items {
		f, ok := forum(it.Category)
		if ok && !s.read[f] {
			s.read[f] = true
			out = append(out, f)
		}
	}
	return out
}

// forum reads the forum number of a category term, "f-1457".
func forum(category string) (int, bool) {
	num, ok := strings.CutPrefix(category, "f-")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(num)
	return n, err == nil && n > 0
}

func (s *Source) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.DiscardHandler)
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Source) fetch(ctx context.Context, forum int) ([]feed.Item, error) {
	base := s.URL
	if base == "" {
		base = DefaultURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(base, "/")+"/atom/f/"+strconv.Itoa(forum)+".atom", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sniffarr")
	res, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", req.URL, res.Status)
	}
	return Parse(io.LimitReader(res.Body, 8<<20))
}

func (s *Source) client() *http.Client {
	s.once.Do(func() {
		c := s.HTTP
		if c == nil {
			c = http.DefaultClient
		}
		s.traced = telemetry.HTTPClient(c)
	})
	return s.traced
}

// atomFeed is what of the feed Parse reads.
type atomFeed struct {
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	Title    string     `xml:"title"`
	Updated  string     `xml:"updated"`
	Links    []atomLink `xml:"link"`
	Category struct {
		Term string `xml:"term,attr"`
	} `xml:"category"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
	Type string `xml:"type,attr"`
}

// Parse reads an Atom feed as rutracker serves it: an entry's title, its
// page as the link with no rel, its infohash from the magnet link in
// rel="enclosure", its category term and its updated time. The size in the
// title's last brackets, "[10.58 GB]", is read when it is there.
func Parse(r io.Reader) ([]feed.Item, error) {
	var f atomFeed
	if err := xml.NewDecoder(r).Decode(&f); err != nil {
		return nil, fmt.Errorf("atom: %w", err)
	}
	var items []feed.Item
	for _, e := range f.Entries {
		it := feed.Item{Title: strings.TrimSpace(e.Title), Category: e.Category.Term, Size: size(e.Title)}
		var ok bool
		for _, l := range e.Links {
			switch l.Rel {
			case "":
				it.URL = l.Href
			case "enclosure":
				it.Hash, ok = infohash(l.Href)
			}
		}
		if !ok {
			continue
		}
		if t, err := time.Parse(time.RFC3339, e.Updated); err == nil {
			it.At = t.UTC()
		}
		items = append(items, it)
	}
	return items, nil
}

// infohash reads the v1 infohash of a magnet link.
func infohash(magnet string) (store.Hash, bool) {
	var h store.Hash
	u, err := url.Parse(magnet)
	if err != nil || u.Scheme != "magnet" {
		return h, false
	}
	for _, xt := range u.Query()["xt"] {
		hexed, ok := strings.CutPrefix(xt, "urn:btih:")
		if !ok || len(hexed) != 2*len(h) {
			continue
		}
		if _, err := hex.Decode(h[:], []byte(hexed)); err == nil {
			return h, true
		}
	}
	return h, false
}

// size reads the size in the title's last brackets, "[10.58 GB]", in bytes;
// 0 when there is none.
func size(title string) int64 {
	open := strings.LastIndex(title, "[")
	if open < 0 || !strings.HasSuffix(title, "]") {
		return 0
	}
	num, unit, ok := strings.Cut(strings.TrimSpace(title[open+1:len(title)-1]), " ")
	if !ok {
		return 0
	}
	n, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	switch strings.ToUpper(unit) {
	case "B":
		return int64(n)
	case "KB":
		return int64(n * (1 << 10))
	case "MB":
		return int64(n * (1 << 20))
	case "GB":
		return int64(n * (1 << 30))
	case "TB":
		return int64(n * (1 << 40))
	}
	return 0
}
