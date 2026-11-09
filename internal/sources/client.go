// Package sources polls Torznab sources for their latest listings: each
// listing is recorded in the store, and the infohash of one whose torrent
// the store lacks is queued to be fetched under store.Torznab. A Client asks
// one source, a Recorder keeps what it lists, and a Poller asks it on a
// schedule.
package sources

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"sarak.sh/mikluko/libarr/telemetry"
	"sarak.sh/mikluko/libarr/torznab"

	"sarak.sh/mikluko/sniffarr/internal/store"
)

// Modes of a Source.
const (
	// Passive is a source asked for its latest listings only.
	Passive = "passive"
	// Active is a source that may also be searched.
	Active = "active"
)

// Source is one Torznab source as configured.
type Source struct {
	// Name keys the source's listings in the store, its journal entries and
	// its metrics: "spidarr".
	Name string
	// URL is the source's Torznab endpoint: http://spidarr.spidarr:8080/api.
	URL string
	// APIKey is sent as the apikey parameter; "" sends none.
	APIKey string
	// TokenFile is the path of a bearer token sent in the Authorization
	// header, read again for each request so a rotated token is taken up;
	// "" sends none.
	TokenFile string
	// Mode is Passive or Active.
	Mode string
}

// MaxBody is how much of an answer a Client reads.
const MaxBody = 8 << 20

// attrIMDb is the torznab:attr of an IMDb ID without its tt, as Torznab
// specifies it; libarr's torznab names only imdbid, which may have it.
const attrIMDb = "imdb"

// errNoFeed is an answer that is neither a Torznab feed nor a Newznab error.
var errNoFeed = errors.New("not a Torznab feed")

// Client asks one Source over HTTP. It is safe for concurrent use.
type Client struct {
	Source Source
	// HTTP sends the requests, each traced as a client span;
	// http.DefaultClient when nil.
	HTTP *http.Client

	once   sync.Once
	traced *http.Client
}

// Latest returns the items of a t=search without q, skipping offset of them
// and returning at most limit. A Newznab error answer is returned as a
// *torznab.Error.
func (c *Client) Latest(ctx context.Context, offset, limit int) ([]torznab.Item, error) {
	u, err := url.Parse(c.Source.URL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("t", "search")
	q.Set("offset", strconv.Itoa(offset))
	q.Set("limit", strconv.Itoa(limit))
	if c.Source.APIKey != "" {
		q.Set("apikey", c.Source.APIKey)
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sniffarr")
	if c.Source.TokenFile != "" {
		token, err := os.ReadFile(c.Source.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("token: %w", err)
		}
		if t := strings.TrimSpace(string(token)); t != "" {
			req.Header.Set("Authorization", "Bearer "+t)
		}
	}
	res, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("%s", res.Status)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, MaxBody))
	if err != nil {
		return nil, err
	}
	return decode(body)
}

func (c *Client) client() *http.Client {
	c.once.Do(func() { c.traced = telemetry.HTTPClient(c.HTTP) })
	return c.traced
}

// decode reads a Torznab feed's items, or a Newznab error as a
// *torznab.Error, by the answer's root element.
func decode(body []byte) ([]torznab.Item, error) {
	d := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errNoFeed, err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "rss":
			var feed torznab.RSS
			if err := d.DecodeElement(&feed, &start); err != nil {
				return nil, err
			}
			return feed.Channel.Items, nil
		case "error":
			var e torznab.Error
			if err := d.DecodeElement(&e, &start); err != nil {
				return nil, err
			}
			return nil, &e
		}
		return nil, fmt.Errorf("%w: <%s>", errNoFeed, start.Name.Local)
	}
}

// isRefusal reports whether err is a source's Newznab error answer.
func isRefusal(err error) bool {
	var e *torznab.Error
	return errors.As(err, &e)
}

// Listing is the item as the source's listing in the store, and false for
// an item without a BitTorrent v1 infohash: in its infohash attr, else in
// the magnet link of its magneturl attr, GUID, link or enclosure.
func Listing(source string, it torznab.Item) (store.Listing, bool) {
	h, ok := itemHash(it)
	if !ok {
		return store.Listing{}, false
	}
	l := store.Listing{Source: source, Hash: h, Title: it.Title, Comments: it.Comments, Categories: it.AllCategories(), Size: it.Size,
		PublishedAt: it.PubDate.UTC()}
	if l.Size == 0 {
		if v, ok := it.Attr(torznab.AttrSize); ok {
			l.Size, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	if v, ok := it.Attr(attrIMDb); ok {
		l.IMDb = imdb(v)
	}
	if v, ok := it.Attr(torznab.AttrIMDbID); ok && l.IMDb == "" {
		l.IMDb = imdb(v)
	}
	l.TMDBID = number(it, torznab.AttrTMDbID)
	l.TVDBID = number(it, torznab.AttrTVDbID)
	return l, true
}

func itemHash(it torznab.Item) (store.Hash, bool) {
	if v, ok := it.Attr(torznab.AttrInfohash); ok {
		if h, ok := hexHash(v); ok {
			return h, true
		}
	}
	magnet, _ := it.Attr(torznab.AttrMagnetURL)
	for _, s := range []string{magnet, it.GUID.Value, it.Link, it.Enclosure.URL} {
		if h, ok := magnetHash(s); ok {
			return h, true
		}
	}
	return store.Hash{}, false
}

// magnetHash reads the v1 infohash of a magnet link, in either case.
func magnetHash(magnet string) (store.Hash, bool) {
	u, err := url.Parse(magnet)
	if err != nil || u.Scheme != "magnet" {
		return store.Hash{}, false
	}
	for _, xt := range u.Query()["xt"] {
		if hexed, ok := strings.CutPrefix(xt, "urn:btih:"); ok {
			if h, ok := hexHash(hexed); ok {
				return h, true
			}
		}
	}
	return store.Hash{}, false
}

// hexHash reads an infohash of 40 hex digits, in either case.
func hexHash(s string) (store.Hash, bool) {
	var h store.Hash
	if len(s) != 2*len(h) {
		return h, false
	}
	_, err := hex.Decode(h[:], []byte(s))
	return h, err == nil
}

// imdb is an IMDb ID with its tt, "" for one that is not a positive number
// with or without it.
func imdb(s string) string {
	digits := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "tt")
	if n, err := strconv.Atoi(digits); err != nil || n <= 0 {
		return ""
	}
	return "tt" + digits
}

// number is the item's attr name as a positive number, 0 for none.
func number(it torznab.Item, name string) int {
	v, _ := it.Attr(name)
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
