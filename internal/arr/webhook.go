package arr

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"sarak.sh/mikluko/sniffarr/internal/store"
)

// maxPayload is the largest webhook body read, in bytes.
const maxPayload = 1 << 20

// Webhook serves the webhook connection of Radarr and Sonarr. A request
// must carry Token as the password of its HTTP Basic credentials, with any
// username. The app is the query's app, radarr or sonarr, or else the one
// the payload's shape tells: a movie is Radarr's, a series Sonarr's. Grab
// and Download events are recorded as grabs and imports; Test and every
// other event answer 200 and record nothing.
type Webhook struct {
	Recorder *Recorder
	Token    string
	Log      *slog.Logger
}

// payload is the part of a webhook body Webhook reads, Radarr's and
// Sonarr's together.
type payload struct {
	EventType    string    `json:"eventType"`
	InstanceName string    `json:"instanceName"`
	Movie        *movie    `json:"movie"`
	RemoteMovie  *movie    `json:"remoteMovie"`
	Series       *series   `json:"series"`
	Episodes     []episode `json:"episodes"`
	Release      *struct {
		ReleaseTitle string `json:"releaseTitle"`
		Indexer      string `json:"indexer"`
		Size         int64  `json:"size"`
	} `json:"release"`
	MovieFile   *file  `json:"movieFile"`
	EpisodeFile *file  `json:"episodeFile"`
	DownloadID  string `json:"downloadId"`
}

type movie struct {
	TMDBID int    `json:"tmdbId"`
	IMDbID string `json:"imdbId"`
}

type series struct {
	TVDBID int    `json:"tvdbId"`
	TMDBID int    `json:"tmdbId"`
	IMDbID string `json:"imdbId"`
}

type episode struct {
	SeasonNumber  int `json:"seasonNumber"`
	EpisodeNumber int `json:"episodeNumber"`
}

type file struct {
	SceneName string `json:"sceneName"`
	Size      int64  `json:"size"`
}

// events are the webhook's event types that are recorded, by the event
// they are recorded as.
var events = map[string]string{"Grab": store.EventGrab, "Download": store.EventImport}

func (h *Webhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, password, ok := r.BasicAuth(); !ok || subtle.ConstantTimeCompare([]byte(password), []byte(h.Token)) != 1 {
		w.Header().Set("WWW-Authenticate", `Basic realm="sniffarr"`)
		http.Error(w, "missing or wrong credentials", http.StatusUnauthorized)
		return
	}
	app := r.URL.Query().Get("app")
	if app != "" && app != store.AppRadarr && app != store.AppSonarr {
		http.Error(w, "app: want radarr or sonarr", http.StatusBadRequest)
		return
	}
	var p payload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPayload)).Decode(&p); err != nil {
		http.Error(w, "payload: "+err.Error(), http.StatusBadRequest)
		return
	}
	kind, ok := events[p.EventType]
	if !ok {
		_, _ = w.Write([]byte("ignored\n"))
		return
	}
	if app == "" {
		app = p.app()
	}
	if app == "" {
		http.Error(w, "payload has neither a movie nor a series: name the app with ?app=radarr or ?app=sonarr", http.StatusBadRequest)
		return
	}
	outcome, err := h.Recorder.Record(r.Context(), SourceWebhook, p.event(app, kind, h.Recorder.now()))
	if err != nil {
		h.Log.ErrorContext(r.Context(), "record arr event", "app", app, "event", kind, "err", err)
		http.Error(w, "record failed", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write([]byte(outcome + "\n"))
}

// app is the app the payload's shape or instance name tells, or "".
func (p *payload) app() string {
	name := strings.ToLower(p.InstanceName)
	switch {
	case p.Movie != nil || p.RemoteMovie != nil:
		return store.AppRadarr
	case p.Series != nil:
		return store.AppSonarr
	case strings.Contains(name, store.AppRadarr):
		return store.AppRadarr
	case strings.Contains(name, store.AppSonarr):
		return store.AppSonarr
	}
	return ""
}

// event is the payload as an event of the app and kind at the time.
func (p *payload) event(app, kind string, at time.Time) Event {
	e := Event{App: app, Kind: kind, DownloadID: p.DownloadID, At: at}
	if p.Release != nil {
		e.Title, e.Indexer, e.Size = p.Release.ReleaseTitle, p.Release.Indexer, p.Release.Size
	}
	for _, f := range []*file{p.MovieFile, p.EpisodeFile} {
		if f != nil && e.Title == "" {
			e.Title = f.SceneName
		}
		if f != nil && e.Size == 0 {
			e.Size = f.Size
		}
	}
	switch app {
	case store.AppRadarr:
		for _, m := range []*movie{p.Movie, p.RemoteMovie} {
			if m != nil && e.TMDBID == 0 {
				e.TMDBID, e.IMDbID = m.TMDBID, m.IMDbID
			}
		}
	case store.AppSonarr:
		if p.Series != nil {
			e.TMDBID, e.TVDBID, e.IMDbID = p.Series.TMDBID, p.Series.TVDBID, p.Series.IMDbID
		}
		e.Season, e.Episode = episodes(p.Episodes)
	}
	return e
}

// episodes returns the season all the episodes are of and the episode when
// there is one, 0 for each otherwise.
func episodes(eps []episode) (season, number int) {
	for i, ep := range eps {
		if i > 0 && ep.SeasonNumber != season {
			return 0, 0
		}
		season = ep.SeasonNumber
	}
	if len(eps) == 1 {
		number = eps[0].EpisodeNumber
	}
	return season, number
}
