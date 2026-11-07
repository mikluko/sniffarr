// Package feed takes in the releases that tracker feeds list: each one's
// infohash queued to be fetched when the store lacks it, its title and page
// kept beside the torrent. A Source is one feed; a Poller reads it on a
// schedule and a Recorder keeps what it lists.
package feed

import (
	"context"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/mikluko/sniffarr/internal/store"
	"github.com/mikluko/sniffarr/internal/telemetry"
)

const scope = "github.com/mikluko/sniffarr/internal/feed"

// Item is one release a feed lists.
type Item struct {
	Hash store.Hash
	// Title is the release as the feed titles it.
	Title string
	// URL is the release's page at the source, "" for none.
	URL string
	// Category is the source's category of the release, "" for none.
	Category string
	// Size is the release's size in bytes, 0 unknown.
	Size int64
	// At is when the source listed the release.
	At time.Time
}

// Source is one feed. Implementations live in packages under this one and
// are named in the configuration by Name.
type Source interface {
	// Name keys the source's items in the store and its metrics:
	// "rutracker".
	Name() string
	// Fetch returns the releases the feed lists at the moment, reading the
	// feed's pages through the Crawler. A feed is a window, so Fetch
	// returns the same release again until it leaves it.
	Fetch(ctx context.Context, c Crawler) ([]Item, error)
}

// Crawler fetches a source's pages and keeps, in the store and across
// restarts, what each URL answered last: crawl_state. The body returned is
// the caller's to close.
type Crawler interface {
	// Fetch GETs the URL, asking only for what changed since the last
	// fetch; ok is false when nothing has.
	Fetch(ctx context.Context, url string) (body io.ReadCloser, ok bool, err error)
	// Once GETs the URL unless a fetch of it succeeded before; ok is false
	// when one did.
	Once(ctx context.Context, url string) (body io.ReadCloser, ok bool, err error)
}

// Pause is the least time a crawler leaves between two requests of a source.
const Pause = time.Second

// MaxBody is how much of a page a crawler reads.
const MaxBody = 8 << 20

// crawler is the Crawler of one source: a request at most every Pause,
// each traced as a client span, each recorded in the store.
type crawler struct {
	store  *store.Store
	source string
	http   *http.Client
	now    func() time.Time
	log    *slog.Logger
	last   time.Time
}

func (c *crawler) Fetch(ctx context.Context, url string) (io.ReadCloser, bool, error) {
	prev, _, err := c.store.CrawlState(ctx, c.source, url)
	if err != nil {
		return nil, false, err
	}
	return c.get(ctx, url, prev)
}

func (c *crawler) Once(ctx context.Context, url string) (io.ReadCloser, bool, error) {
	prev, ok, err := c.store.CrawlState(ctx, c.source, url)
	if err != nil {
		return nil, false, err
	}
	if ok && prev.Fetched() {
		return nil, false, nil
	}
	return c.get(ctx, url, store.Crawl{})
}

// get GETs the URL with prev's validators, a Pause after the last request,
// and records the answer.
func (c *crawler) get(ctx context.Context, url string, prev store.Crawl) (io.ReadCloser, bool, error) {
	if wait := c.last.Add(Pause).Sub(c.now()); wait > 0 {
		if err := sleep(ctx, wait); err != nil {
			return nil, false, err
		}
	}
	c.last = c.now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", "sniffarr")
	if prev.ETag != "" {
		req.Header.Set("If-None-Match", prev.ETag)
	}
	if prev.LastModified != "" {
		req.Header.Set("If-Modified-Since", prev.LastModified)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, false, c.record(ctx, store.Crawl{Source: c.source, URL: url, Error: err.Error()}, err)
	}
	rec := store.Crawl{Source: c.source, URL: url, Status: res.StatusCode, ETag: res.Header.Get("ETag"), LastModified: res.Header.Get("Last-Modified")}
	switch {
	case res.StatusCode == http.StatusNotModified:
		_ = res.Body.Close()
		rec.ETag, rec.LastModified = prev.ETag, prev.LastModified
		return nil, false, c.record(ctx, rec, nil)
	case res.StatusCode < 200 || res.StatusCode >= 300:
		_ = res.Body.Close()
		err := fmt.Errorf("%s: %s", url, res.Status)
		rec.Error = err.Error()
		return nil, false, c.record(ctx, rec, err)
	}
	if err := c.record(ctx, rec, nil); err != nil {
		_ = res.Body.Close()
		return nil, false, err
	}
	return body{io.LimitReader(res.Body, MaxBody), res.Body}, true, nil
}

