package arr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mikluko/sniffarr/internal/store"
	"github.com/mikluko/sniffarr/internal/telemetry"
)

// Defaults of History.
const (
	DefaultPageSize = 250
	DefaultInterval = 24 * time.Hour
	DefaultRetry    = time.Hour
)

// grabbed is the history event type of a grab in the history API, the same
// number in Radarr and Sonarr.
const grabbed = "1"

// History imports the grabs in the history of one Radarr or Sonarr through
// its v3 API, recording them with Recorder. Zero fields take the default
// named in their doc.
type History struct {
	// App is store.AppRadarr or store.AppSonarr.
	App string
	// URL is the app's base URL, with its URL base if it has one.
	URL    string
	APIKey string
	// HTTP sends the requests, each traced as a client span;
	// http.DefaultClient. It is read on the first request.
	HTTP     *http.Client
	Recorder *Recorder
	// PageSize is how many records Import asks for at once;
	// DefaultPageSize.
	PageSize int
	// Interval is how often Run picks up the grabs since the latest
	// recorded; DefaultInterval. Retry is how long it waits after a round
	// that failed; DefaultRetry.
	Interval time.Duration
	Retry    time.Duration
	Log      *slog.Logger

	once   sync.Once
	traced *http.Client
}

// record is a history record of the history API, Radarr's and Sonarr's
// together.
type record struct {
	SourceTitle string            `json:"sourceTitle"`
	Date        time.Time         `json:"date"`
	DownloadID  string            `json:"downloadId"`
	Data        map[string]string `json:"data"`
	Movie       *movie            `json:"movie"`
	Series      *series           `json:"series"`
	Episode     *episode          `json:"episode"`
}

