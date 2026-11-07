package arr

import (
	"context"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/database"
	"github.com/mikluko/sniffarr/internal/search"
	"github.com/mikluko/sniffarr/internal/store"
)

// fakeFinder knows Breaking Bad, TVDB 81189, as TMDB 1396.
type fakeFinder struct{ calls atomic.Int64 }

func (f *fakeFinder) FindTVDB(_ context.Context, tvdbID int) (int, error) {
	f.calls.Add(1)
	if tvdbID == 81189 {
		return 1396, nil
	}
	return 0, nil
}

type fixture struct {
	db     *database.DB
	rec    *Recorder
	finder *fakeFinder
	reader *sdkmetric.ManualReader
	hook   *httptest.Server
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	s, err := store.New(ctx, db, store.Options{TMDB: true})
	require.NoError(t, err)
	q, err := store.NewTasks(ctx, db, store.TaskOptions{})
	require.NoError(t, err)
	f := &fixture{db: db, finder: &fakeFinder{}, reader: sdkmetric.NewManualReader()}
	f.rec = &Recorder{Store: s, Tasks: q, Finder: f.finder, Log: slog.New(slog.DiscardHandler),
		Meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(f.reader))}
	f.hook = httptest.NewServer(&Webhook{Recorder: f.rec, Token: "secret", Log: f.rec.Log})
	t.Cleanup(f.hook.Close)
	return f
}

func hash(b byte) store.Hash {
	var h store.Hash
	h[0] = b
	return h
}

func hexOf(b byte) string {
	h := hash(b)
	return strings.ToUpper(hex.EncodeToString(h[:]))
}

// post sends the body to the webhook with the password, and returns the
// status and the answer.
func (f *fixture) post(t *testing.T, query, password, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.hook.URL+"/"+query, strings.NewReader(body))
	require.NoError(t, err)
	if password != "" {
		req.SetBasicAuth("anyone", password)
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	return res.StatusCode, strings.TrimSpace(string(b))
}

func radarrGrab(downloadID string) string {
	return `{"eventType":"Grab","instanceName":"Radarr","applicationUrl":"http://radarr:7878",
		"movie":{"id":7,"title":"Alien","year":1979,"releaseDate":"1979-05-25","tmdbId":348,"imdbId":"tt0078748"},
		"remoteMovie":{"tmdbId":348,"imdbId":"tt0078748","title":"Alien","year":1979},
		"release":{"quality":"Bluray-1080p","qualityVersion":1,"releaseGroup":"GRP","releaseTitle":"Alien.1979.1080p.BluRay.x264-GRP","indexer":"Tracker (Prowlarr)","size":8589934592,"customFormatScore":0},
		"downloadClient":"qBittorrent","downloadClientType":"qBittorrent","downloadId":"` + downloadID + `"}`
}

func TestWebhookAuth(t *testing.T) {
	f := setup(t)
	test := `{"eventType":"Test","instanceName":"Radarr","movie":{"id":1,"title":"Test Title","tmdbId":0}}`
	code, _ := f.post(t, "", "", test)
	require.Equal(t, http.StatusUnauthorized, code, "no credentials")
	code, _ = f.post(t, "", "wrong", test)
	require.Equal(t, http.StatusUnauthorized, code, "wrong password")
	code, answer := f.post(t, "", "secret", test)
	require.Equal(t, http.StatusOK, code, "the password, with any username")
	require.Equal(t, "ignored", answer)

	req, err := http.NewRequest(http.MethodPost, f.hook.URL, strings.NewReader(test))
	require.NoError(t, err)
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, `Basic realm="sniffarr"`, res.Header.Get("WWW-Authenticate"))
}

