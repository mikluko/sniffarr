package titles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/time/rate"
)

// DefaultTMDBURL is the base URL of the TMDB API.
const DefaultTMDBURL = "https://api.themoviedb.org"

// DefaultRate is the default of TMDB requests a second.
const DefaultRate = 20

// TMDB is a client of the TMDB API v3. Its errors never carry the key.
type TMDB struct {
	// Key is a v3 API key, or an API read access token.
	Key  string
	URL  string
	HTTP *http.Client
	// Limiter paces every request, retries too.
	Limiter *rate.Limiter
	// Retries is how many times a 429 or 5xx answer is retried.
	Retries int
	// Backoff is the wait before the first retry when the answer names none;
	// it doubles each time.
	Backoff time.Duration
	// Meter records metrics; the global MeterProvider when nil.
	Meter metric.MeterProvider

	once     sync.Once
	requests metric.Int64Counter
}

// NewTMDB returns a client of the production API for the key, at most rps
// requests a second.
func NewTMDB(key string, rps int) *TMDB {
	return &TMDB{
		Key:     key,
		URL:     DefaultTMDBURL,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		Limiter: rate.NewLimiter(rate.Limit(rps), max(rps/4, 1)),
		Retries: 4,
		Backoff: time.Second,
	}
}

// TMDBError is a non-200 answer from TMDB.
type TMDBError struct {
	Status int
	Path   string
}

func (e *TMDBError) Error() string {
	return fmt.Sprintf("tmdb %s: %d %s", e.Path, e.Status, http.StatusText(e.Status))
}

// Permanent reports whether repeating the same request cannot succeed.
func (e *TMDBError) Permanent() bool {
	return e.Status >= 400 && e.Status < 500 && e.Status != http.StatusTooManyRequests && e.Status != http.StatusUnauthorized
}

// Candidate is a work a TMDB search found.
type Candidate struct {
	ID            int
	Title         string
	OriginalTitle string
	// Year is the year of release or first air, 0 when TMDB has none.
	Year     int
	Overview string
}

// Search searches movies, or series when series is set, by title in the
// language; year, unless 0, narrows a movie search.
func (c *TMDB) Search(ctx context.Context, series bool, query string, year int, language string) ([]Candidate, error) {
	q := url.Values{"query": {query}, "language": {language}}
	path := "/3/search/movie"
	if series {
		path = "/3/search/tv"
	} else if year != 0 {
		q.Set("year", strconv.Itoa(year))
	}
	var res struct {
		Results []struct {
			ID            int    `json:"id"`
			Title         string `json:"title"`
			OriginalTitle string `json:"original_title"`
			ReleaseDate   string `json:"release_date"`
			Name          string `json:"name"`
			OriginalName  string `json:"original_name"`
			FirstAirDate  string `json:"first_air_date"`
			Overview      string `json:"overview"`
		} `json:"results"`
	}
	if err := c.get(ctx, path, q, &res); err != nil {
		return nil, err
	}
	out := make([]Candidate, len(res.Results))
	for i, r := range res.Results {
		out[i] = Candidate{ID: r.ID, Title: r.Title + r.Name, OriginalTitle: r.OriginalTitle + r.OriginalName,
			Year: yearOf(r.ReleaseDate + r.FirstAirDate), Overview: r.Overview}
	}
	return out, nil
}

// ExternalIDs returns the IMDb, TVDB and Wikidata IDs of the movie, or the
// series when series is set, by provider; absent IDs are left out.
func (c *TMDB) ExternalIDs(ctx context.Context, series bool, id int) (map[string]string, error) {
	path := "/3/movie/" + strconv.Itoa(id) + "/external_ids"
	if series {
		path = "/3/tv/" + strconv.Itoa(id) + "/external_ids"
	}
	var res struct {
		IMDb     string `json:"imdb_id"`
		TVDB     int    `json:"tvdb_id"`
		Wikidata string `json:"wikidata_id"`
	}
	if err := c.get(ctx, path, url.Values{}, &res); err != nil {
		return nil, err
	}
	out := map[string]string{}
	if res.IMDb != "" {
		out["imdb"] = res.IMDb
	}
	if res.TVDB != 0 {
		out["tvdb"] = strconv.Itoa(res.TVDB)
	}
	if res.Wikidata != "" {
		out["wikidata"] = res.Wikidata
	}
	return out, nil
}

func yearOf(date string) int {
	if len(date) < 4 {
		return 0
	}
	y, _ := strconv.Atoi(date[:4])
	return y
}

// get fetches path and decodes the answer into v, retrying 429 and 5xx
// answers.
func (c *TMDB) get(ctx context.Context, path string, q url.Values, v any) error {
	wait := c.Backoff
	for attempt := 0; ; attempt++ {
		retryAfter, err := c.try(ctx, path, q, v)
		var tErr *TMDBError
		if err == nil || !errors.As(err, &tErr) || !(tErr.Status == http.StatusTooManyRequests || tErr.Status >= 500) || attempt >= c.Retries {
			return err
		}
		d := wait
		if retryAfter > 0 {
			d = retryAfter
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
		wait *= 2
	}
}

// try makes one request; on a 429 it returns the wait the answer asks for.
func (c *TMDB) try(ctx context.Context, path string, q url.Values, v any) (time.Duration, error) {
	if err := c.Limiter.Wait(ctx); err != nil {
		return 0, err
	}
	bearer := strings.Count(c.Key, ".") == 2
	if !bearer {
		q.Set("api_key", c.Key)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+path+"?"+q.Encode(), nil)
	if err != nil {
		return 0, fmt.Errorf("tmdb %s: build request", path)
	}
	if bearer {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	req.Header.Set("Accept", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		c.count(ctx, "error")
		var uErr *url.Error
		if errors.As(err, &uErr) {
			err = uErr.Err
		}
		return 0, fmt.Errorf("tmdb %s: %w", path, err)
	}
	defer res.Body.Close()
	c.count(ctx, strconv.Itoa(res.StatusCode))
	if res.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
		secs, _ := strconv.Atoi(res.Header.Get("Retry-After"))
		return time.Duration(secs) * time.Second, &TMDBError{Status: res.StatusCode, Path: path}
	}
	if err := json.NewDecoder(res.Body).Decode(v); err != nil {
		return 0, fmt.Errorf("tmdb %s: decode answer: %w", path, err)
	}
	return 0, nil
}

func (c *TMDB) count(ctx context.Context, status string) {
	c.once.Do(func() {
		mp := c.Meter
		if mp == nil {
			mp = otel.GetMeterProvider()
		}
		var err error
		c.requests, err = mp.Meter(scope).Int64Counter("sniffarr.titles.requests", metric.WithUnit("{request}"),
			metric.WithDescription("Requests to TMDB, by response status or error."))
		if err != nil {
			otel.Handle(err)
		}
	})
	c.requests.Add(ctx, 1, metric.WithAttributes(attribute.String("sniffarr.titles.status", status)))
}
