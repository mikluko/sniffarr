package tmdb

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

// DefaultURL is the base URL of the TMDB API.
const DefaultURL = "https://api.themoviedb.org"

// DefaultRate is the default of TMDB requests a second.
const DefaultRate = 20

// Client is a client of the TMDB API v3. Its errors never carry the key.
type Client struct {
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

// DefaultKey is the TMDB read access token sniffarr ships with; TMDB_API_KEY
// overrides it, and an empty one turns matching off.
const DefaultKey = "eyJhbGciOiJIUzI1NiJ9.eyJhdWQiOiJlMGMwZjVhNmI1ZTU5NTc3Mjg3NTQ5ZDI0NzlmYWEwYSIsIm5iZiI6MTc5MjQ3NTcwOC45MTkwMDAxLCJzdWIiOiI2YWQ3MDIzYzg4YzI5ODYwNTNiZWQ5YzMiLCJzY29wZXMiOlsiYXBpX3JlYWQiXSwidmVyc2lvbiI6MX0.GzOo3vXlML2qXMF6DydhMkag0X9LGg7Ydk5e733IS9I"

// NewClient returns a client of the production API for the key, at most rps
// requests a second.
func NewClient(key string, rps int) *Client {
	return &Client{
		Key:     key,
		URL:     DefaultURL,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		Limiter: rate.NewLimiter(rate.Limit(rps), max(rps/4, 1)),
		Retries: 4,
		Backoff: time.Second,
	}
}

// Error is a non-200 answer from TMDB.
type Error struct {
	Status int
	Path   string
}

func (e *Error) Error() string {
	return fmt.Sprintf("tmdb %s: %d %s", e.Path, e.Status, http.StatusText(e.Status))
}

// Permanent reports whether repeating the same request cannot succeed.
func (e *Error) Permanent() bool {
	return e.Status >= 400 && e.Status < 500 && e.Status != http.StatusTooManyRequests && e.Status != http.StatusUnauthorized
}

// Candidate is a work a TMDB search found.
type Candidate struct {
	ID            int
	Title         string
	OriginalTitle string
	// Year is the year of release or first air, 0 when TMDB has none.
	Year       int
	Overview   string
	PosterPath string
	// Popularity is TMDB's score of recent attention; it has no unit and is
	// comparable only between candidates of one search.
	Popularity float64
	Votes      int
	// Language is the original language, ISO 639-1; Countries the origin
	// countries, ISO 3166-1 alpha-2, which the search gives for series only.
	Language  string
	Countries []string
}

// Search searches movies, or series when series is set, by title in the
// language; year, unless 0, narrows a movie search.
func (c *Client) Search(ctx context.Context, series bool, query string, year int, language string) ([]Candidate, error) {
	q := url.Values{"query": {query}, "language": {language}}
	path := "/3/search/movie"
	if series {
		path = "/3/search/tv"
	} else if year != 0 {
		q.Set("year", strconv.Itoa(year))
	}
	var res struct {
		Results []struct {
			ID            int      `json:"id"`
			Title         string   `json:"title"`
			OriginalTitle string   `json:"original_title"`
			ReleaseDate   string   `json:"release_date"`
			Name          string   `json:"name"`
			OriginalName  string   `json:"original_name"`
			FirstAirDate  string   `json:"first_air_date"`
			Overview      string   `json:"overview"`
			PosterPath    string   `json:"poster_path"`
			Popularity    float64  `json:"popularity"`
			VoteCount     int      `json:"vote_count"`
			Language      string   `json:"original_language"`
			Countries     []string `json:"origin_country"`
		} `json:"results"`
	}
	if err := c.get(ctx, path, q, &res); err != nil {
		return nil, err
	}
	out := make([]Candidate, len(res.Results))
	for i, r := range res.Results {
		out[i] = Candidate{ID: r.ID, Title: r.Title + r.Name, OriginalTitle: r.OriginalTitle + r.OriginalName,
			Year: yearOf(r.ReleaseDate + r.FirstAirDate), Overview: r.Overview, PosterPath: r.PosterPath,
			Popularity: r.Popularity, Votes: r.VoteCount, Language: r.Language, Countries: r.Countries}
	}
	return out, nil
}

// Details is what TMDB tells of a movie or series in one language, with its
// IDs with other providers.
type Details struct {
	ID            int
	Title         string
	OriginalTitle string
	// Year is the year of release or first air, 0 when TMDB has none.
	Year       int
	PosterPath string
	Overview   string
	Genres     []string
	Rating     float64
	Votes      int
	// Runtime is in minutes: a movie's, or the first typical episode runtime
	// of a series; 0 when TMDB has none.
	Runtime int
	// Status and Seasons are a series'.
	Status  string
	Seasons int
	// Countries are the origin countries, or the production countries when
	// TMDB names no origin, as ISO 3166-1 alpha-2; Language is ISO 639-1.
	Countries []string
	Language  string
	// IDs are the IMDb, TVDB and Wikidata IDs by provider; absent IDs are
	// left out.
	IDs map[string]string
}

// Details returns the movie, or the series when series is set, with the ID
// in the language, an IETF tag such as en-US; text TMDB has no translation
// of comes as TMDB falls back.
func (c *Client) Details(ctx context.Context, series bool, id int, language string) (Details, error) {
	path := "/3/movie/" + strconv.Itoa(id)
	if series {
		path = "/3/tv/" + strconv.Itoa(id)
	}
	var res struct {
		ID                  int                     `json:"id"`
		Title               string                  `json:"title"`
		Name                string                  `json:"name"`
		OriginalTitle       string                  `json:"original_title"`
		OriginalName        string                  `json:"original_name"`
		ReleaseDate         string                  `json:"release_date"`
		FirstAirDate        string                  `json:"first_air_date"`
		PosterPath          string                  `json:"poster_path"`
		Overview            string                  `json:"overview"`
		Genres              []struct{ Name string } `json:"genres"`
		VoteAverage         float64                 `json:"vote_average"`
		VoteCount           int                     `json:"vote_count"`
		Runtime             int                     `json:"runtime"`
		EpisodeRunTime      []int                   `json:"episode_run_time"`
		Status              string                  `json:"status"`
		NumberOfSeasons     int                     `json:"number_of_seasons"`
		OriginCountry       []string                `json:"origin_country"`
		ProductionCountries []struct {
			ISO string `json:"iso_3166_1"`
		} `json:"production_countries"`
		OriginalLanguage string `json:"original_language"`
		ExternalIDs      struct {
			IMDb     string `json:"imdb_id"`
			TVDB     int    `json:"tvdb_id"`
			Wikidata string `json:"wikidata_id"`
		} `json:"external_ids"`
	}
	if err := c.get(ctx, path, url.Values{"language": {language}, "append_to_response": {"external_ids"}}, &res); err != nil {
		return Details{}, err
	}
	d := Details{ID: res.ID, Title: res.Title + res.Name, OriginalTitle: res.OriginalTitle + res.OriginalName,
		Year: yearOf(res.ReleaseDate + res.FirstAirDate), PosterPath: res.PosterPath, Overview: res.Overview,
		Rating: res.VoteAverage, Votes: res.VoteCount, Runtime: res.Runtime, Countries: res.OriginCountry,
		Language: res.OriginalLanguage, IDs: map[string]string{}}
	for _, g := range res.Genres {
		d.Genres = append(d.Genres, g.Name)
	}
	if series {
		d.Status, d.Seasons = res.Status, res.NumberOfSeasons
		if len(res.EpisodeRunTime) > 0 {
			d.Runtime = res.EpisodeRunTime[0]
		}
	}
	if len(d.Countries) == 0 {
		for _, c := range res.ProductionCountries {
			d.Countries = append(d.Countries, c.ISO)
		}
	}
	if res.ExternalIDs.IMDb != "" {
		d.IDs["imdb"] = res.ExternalIDs.IMDb
	}
	if res.ExternalIDs.TVDB != 0 {
		d.IDs["tvdb"] = strconv.Itoa(res.ExternalIDs.TVDB)
	}
	if res.ExternalIDs.Wikidata != "" {
		d.IDs["wikidata"] = res.ExternalIDs.Wikidata
	}
	return d, nil
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
func (c *Client) get(ctx context.Context, path string, q url.Values, v any) error {
	wait := c.Backoff
	for attempt := 0; ; attempt++ {
		retryAfter, err := c.try(ctx, path, q, v)
		var tErr *Error
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
func (c *Client) try(ctx context.Context, path string, q url.Values, v any) (time.Duration, error) {
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
		return time.Duration(secs) * time.Second, &Error{Status: res.StatusCode, Path: path}
	}
	if err := json.NewDecoder(res.Body).Decode(v); err != nil {
		return 0, fmt.Errorf("tmdb %s: decode answer: %w", path, err)
	}
	return 0, nil
}

func (c *Client) count(ctx context.Context, status string) {
	c.once.Do(func() {
		mp := c.Meter
		if mp == nil {
			mp = otel.GetMeterProvider()
		}
		var err error
		c.requests, err = mp.Meter(scope).Int64Counter("sniffarr.tmdb.requests", metric.WithUnit("{request}"),
			metric.WithDescription("Requests to TMDB, by response status or error."))
		if err != nil {
			otel.Handle(err)
		}
	})
	c.requests.Add(ctx, 1, metric.WithAttributes(attribute.String("sniffarr.tmdb.status", status)))
}