func TestWebhookIgnores(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	for _, body := range []string{
		`{"eventType":"Test","instanceName":"Sonarr","series":{"id":1,"title":"Test Title","tvdbId":1234},"episodes":[{"seasonNumber":1,"episodeNumber":1}]}`,
		`{"eventType":"Rename","movie":{"id":1,"tmdbId":348}}`,
		`{"eventType":"HealthRestored"}`,
	} {
		code, answer := f.post(t, "", "secret", body)
		require.Equal(t, http.StatusOK, code, body)
		require.Equal(t, "ignored", answer)
	}
	for _, app := range []string{store.AppRadarr, store.AppSonarr} {
		_, ok, err := f.rec.Store.LatestArr(ctx, app, store.EventGrab)
		require.NoError(t, err)
		require.False(t, ok, "nothing is stored")
	}
	code, _ := f.post(t, "?app=lidarr", "secret", radarrGrab(hexOf(1)))
	require.Equal(t, http.StatusBadRequest, code)
	code, _ = f.post(t, "", "secret", `{"eventType":"Grab","release":{"releaseTitle":"x"},"downloadId":"`+hexOf(1)+`"}`)
	require.Equal(t, http.StatusBadRequest, code, "neither the payload nor the query tells the app")
	code, _ = f.post(t, "", "secret", `{"eventType":`)
	require.Equal(t, http.StatusBadRequest, code)
}

func TestWebhookGrabStored(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	now := time.Now().Truncate(time.Second)
	f.rec.Now = func() time.Time { return now }
	s := f.rec.Store
	_, _, err := s.Insert(ctx, store.Torrent{Hash: hash(1), Name: "Alien.1979.1080p.BluRay.x264-GRP", Size: 8 << 30, Discovery: store.Sample, SeenAt: time.Now(),
		Kind: "movie", KindLayer: store.LayerRules, TitleKey: "y:alien 1979"})
	require.NoError(t, err)

	code, answer := f.post(t, "", "secret", radarrGrab(hexOf(1)))
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, Matched, answer)
	r, err := s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.NotZero(t, r.Work)
	require.Equal(t, store.LayerArr, r.Match.Layer)
	require.Equal(t, 348, r.Match.Work.TMDBID)

	_, _, err = s.RecordMatch(ctx, "y:alien 1979", store.Found{Hit: &store.Hit{Type: store.TypeMovie, TMDBID: 8077, Title: "Alien³"}, Confidence: 1, Layer: store.LayerTMDB}, time.Now())
	require.NoError(t, err)
	r, err = s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.Equal(t, store.LayerArr, r.Match.Layer, "a later TMDB match leaves the arr match")
	require.Equal(t, 348, r.Match.Work.TMDBID)

	code, answer = f.post(t, "", "secret", radarrGrab(hexOf(1)))
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, Duplicate, answer)

	journal, err := s.Journal(ctx, hash(1), 10)
	require.NoError(t, err)
	require.Len(t, journal, 3, "stored, grabbed, matched; the duplicate adds nothing")
	require.Equal(t, store.JournalArr, journal[1].Kind)
	require.JSONEq(t, `{"app":"radarr","source":"webhook","indexer":"Tracker (Prowlarr)","title":"Alien.1979.1080p.BluRay.x264-GRP","size":8589934592,"work":`+strconv.FormatInt(r.Work, 10)+`}`,
		journal[1].Detail, "a webhook's event is recorded as it happens")
	require.Equal(t, now.UTC(), journal[1].At)

	var rm metricdata.ResourceMetrics
	require.NoError(t, f.reader.Collect(ctx, &rm))
	sums := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				k := m.Name
				for _, kv := range dp.Attributes.ToSlice() {
					k += " " + kv.Value.AsString()
				}
				sums[k] += dp.Value
			}
		}
	}
	require.Equal(t, map[string]int64{
		"sniffarr.arr.events radarr grab webhook": 2,
		"sniffarr.arr.outcomes radarr matched":    1,
		"sniffarr.arr.outcomes radarr duplicate":  1,
	}, sums)
}

