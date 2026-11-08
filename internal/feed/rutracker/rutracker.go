// Package rutracker reads rutracker's Atom feeds, which list a forum's
// latest releases, each with a magnet link, to anyone.
package rutracker

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"sarak.sh/mikluko/sniffarr/internal/feed"
	"sarak.sh/mikluko/sniffarr/internal/store"
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

// Source reads the feeds of Config.Forums every round, and once the feed
// of every other forum those name in their categories: a forum's feed
// holds about a month, the whole tracker's a few hours, so the first round
// fills a store from each forum the tracker is active in.
type Source struct {
	Config
}

// Name returns Name.
func (s *Source) Name() string { return Name }

// Fetch reads every forum's feed and returns their releases, a release
// listed by several forums once. An entry without a magnet link, or with
// one naming no v1 infohash, is left out. A forum of a category that is
// not among Forums is read too, once; its feed failing is left to the
// crawler's log.
func (s *Source) Fetch(ctx context.Context, c feed.Crawler) ([]feed.Item, error) {
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
		got, err := s.fetch(ctx, c.Fetch, f)
		if err != nil {
			return nil, fmt.Errorf("forum %d: %w", f, err)
		}
		add(got)
	}
	for _, f := range others(forums, items) {
		got, err := s.fetch(ctx, c.Once, f)
		if err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			continue
		}
		add(got)
	}
	return items, nil
}

// others returns the forums the items' categories name that are not among
// forums, each once.
func others(forums []int, items []feed.Item) []int {
	skip := map[int]bool{}
	for _, f := range forums {
		skip[f] = true
	}
	var out []int
	for _, it := range items {
		if f, ok := forum(it.Category); ok && !skip[f] {
			skip[f] = true
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

// fetch reads a forum's feed through get, nothing when get has nothing new.
func (s *Source) fetch(ctx context.Context, get func(context.Context, string) (io.ReadCloser, bool, error), forum int) ([]feed.Item, error) {
	base := s.URL
	if base == "" {
		base = DefaultURL
	}
	body, ok, err := get(ctx, strings.TrimSuffix(base, "/")+"/atom/f/"+strconv.Itoa(forum)+".atom")
	if err != nil || !ok {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	return Parse(body)
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
				it.Hash, ok = feed.Infohash(l.Href)
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
	return feed.Bytes(n, unit)
}
