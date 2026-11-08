package arr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"sarak.sh/mikluko/sniffarr/internal/store"
	"sarak.sh/mikluko/sniffarr/internal/telemetry/telemetrytest"
)

// fakeArr serves records as the history API of the app does, paged in date
// order, and remembers the queries it was asked.
type fakeArr struct {
	*httptest.Server
	mu      sync.Mutex
	records []map[string]any
	queries []string
}

func newArr(t *testing.T, app string, records ...map[string]any) *fakeArr {
	f := &fakeArr{records: records}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		q := r.URL.Query()
		f.queries = append(f.queries, r.URL.Path+"?"+q.Encode())
		include := "includeMovie"
		if app == store.AppSonarr {
			include = "includeSeries"
		}
		if r.Header.Get("X-Api-Key") != "key" || q.Get("eventType") != "1" || q.Get(include) != "true" {
			http.Error(w, "{}", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/sub/api/v3/history":
			page, _ := strconv.Atoi(q.Get("page"))
			size, _ := strconv.Atoi(q.Get("pageSize"))
			if q.Get("sortKey") != "date" || q.Get("sortDirection") != "ascending" || page < 1 || size < 1 {
				http.Error(w, "{}", http.StatusBadRequest)
				return
			}
			from, to := min((page-1)*size, len(f.records)), min(page*size, len(f.records))
			_ = json.NewEncoder(w).Encode(map[string]any{"page": page, "pageSize": size, "sortKey": "date", "sortDirection": "ascending",
				"totalRecords": len(f.records), "records": f.records[from:to]})
		case "/sub/api/v3/history/since":
			since, err := time.Parse(time.RFC3339, q.Get("date"))
			if err != nil {
				http.Error(w, "{}", http.StatusBadRequest)
				return
			}
			out := []map[string]any{}
			for _, r := range f.records {
				if !r["date"].(time.Time).Before(since) {
					out = append(out, r)
				}
			}
			_ = json.NewEncoder(w).Encode(out)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func movieRecord(b byte, tmdbID int, at time.Time) map[string]any {
	return map[string]any{"id": int(b), "movieId": tmdbID, "sourceTitle": "Movie." + strconv.Itoa(tmdbID) + ".1080p", "date": at,
		"downloadId": hexOf(b), "eventType": "grabbed",
		"data":  map[string]any{"indexer": "Tracker", "size": "8589934592", "torrentInfoHash": hexOf(b), "tmdbId": strconv.Itoa(tmdbID)},
		"movie": map[string]any{"id": tmdbID, "title": "Movie", "tmdbId": tmdbID, "imdbId": "tt" + strconv.Itoa(tmdbID)}}
}

func (f *fixture) history(app, url string) *History {
	return &History{App: app, URL: url + "/sub/api/v3/", APIKey: "key", Recorder: f.rec, PageSize: 2, Log: f.rec.Log}
}

func TestHistoryRadarr(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	t1 := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	arr := newArr(t, store.AppRadarr, movieRecord(1, 348, t1), movieRecord(2, 8077, t1.Add(time.Minute)), movieRecord(3, 348, t1.Add(2*time.Minute)),
		movieRecord(4, 679, t1.Add(3*time.Minute)), movieRecord(5, 348, t1.Add(4*time.Minute)))
	code, answer := f.post(t, "", "secret", radarrGrab(hexOf(3)))
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, Queued, answer)

	h := f.history(store.AppRadarr, arr.URL)
	require.NoError(t, h.round(ctx))
	require.Len(t, arr.queries, 3, "five records, two a page")
	done, err := f.rec.Store.ArrImported(ctx, store.AppRadarr)
	require.NoError(t, err)
	require.True(t, done)
	id, err := f.rec.Store.WorkByTMDB(ctx, store.TypeMovie, 348)
	require.NoError(t, err)
	sums, err := f.rec.Store.Summaries(ctx, []int64{id})
	require.NoError(t, err)
	require.Equal(t, int64(3), sums[id].GrabsWeek, "the webhook's grab and the history's are one")
	for _, b := range []byte{1, 2, 4, 5} {
		d, err := f.rec.Tasks.Discovery(ctx, hash(b))
		require.NoError(t, err)
		require.Equal(t, store.Arr, d)
	}

	arr.mu.Lock()
	arr.records = append(arr.records, movieRecord(6, 679, time.Now().UTC().Truncate(time.Second)))
	arr.queries = nil
	arr.mu.Unlock()
	require.NoError(t, h.round(ctx))
	require.Len(t, arr.queries, 1)
	require.Contains(t, arr.queries[0], "/sub/api/v3/history/since?")
	id, err = f.rec.Store.WorkByTMDB(ctx, store.TypeMovie, 679)
	require.NoError(t, err)
	sums, err = f.rec.Store.Summaries(ctx, []int64{id})
	require.NoError(t, err)
	require.Equal(t, int64(2), sums[id].GrabsWeek, "the round after the import picks up the new grab")
}

func TestHistorySonarr(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	episode := func(b byte, season, number int, tmdbID int) map[string]any {
		return map[string]any{"episodeId": number, "seriesId": 3, "sourceTitle": "Breaking.Bad.S02.1080p", "date": at, "downloadId": hexOf(b),
			"eventType": "grabbed", "data": map[string]any{"indexer": "Tracker", "size": "42949672960"},
			"episode": map[string]any{"seasonNumber": season, "episodeNumber": number},
			"series":  map[string]any{"title": "Breaking Bad", "tvdbId": 81189, "tmdbId": tmdbID, "imdbId": "tt0903747"}}
	}
	arr := newArr(t, store.AppSonarr, episode(7, 2, 1, 0), episode(7, 2, 2, 0), episode(7, 2, 3, 0), episode(8, 1, 5, 1396))
	h := f.history(store.AppSonarr, arr.URL)
	h.PageSize = 10
	n, err := h.Import(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, n, "the episodes of a season pack are one grab")
	h7 := hash(7)
	var season, ep, size int64
	require.NoError(t, f.db.R.QueryRowContext(ctx, "SELECT coalesce(season, 0), coalesce(episode, 0), size FROM arr_events WHERE infohash = ?", h7[:]).Scan(&season, &ep, &size))
	require.Equal(t, int64(2), season)
	require.Zero(t, ep)
	require.Equal(t, int64(42949672960), size)
	require.Equal(t, int64(1), f.finder.calls.Load(), "the pack's TVDB ID is looked up once; the episode with a TMDB ID needs none")
}

func TestHistoryTrace(t *testing.T) {
	f := setup(t)
	arr := newArr(t, store.AppRadarr)
	telemetrytest.RequireClientChild(t, func(ctx context.Context) {
		_, err := f.history(store.AppRadarr, arr.URL).Import(ctx)
		require.NoError(t, err)
	})
}
