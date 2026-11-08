// Package nnmclub reads NNM-Club's RSS, which lists the latest topics of
// its forums to anyone, and each new topic's page for the magnet link the
// RSS leaves out.
package nnmclub

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"

	"sarak.sh/mikluko/sniffarr/internal/feed"
)

// Name is the source's name.
const Name = "nnmclub"

// DefaultURL is the forum; the RSS is rss.php under it, a topic
// viewtopic.php?t=<id>.
const DefaultURL = "https://nnmclub.to/forum"

// Config is which feeds a Source reads. Zero fields take the default named
// in their doc.
type Config struct {
	// URL is the forum; DefaultURL.
	URL string
	// Forums are the forums whose RSS is read; none reads the RSS of every
	// public forum.
	Forums []int
}

// Source reads the RSS of Config.Forums every round and the page of each
// topic it lists once, whether the page has a magnet link or not.
type Source struct {
	Config
}

// Name returns Name.
func (s *Source) Name() string { return Name }

// Fetch reads every forum's RSS, then the page of each listed topic not
// read before, and returns the releases whose page has a magnet link. A
// topic's page failing is left to the crawler's log, and the topic to the
// next round.
func (s *Source) Fetch(ctx context.Context, c feed.Crawler) ([]feed.Item, error) {
	topics, err := s.topics(ctx, c)
	if err != nil {
		return nil, err
	}
	var items []feed.Item
	seen := map[int]bool{}
	for _, t := range topics {
		if seen[t.ID] {
			continue
		}
		seen[t.ID] = true
		body, ok, err := c.Once(ctx, s.base()+"/viewtopic.php?t="+strconv.Itoa(t.ID))
		if err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			continue
		}
		if !ok {
			continue
		}
		hash, found, err := ParseTopic(body)
		_ = body.Close()
		if err != nil {
			return nil, fmt.Errorf("topic %d: %w", t.ID, err)
		}
		if found {
			items = append(items, feed.Item{Hash: hash, Title: t.Title, URL: t.URL, Category: t.Category, At: t.At})
		}
	}
	return items, nil
}

// topics lists the RSS of every forum, or of the whole forum for none;
// a feed with nothing new lists nothing.
func (s *Source) topics(ctx context.Context, c feed.Crawler) ([]topic, error) {
	if len(s.Forums) == 0 {
		return s.rss(ctx, c, 0)
	}
	var topics []topic
	for _, f := range s.Forums {
		got, err := s.rss(ctx, c, f)
		if err != nil {
			return nil, fmt.Errorf("forum %d: %w", f, err)
		}
		topics = append(topics, got...)
	}
	return topics, nil
}

// topic is one item of the RSS.
type topic struct {
	ID       int
	Title    string
	Category string
	URL      string
	At       time.Time
}

func (s *Source) base() string {
	if s.URL == "" {
		return DefaultURL
	}
	return strings.TrimSuffix(s.URL, "/")
}

func (s *Source) rss(ctx context.Context, c feed.Crawler, forum int) ([]topic, error) {
	u := s.base() + "/rss.php"
	if forum > 0 {
		u += "?f=" + strconv.Itoa(forum)
	}
	body, ok, err := c.Fetch(ctx, u)
	if err != nil || !ok {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	return ParseRSS(body)
}

type rssFeed struct {
	Items []rssItem `xml:"channel>item"`
}

type rssItem struct {
	Title   string `xml:"title"`
	Link    string `xml:"link"`
	PubDate string `xml:"pubDate"`
}

// ParseRSS reads the RSS as the forum serves it, in its own charset: an
// item's title, "<forum> :: <topic>", its link, viewtopic.php?t=<id>, and
// its date. An item without a topic id is left out.
func ParseRSS(r io.Reader) ([]topic, error) {
	d := xml.NewDecoder(r)
	d.CharsetReader = charset.NewReaderLabel
	var f rssFeed
	if err := d.Decode(&f); err != nil {
		return nil, fmt.Errorf("rss: %w", err)
	}
	var topics []topic
	for _, it := range f.Items {
		id, ok := topicID(it.Link)
		if !ok {
			continue
		}
		t := topic{ID: id, URL: it.Link, Title: strings.TrimSpace(it.Title)}
		if forum, title, ok := strings.Cut(t.Title, " :: "); ok {
			t.Category, t.Title = strings.TrimSpace(forum), strings.TrimSpace(title)
		}
		if at, err := time.Parse(time.RFC1123, it.PubDate); err == nil {
			t.At = at.UTC()
		}
		topics = append(topics, t)
	}
	return topics, nil
}

// topicID reads the t of a viewtopic.php link.
func topicID(link string) (int, bool) {
	u, err := url.Parse(link)
	if err != nil {
		return 0, false
	}
	id, err := strconv.Atoi(u.Query().Get("t"))
	return id, err == nil && id > 0
}

// ParseTopic reads a topic's page, in its own charset, for the first magnet
// link naming a v1 infohash; ok is false when there is none.
func ParseTopic(r io.Reader) (hash [20]byte, ok bool, err error) {
	utf8, err := charset.NewReader(r, "text/html")
	if err != nil {
		return hash, false, err
	}
	z := html.NewTokenizer(utf8)
	for {
		switch z.Next() {
		case html.ErrorToken:
			if z.Err() == io.EOF {
				return hash, false, nil
			}
			return hash, false, z.Err()
		case html.StartTagToken, html.SelfClosingTagToken:
			if h, ok := magnetOf(z); ok {
				return h, true, nil
			}
		}
	}
}

// magnetOf reads the infohash of the tag the tokenizer is on, when it is an
// anchor whose href is a magnet link naming one.
func magnetOf(z *html.Tokenizer) ([20]byte, bool) {
	name, more := z.TagName()
	if string(name) != "a" {
		return [20]byte{}, false
	}
	for more {
		var key, val []byte
		key, val, more = z.TagAttr()
		if string(key) == "href" {
			if h, ok := feed.Infohash(string(val)); ok {
				return h, true
			}
		}
	}
	return [20]byte{}, false
}