// Run imports the whole history once per store and then, at start and
// every Interval, the grabs since the latest recorded one of the app, until
// ctx is done.
func (h *History) Run(ctx context.Context) error {
	interval, retry := h.Interval, h.Retry
	if interval <= 0 {
		interval = DefaultInterval
	}
	if retry <= 0 {
		retry = DefaultRetry
	}
	for {
		wait := interval
		if err := h.round(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			h.Log.ErrorContext(ctx, "import arr history", "app", h.App, "err", err)
			wait = retry
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// round imports the whole history if it was never imported, or else the
// grabs since the latest recorded one.
func (h *History) round(ctx context.Context) error {
	s := h.Recorder.Store
	done, err := s.ArrImported(ctx, h.App)
	if err != nil {
		return err
	}
	latest, found, err := s.LatestArr(ctx, h.App, store.EventGrab)
	if err != nil {
		return err
	}
	if done && found {
		n, err := h.Since(ctx, latest)
		if n > 0 {
			h.Log.InfoContext(ctx, "imported arr history", "app", h.App, "since", latest, "events", n)
		}
		return err
	}
	n, err := h.Import(ctx)
	if err != nil {
		return err
	}
	h.Log.InfoContext(ctx, "imported arr history", "app", h.App, "events", n)
	return s.SetArrImported(ctx, h.App)
}

// Import records every grab in the history, oldest first, and returns how
// many events it recorded that were not recorded before. The records of one
// download, such as those of a season pack's episodes, are one event.
func (h *History) Import(ctx context.Context) (int, error) {
	size := h.PageSize
	if size <= 0 {
		size = DefaultPageSize
	}
	total := 0
	for page := 1; ; page++ {
		q := h.query()
		q.Set("page", strconv.Itoa(page))
		q.Set("pageSize", strconv.Itoa(size))
		q.Set("sortKey", "date")
		q.Set("sortDirection", "ascending")
		var res struct {
			TotalRecords int      `json:"totalRecords"`
			Records      []record `json:"records"`
		}
		if err := h.get(ctx, "/api/v3/history", q, &res); err != nil {
			return total, err
		}
		n, err := h.record(ctx, res.Records)
		total += n
		if err != nil || len(res.Records) == 0 || page*size >= res.TotalRecords {
			return total, err
		}
	}
}

// Since records the grabs in the history at or after t and returns how many
// events it recorded that were not recorded before.
func (h *History) Since(ctx context.Context, t time.Time) (int, error) {
	q := h.query()
	q.Set("date", t.UTC().Format(time.RFC3339))
	var res []record
	if err := h.get(ctx, "/api/v3/history/since", q, &res); err != nil {
		return 0, err
	}
	return h.record(ctx, res)
}

// query is the query every history request has: grabs only, with the
// movie or the series and episode of each.
func (h *History) query() url.Values {
	q := url.Values{"eventType": {grabbed}}
	if h.App == store.AppSonarr {
		q.Set("includeSeries", "true")
		q.Set("includeEpisode", "true")
	} else {
		q.Set("includeMovie", "true")
	}
	return q
}

// record records the records as events, those of one download as one, and
// returns how many were not recorded before.
func (h *History) record(ctx context.Context, records []record) (int, error) {
	var order []string
	byKey := map[string]Event{}
	for _, r := range records {
		e := h.event(r)
		k := key(e)
		prev, ok := byKey[k]
		if !ok {
			order = append(order, k)
			byKey[k] = e
			continue
		}
		if prev.Season != e.Season {
			prev.Season = 0
		}
		prev.Episode = 0
		byKey[k] = prev
	}
	n := 0
	for _, k := range order {
		outcome, err := h.Recorder.Record(ctx, SourceHistory, byKey[k])
		if err != nil {
			return n, err
		}
		if outcome != Duplicate {
			n++
		}
	}
	return n, nil
}

// event is the grab the record tells.
func (h *History) event(r record) Event {
	e := Event{App: h.App, Kind: store.EventGrab, DownloadID: r.DownloadID, Title: r.SourceTitle, At: r.Date.UTC(),
		InfoHash: datum(r.Data, "torrentInfoHash"), Indexer: datum(r.Data, "indexer")}
	e.Size, _ = strconv.ParseInt(datum(r.Data, "size"), 10, 64)
	switch {
	case r.Movie != nil:
		e.TMDBID, e.IMDbID = r.Movie.TMDBID, r.Movie.IMDbID
	case r.Series != nil:
		e.TMDBID, e.TVDBID, e.IMDbID = r.Series.TMDBID, r.Series.TVDBID, r.Series.IMDbID
	}
	if e.TMDBID == 0 && h.App == store.AppRadarr {
		e.TMDBID, _ = strconv.Atoi(datum(r.Data, "tmdbId"))
	}
	if r.Episode != nil {
		e.Season, e.Episode = r.Episode.SeasonNumber, r.Episode.EpisodeNumber
	}
	return e
}

// datum is the record's data under the key, in any case.
func datum(data map[string]string, key string) string {
	for k, v := range data {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

// get fetches the path of the API and decodes its answer into v.
func (h *History) get(ctx context.Context, path string, q url.Values, v any) error {
	h.once.Do(func() { h.traced = telemetry.HTTPClient(h.HTTP) })
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(h.URL, "/")+path+"?"+q.Encode(), nil)
	if err != nil {
		return fmt.Errorf("%s %s: %w", h.App, path, err)
	}
	req.Header.Set("X-Api-Key", h.APIKey)
	req.Header.Set("Accept", "application/json")
	res, err := h.traced.Do(req)
	if err != nil {
		var uErr *url.Error
		if errors.As(err, &uErr) {
			err = uErr.Err
		}
		return fmt.Errorf("%s %s: %w", h.App, path, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
		return fmt.Errorf("%s %s: %d %s", h.App, path, res.StatusCode, http.StatusText(res.StatusCode))
	}
	if err := json.NewDecoder(res.Body).Decode(v); err != nil {
		return fmt.Errorf("%s %s: decode answer: %w", h.App, path, err)
	}
	return nil
}