// record keeps rec and returns err, or the store's error over it; a failed
// fetch is logged.
func (c *crawler) record(ctx context.Context, rec store.Crawl, err error) error {
	if err != nil {
		c.log.WarnContext(ctx, "fetch", "source", c.source, "url", rec.URL, "err", err)
	}
	if serr := c.store.RecordCrawl(ctx, rec, c.now()); serr != nil {
		return serr
	}
	return err
}

// body is a response body read to MaxBody.
type body struct {
	io.Reader
	io.Closer
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

// Infohash reads the v1 infohash of a magnet link, in either case.
func Infohash(magnet string) (store.Hash, bool) {
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

// Bytes is n of the binary unit, "GB" or "ГБ"; 0 for a unit it does not
// know.
func Bytes(n float64, unit string) int64 {
	switch strings.ToUpper(unit) {
	case "B", "Б":
		return int64(n)
	case "KB", "КБ":
		return int64(n * (1 << 10))
	case "MB", "МБ":
		return int64(n * (1 << 20))
	case "GB", "ГБ":
		return int64(n * (1 << 30))
	case "TB", "ТБ":
		return int64(n * (1 << 40))
	}
	return 0
}

// Outcomes of Recorder.Record.
const (
	// Stored is a new item whose torrent the store has.
	Stored = "stored"
	// Queued is a new item whose infohash was queued to be fetched.
	Queued = "queued"
	// Rejected is a new item whose torrent is rejected, so not queued.
	Rejected = "rejected"
	// Duplicate is an item recorded before.
	Duplicate = "duplicate"
)

// Recorder records items in the store, queues the infohashes of their
// torrents that are not stored, and counts them. It is safe for concurrent
// use.
type Recorder struct {
	Store *store.Store
	Tasks *store.Tasks
	Log   *slog.Logger
	Now   func() time.Time
	// Meter records metrics; the global MeterProvider when nil.
	Meter metric.MeterProvider

	once  sync.Once
	items metric.Int64Counter
}

func (r *Recorder) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Record records the item of the source and returns its outcome. The
// infohash of a new item whose torrent is neither stored nor rejected is
// queued under store.Feed. The outcome is journaled once recorded, in a
// transaction of its own; a failure to journal it is logged, not returned.
func (r *Recorder) Record(ctx context.Context, source string, it Item) (string, error) {
	r.init()
	now := r.now()
	rec, err := r.Store.RecordFeedItem(ctx, store.FeedItem{Source: source, Hash: it.Hash, Title: it.Title, URL: it.URL,
		Category: it.Category, Size: it.Size, PublishedAt: it.At}, now)
	if err != nil {
		return "", fmt.Errorf("record feed item: %w", err)
	}
	outcome, err := r.effect(ctx, rec, it.Hash)
	if err != nil {
		return "", err
	}
	if err := r.Store.JournalFeed(ctx, it.Hash, now, outcome, store.Detail("source", source, "title", it.Title, "url", it.URL,
		"category", it.Category, "size", it.Size, "published_at", it.At)); err != nil {
		r.Log.ErrorContext(ctx, "journal feed item", "source", source, "infohash", hex.EncodeToString(it.Hash[:]), "err", err)
	}
	r.items.Add(ctx, 1, metric.WithAttributes(attribute.String("sniffarr.feed.source", source), attribute.String("sniffarr.feed.outcome", outcome)))
	return outcome, nil
}

func (r *Recorder) effect(ctx context.Context, rec store.FeedRecorded, h store.Hash) (string, error) {
	switch {
	case !rec.New:
		return Duplicate, nil
	case rec.Stored:
		return Stored, nil
	}
	_, rejected, err := r.Store.Known(ctx, []store.Hash{h}, r.now())
	if err != nil {
		return "", fmt.Errorf("known: %w", err)
	}
	if rejected[h] {
		return Rejected, nil
	}
	if _, _, err := r.Tasks.Enqueue(ctx, []store.Sighting{{Hash: h, Discovery: store.Feed}}, r.now()); err != nil {
		return "", fmt.Errorf("enqueue: %w", err)
	}
	return Queued, nil
}

func (r *Recorder) init() {
	r.once.Do(func() {
		mp := r.Meter
		if mp == nil {
			mp = otel.GetMeterProvider()
		}
		var err error
		if r.items, err = mp.Meter(scope).Int64Counter("sniffarr.feed.items", metric.WithUnit("{item}"),
			metric.WithDescription("Releases tracker feeds listed, by source and what came of them.")); err != nil {
			otel.Handle(err)
		}
	})
}

// Defaults of Poller.
const (
	DefaultInterval = time.Hour
	DefaultRetry    = 10 * time.Minute
)

// Poller records what Source lists, at start and then once an Interval, at
// the moment of the interval Offset names. Zero fields take the default
// named in their doc.
type Poller struct {
	Source   Source
	Recorder *Recorder
	// Interval is how often Run reads the feed; DefaultInterval. Retry is
	// how long it waits after a read that failed, at most until the next
	// round; DefaultRetry.
	Interval time.Duration
	Retry    time.Duration
	Log      *slog.Logger
	// HTTP sends the source's requests, each traced as a client span;
	// http.DefaultClient.
	HTTP *http.Client
	// Now is time.Now when nil.
	Now func() time.Time
	// Node tells this node from others reading the same sources, so their
	// rounds fall at different moments; the store's hash key when empty.
	Node []byte

	once    sync.Once
	crawler *crawler
}

// Run reads the feed at once, then at every Interval of wall-clock time
// plus the source's Offset, until ctx is done. The rounds fall at the same
// moments across restarts, and sources with different names at different
// ones.
func (p *Poller) Run(ctx context.Context) error {
	if len(p.Node) == 0 {
		key, err := p.Recorder.Store.HashKey(ctx)
		if err != nil {
			return err
		}
		p.Node = key[:]
	}
	for {
		now := p.now()
		next := p.Next(now)
		if err := p.Round(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			p.Log.ErrorContext(ctx, "read feed", "source", p.Source.Name(), "err", err)
			if retry := p.now().Add(p.retry()); retry.Before(next) {
				next = retry
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Until(next)):
		}
	}
}

// Next is the first moment after now that falls on an Interval of
// wall-clock time plus Offset(Source.Name(), Node).
func (p *Poller) Next(now time.Time) time.Time {
	interval := p.interval()
	next := now.Truncate(interval).Add(Offset(p.Source.Name(), p.Node, interval))
	for !next.After(now) {
		next = next.Add(interval)
	}
	return next
}

// Offset is the share of interval the rounds of the source name fall at
// on the node: whole seconds fixed by the two, so two sources read at
// different moments, and so do two nodes reading one source.
func Offset(name string, node []byte, interval time.Duration) time.Duration {
	seconds := uint32(interval / time.Second)
	if seconds == 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write(node)
	_, _ = h.Write([]byte(name))
	return time.Duration(h.Sum32()%seconds) * time.Second
}

func (p *Poller) interval() time.Duration {
	if p.Interval > 0 {
		return p.Interval
	}
	return DefaultInterval
}

func (p *Poller) retry() time.Duration {
	if p.Retry > 0 {
		return p.Retry
	}
	return DefaultRetry
}

func (p *Poller) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// crawl is the source's Crawler, one for the life of the Poller so its
// pauses hold across rounds.
func (p *Poller) crawl() *crawler {
	p.once.Do(func() {
		c := p.HTTP
		if c == nil {
			c = http.DefaultClient
		}
		log := p.Log
		if log == nil {
			log = slog.New(slog.DiscardHandler)
		}
		p.crawler = &crawler{store: p.Recorder.Store, source: p.Source.Name(), http: telemetry.HTTPClient(c), now: p.now, log: log}
	})
	return p.crawler
}

// Round reads the feed once and records every item, logging how many were
// new.
func (p *Poller) Round(ctx context.Context) error {
	items, err := p.Source.Fetch(ctx, p.crawl())
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for _, it := range items {
		outcome, err := p.Recorder.Record(ctx, p.Source.Name(), it)
		if err != nil {
			return err
		}
		counts[outcome]++
	}
	if len(items) > counts[Duplicate] {
		p.Log.InfoContext(ctx, "read feed", "source", p.Source.Name(), "items", len(items),
			"queued", counts[Queued], "stored", counts[Stored], "rejected", counts[Rejected])
	}
	return nil
}
