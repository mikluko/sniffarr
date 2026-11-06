// Package rutor reads rutor's listing pages, which carry a magnet link per
// release to anyone; its RSS carries none.
package rutor

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	"github.com/mikluko/sniffarr/internal/feed"
	"github.com/mikluko/sniffarr/internal/store"
)

// Name is the source's name.
const Name = "rutor"

// DefaultURL is the site; a listing page is browse/<page>/<category>/0/0
// under it, 100 releases a page, newest first, category 0 every one.
const DefaultURL = "https://rutor.info"

// DefaultDepth is how many pages the first pass reads.
const DefaultDepth = 10

// Config is which pages a Source reads. Zero fields take the default named
// in their doc.
type Config struct {
	// URL is the site; DefaultURL.
	URL string
	// Categories are the categories read, 0 every one; []int{0}.
	Categories []int
	// Depth is how many pages of each category the first pass reads, a
	// pass marked in the feed.State; later ones read the first page;
	// DefaultDepth.
	Depth int
}

// Source reads the listing pages of Config.Categories.
type Source struct {
	Config
	// HTTP sends the requests, each traced as a client span;
	// http.DefaultClient. It is read on the first request.
	HTTP *http.Client
	// Log discards everything when nil.
	Log *slog.Logger

	once   sync.Once
	traced *http.Client
}

// pause is the wait between pages.
const pause = time.Second

// deepKey marks the first pass done.
const deepKey = "deep"

// Name returns Name.
func (s *Source) Name() string { return Name }

// Fetch reads the first page of every category, or Depth pages each on the
// first pass, and returns their releases, one listed under several
// categories once.
func (s *Source) Fetch(ctx context.Context, state feed.State) ([]feed.Item, error) {
	categories := s.Categories
	if len(categories) == 0 {
		categories = []int{0}
	}
	deep, err := state.Done(ctx, deepKey)
	if err != nil {
		return nil, err
	}
	pages := 1
	if !deep {
		pages = s.depth()
	}
	var items []feed.Item
	seen := map[store.Hash]bool{}
	for i, c := range categories {
		got, err := s.category(ctx, c, pages, i > 0)
		if err != nil {
			return nil, err
		}
		for _, it := range got {
			if !seen[it.Hash] {
				seen[it.Hash] = true
				items = append(items, it)
			}
		}
	}
	if !deep {
		if err := state.Mark(ctx, deepKey); err != nil {
			return nil, err
		}
	}
	return items, nil
}

// category reads up to pages pages of the category, a pause apart, and
// before the first too when waited; an empty page ends it.
func (s *Source) category(ctx context.Context, category, pages int, waited bool) ([]feed.Item, error) {
	var items []feed.Item
	for p := range pages {
		if waited || p > 0 {
			if err := feed.Sleep(ctx, pause); err != nil {
				return nil, err
			}
		}
		got, err := s.page(ctx, category, p)
		if err != nil {
			return nil, fmt.Errorf("category %d page %d: %w", category, p, err)
		}
		if len(got) == 0 {
			break
		}
		items = append(items, got...)
	}
	return items, nil
}

func (s *Source) depth() int {
	if s.Depth > 0 {
		return s.Depth
	}
	return DefaultDepth
}

func (s *Source) base() string {
	if s.URL == "" {
		return DefaultURL
	}
	return strings.TrimSuffix(s.URL, "/")
}

func (s *Source) page(ctx context.Context, category, page int) ([]feed.Item, error) {
	u := fmt.Sprintf("%s/browse/%d/%d/0/0", s.base(), page, category)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
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
		return nil, fmt.Errorf("%s: %s", u, res.Status)
	}
	items, err := Parse(io.LimitReader(res.Body, 8<<20), s.base())
	if err != nil {
		return nil, err
	}
	if category != 0 {
		for i := range items {
			items[i].Category = strconv.Itoa(category)
		}
	}
	return items, nil
}

func (s *Source) client() *http.Client {
	s.once.Do(func() { s.traced = feed.Client(s.HTTP) })
	return s.traced
}

// Parse reads a listing page: each table row with a magnet link is a
// release, its title and page the link to /torrent/<id>/..., resolved
// against base, its date the row's first cell, "06 Ноя 26", and its size
// the cell like "4.92 GB". A row without a magnet link naming a v1
// infohash is left out.
func Parse(r io.Reader, base string) ([]feed.Item, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("html: %w", err)
	}
	var items []feed.Item
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "tr" {
			if it, ok := row(n, base); ok {
				items = append(items, it)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return items, nil
}

// row reads one release from a table row.
func row(tr *html.Node, base string) (feed.Item, bool) {
	var it feed.Item
	var ok bool
	var cells []string
	for td := tr.FirstChild; td != nil; td = td.NextSibling {
		if td.Type != html.ElementNode || td.Data != "td" {
			continue
		}
		cells = append(cells, strings.TrimSpace(text(td)))
		for _, a := range anchors(td) {
			href := attr(a, "href")
			switch {
			case strings.HasPrefix(href, "magnet:"):
				it.Hash, ok = feed.Infohash(href)
			case strings.HasPrefix(href, "/torrent/"):
				it.Title = strings.TrimSpace(text(a))
				it.URL = base + href
			}
		}
	}
	if !ok {
		return it, false
	}
	if len(cells) > 0 {
		it.At = date(cells[0])
		it.Size = size(cells[1:])
	}
	return it, true
}

// size is the bytes of the first cell like "4.92 GB", 0 for none.
func size(cells []string) int64 {
	for _, c := range cells {
		n, unit, found := strings.Cut(c, " ")
		if !found {
			continue
		}
		if f, err := strconv.ParseFloat(n, 64); err == nil {
			if b := feed.Bytes(f, unit); b > 0 {
				return b
			}
		}
	}
	return 0
}

// months are the listing's month names.
var months = map[string]time.Month{
	"Янв": time.January, "Фев": time.February, "Мар": time.March, "Апр": time.April,
	"Май": time.May, "Июн": time.June, "Июл": time.July, "Авг": time.August,
	"Сен": time.September, "Окт": time.October, "Ноя": time.November, "Дек": time.December,
}

// date reads "06 Ноя 26"; zero when it cannot.
func date(s string) time.Time {
	parts := strings.Fields(strings.ReplaceAll(s, " ", " "))
	if len(parts) != 3 {
		return time.Time{}
	}
	day, err := strconv.Atoi(parts[0])
	month, ok := months[parts[1]]
	year, err2 := strconv.Atoi(parts[2])
	if err != nil || !ok || err2 != nil {
		return time.Time{}
	}
	return time.Date(2000+year, month, day, 0, 0, 0, 0, time.UTC)
}

func anchors(n *html.Node) []*html.Node {
	var out []*html.Node
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			out = append(out, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func text(n *html.Node) string {
	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}
