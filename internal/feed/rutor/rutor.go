// Package rutor reads rutor's listing pages, which carry a magnet link per
// release to anyone; its RSS carries none.
package rutor

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
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
	// Depth is how many pages of each category are read once, the first
	// page every round; DefaultDepth.
	Depth int
}

// Source reads the listing pages of Config.Categories.
type Source struct {
	Config
}

// Name returns Name.
func (s *Source) Name() string { return Name }

// Fetch reads the first page of every category, and the next Depth-1 pages
// once, and returns their releases, one listed under several categories
// once. The pages read once stop at the first read before, so a category
// read before costs one page a round.
func (s *Source) Fetch(ctx context.Context, c feed.Crawler) ([]feed.Item, error) {
	categories := s.Categories
	if len(categories) == 0 {
		categories = []int{0}
	}
	var items []feed.Item
	seen := map[store.Hash]bool{}
	for _, cat := range categories {
		got, err := s.category(ctx, c, cat)
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
	return items, nil
}

// category reads the category's first page, and the pages after it that
// were not read before, up to Depth; an empty page ends it.
func (s *Source) category(ctx context.Context, c feed.Crawler, category int) ([]feed.Item, error) {
	var items []feed.Item
	for p := range s.depth() {
		get := c.Once
		if p == 0 {
			get = c.Fetch
		}
		body, ok, err := get(ctx, fmt.Sprintf("%s/browse/%d/%d/0/0", s.base(), p, category))
		if err != nil {
			return nil, fmt.Errorf("category %d page %d: %w", category, p, err)
		}
		if !ok {
			if p == 0 {
				continue
			}
			break
		}
		got, err := Parse(body, s.base())
		_ = body.Close()
		if err != nil {
			return nil, fmt.Errorf("category %d page %d: %w", category, p, err)
		}
		if len(got) == 0 {
			break
		}
		if category != 0 {
			for i := range got {
				got[i].Category = strconv.Itoa(category)
			}
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