func TestWebhookGrabUnknown(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	s := f.rec.Store
	code, answer := f.post(t, "?app=radarr", "secret", radarrGrab(hexOf(2)))
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, Queued, answer)
	d, err := f.rec.Tasks.Discovery(ctx, hash(2))
	require.NoError(t, err)
	require.Equal(t, store.Arr, d)

	_, _, err = s.Insert(ctx, store.Torrent{Hash: hash(2), Name: "Alien.1979.1080p.BluRay.x264-GRP", Size: 8 << 30, Discovery: d, SeenAt: time.Now(),
		Kind: "movie", KindLayer: store.LayerRules, TitleKey: "y:alien 1979"})
	require.NoError(t, err)
	r, err := s.Get(ctx, hash(2))
	require.NoError(t, err)
	require.Equal(t, store.LayerArr, r.Match.Layer, "the release is matched as it is stored")
	require.Equal(t, 348, r.Match.Work.TMDBID)

	_, err = s.Reject(ctx, hash(3), "script", "script", 1<<20, time.Now())
	require.NoError(t, err)
	_, answer = f.post(t, "", "secret", radarrGrab(hexOf(3)))
	require.Equal(t, Unknown, answer, "a rejected torrent is not queued")
	_, err = f.rec.Tasks.Discovery(ctx, hash(3))
	require.ErrorIs(t, err, store.ErrNotQueued)
}

func TestWebhookSonarrTVDB(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	grab := func(downloadID string) string {
		return `{"eventType":"Grab","instanceName":"Sonarr",
			"series":{"id":3,"title":"Breaking Bad","titleSlug":"breaking-bad","tvdbId":81189,"tvMazeId":169,"imdbId":"tt0903747","type":"standard","year":2008},
			"episodes":[{"id":11,"episodeNumber":1,"seasonNumber":2,"title":"Seven Thirty-Seven"},{"id":12,"episodeNumber":2,"seasonNumber":2,"title":"Grilled"}],
			"release":{"releaseTitle":"Breaking.Bad.S02.1080p.BluRay","indexer":"Tracker","size":42949672960},
			"downloadClient":"qBittorrent","downloadClientType":"qBittorrent","downloadId":"` + downloadID + `"}`
	}
	for _, b := range []byte{4, 5} {
		code, answer := f.post(t, "", "secret", grab(hexOf(b)))
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, Queued, answer)
	}
	require.Equal(t, int64(1), f.finder.calls.Load(), "the TVDB mapping is looked up once, then read from the store")
	id, err := f.rec.Store.WorkByTMDB(ctx, store.TypeTV, 1396)
	require.NoError(t, err)
	sums, err := f.rec.Store.Summaries(ctx, []int64{id})
	require.NoError(t, err)
	require.Equal(t, int64(2), sums[id].GrabsWeek)
	require.Contains(t, sums[id].Work.IDs, store.ExternalID{Provider: store.ProviderTVDB, ID: "81189"})

	_, answer := f.post(t, "", "secret", strings.ReplaceAll(grab(hexOf(6)), `"tvdbId":81189`, `"tvdbId":1`))
	require.Equal(t, Queued, answer, "a series TMDB does not know is still demand without a work")
}

func TestWebhookNonHexDownloadID(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	body := strings.ReplaceAll(radarrGrab("SABnzbd_nzo_kyt1f0"), `"downloadClientType":"qBittorrent"`, `"downloadClientType":"Sabnzbd"`)
	code, answer := f.post(t, "", "secret", body)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, Unknown, answer)
	imp := `{"eventType":"Download","movie":{"id":7,"title":"Alien","tmdbId":348},"movieFile":{"id":1,"relativePath":"Alien.mkv","sceneName":"Alien.1979.1080p.BluRay.x264-GRP","size":8589934592},
		"isUpgrade":false,"downloadClient":"SABnzbd","downloadId":"SABnzbd_nzo_kyt1f0"}`
	code, answer = f.post(t, "", "secret", imp)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, Unknown, answer, "an import is an event of its own")

	b := &search.Browser{Store: f.rec.Store}
	res, err := b.Title(ctx, connect.NewRequest(&sniffarrv1.TitleRequest{Id: "m348"}))
	require.NoError(t, err)
	require.Equal(t, int64(1), res.Msg.GetWork().GetGrabsWeek(), "a grab without an infohash is still demand; an import is no grab")
	require.Zero(t, res.Msg.GetWork().GetReleases())
}
