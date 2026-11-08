// Package torznab serves the search API as a Torznab indexer, for Prowlarr,
// Radarr and Sonarr: movies and series only, searchable by text and by the
// TMDB, IMDb and TVDB IDs of their works.
package torznab

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"sarak.sh/mikluko/libarr/torznab"
	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/names"
	"sarak.sh/mikluko/sniffarr/internal/search"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

const (
	// DefaultLimit is how many items a search returns when asked for none
	// in particular.
	DefaultLimit = 100
	// MaxLimit is the most items a search returns, offset included.
	MaxLimit = search.MaxLimit
)

// limits are the items a search returns by default and at most.
var limits = torznab.Limits{Default: DefaultLimit, Max: MaxLimit}

// Categories of the items: movies and TV, each by resolution.
const (
	catMovies    = 2000
	catMoviesSD  = 2030
	catMoviesHD  = 2040
	catMoviesUHD = 2045
	catTV        = 5000
	catTVSD      = 5030
	catTVHD      = 5040
	catTVUHD     = 5045
)

// Searcher is the search API; search.Service is one.
type Searcher interface {
	Search(ctx context.Context, req *connect.Request[sniffarrv1.SearchRequest]) (*connect.Response[sniffarrv1.SearchResponse], error)
}

// ProbeWanter takes the releases a search with a query or an ID returned;
// *store.Store is one.
type ProbeWanter interface {
	WantProbe(ctx context.Context, hashes []store.Hash, w store.ProbeWant, at time.Time) error
}

// Handler serves the Torznab API at any path. Every request must carry
// Token as its apikey parameter.
type Handler struct {
	Search Searcher
	Token  string
	// Probes, unless nil, takes the releases of every search with a query
	// or an ID; a failure to record them is logged and fails no search.
	Probes ProbeWanter
	Log    *slog.Logger
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if subtle.ConstantTimeCompare([]byte(q.Get("apikey")), []byte(h.Token)) != 1 {
		h.fail(w, &torznab.Error{Code: torznab.CodeIncorrectCredentials, Description: "Incorrect user credentials"})
		return
	}
	switch t := q.Get("t"); t {
	case "caps":
		h.check(torznab.WriteCaps(w, capabilities()))
	case "search", "movie", "tvsearch":
		req, err := torznab.ParseRequest(q, limits)
		if err != nil {
			h.fail(w, asError(err))
			return
		}
		feed, terr := h.search(r.Context(), req)
		if terr != nil {
			h.fail(w, terr)
			return
		}
		h.check(torznab.WriteFeed(w, feed))
	case "":
		h.fail(w, torznab.MissingParameter("t"))
	default:
		h.fail(w, torznab.NoSuchFunction(t))
	}
}

// capabilities is the answer to t=caps.
func capabilities() *torznab.Caps {
	return &torznab.Caps{
		Server: torznab.Server{Title: "sniffarr"},
		Limits: limits,
		Searching: torznab.Searching{Modes: []torznab.Mode{
			{Function: "search", Available: true, SupportedParams: "q"},
			{Function: "tv-search", Available: true, SupportedParams: "q,tvdbid,tmdbid,season,ep"},
			{Function: "movie-search", Available: true, SupportedParams: "q,tmdbid,imdbid"},
			{Function: "music-search"},
			{Function: "audio-search"},
			{Function: "book-search"},
		}},
		Categories: []torznab.Category{
			{ID: catMovies, Name: "Movies", Subcats: []torznab.Subcat{{ID: catMoviesSD, Name: "Movies/SD"}, {ID: catMoviesHD, Name: "Movies/HD"}, {ID: catMoviesUHD, Name: "Movies/UHD"}}},
			{ID: catTV, Name: "TV", Subcats: []torznab.Subcat{{ID: catTVSD, Name: "TV/SD"}, {ID: catTVHD, Name: "TV/HD"}, {ID: catTVUHD, Name: "TV/UHD"}}},
		},
	}
}

// newFeed is a feed with no items.
func newFeed() *torznab.RSS {
	return &torznab.RSS{Channel: torznab.Channel{Title: "sniffarr", Description: "BitTorrent DHT releases"}}
}

// asError is err as a Newznab error, CodeUnknown when it is not one.
func asError(err error) *torznab.Error {
	var e *torznab.Error
	if errors.As(err, &e) {
		return e
	}
	return &torznab.Error{Code: torznab.CodeUnknown, Description: err.Error()}
}

// search runs the search req.
func (h *Handler) search(ctx context.Context, req torznab.Request) (*torznab.RSS, *torznab.Error) {
	f := newFeed()
	kinds := kindsOf(req.T, req.Cat)
	if len(kinds) == 0 || req.Limit == 0 {
		return f, nil
	}
	sreq := &sniffarrv1.SearchRequest{Query: req.Q, Kinds: kinds, Limit: int32(req.Offset + req.Limit)}
	var terr *torznab.Error
	if sreq.Id, terr = idOf(req); terr != nil {
		return nil, terr
	}
	if req.T == "tvsearch" {
		var dated bool
		if sreq.Season, sreq.Episode, dated, terr = episodeOf(req.Season, req.Ep); terr != nil {
			return nil, terr
		}
		if dated {
			return f, nil
		}
	}
	res, err := h.Search.Search(ctx, connect.NewRequest(sreq))
	if connect.CodeOf(err) == connect.CodeInvalidArgument {
		return nil, &torznab.Error{Code: torznab.CodeIncorrectParameter, Description: err.Error()}
	}
	if err != nil {
		h.Log.ErrorContext(ctx, "torznab search", "err", err)
		return nil, &torznab.Error{Code: torznab.CodeUnknown, Description: "Search failed"}
	}
	releases := res.Msg.GetReleases()
	releases = releases[min(req.Offset, len(releases)):]
	releases = releases[:min(req.Limit, len(releases))]
	for _, r := range releases {
		f.Channel.Items = append(f.Channel.Items, itemOf(r))
	}
	if h.Probes != nil && (sreq.GetQuery() != "" || sreq.GetId() != nil) {
		h.want(ctx, req.T, sreq, releases)
	}
	return f, nil
}

// want records the releases with Probes as returned by the search req of
// the function t.
func (h *Handler) want(ctx context.Context, t string, req *sniffarrv1.SearchRequest, releases []*sniffarrv1.Release) {
	hashes := make([]store.Hash, 0, len(releases))
	for _, r := range releases {
		if len(r.GetInfohash()) == len(store.Hash{}) {
			hashes = append(hashes, store.Hash(r.GetInfohash()))
		}
	}
	w := store.ProbeWant{T: t, Query: req.GetQuery(), Season: int(req.GetSeason()), Episode: int(req.GetEpisode())}
	if id := req.GetId(); id != nil {
		w.ID = id.GetProvider() + "id:" + id.GetId()
	}
	if err := h.Probes.WantProbe(ctx, hashes, w, time.Now()); err != nil {
		h.Log.ErrorContext(ctx, "torznab want probes", "err", err)
	}
}

// kindsOf returns the kinds the function t searches, narrowed by the
// categories cats when there are any.
func kindsOf(t string, cats []int) []sniffarrv1.Kind {
	movies, series := t != "tvsearch", t != "movie"
	if len(cats) > 0 {
		var m, s bool
		for _, n := range cats {
			m = m || n/1000 == catMovies/1000
			s = s || n/1000 == catTV/1000
		}
		movies, series = movies && m, series && s
	}
	var out []sniffarrv1.Kind
	if movies {
		out = append(out, sniffarrv1.Kind_KIND_MOVIE)
	}
	if series {
		out = append(out, sniffarrv1.Kind_KIND_SERIES)
	}
	return out
}

// idOf returns the first ID parameter the function of req takes that is
// set.
func idOf(req torznab.Request) (*sniffarrv1.ExternalId, *torznab.Error) {
	var params []string
	switch req.T {
	case "movie":
		params = []string{"tmdbid", "imdbid"}
	case "tvsearch":
		params = []string{"tvdbid", "tmdbid", "imdbid"}
	}
	ids := map[string]string{"tmdbid": req.TMDbID, "imdbid": req.IMDbID, "tvdbid": req.TVDbID}
	for _, p := range params {
		v := strings.TrimSpace(ids[p])
		if v == "" {
			continue
		}
		provider := strings.TrimSuffix(p, "id")
		if p == "imdbid" {
			v = strings.TrimPrefix(v, "tt")
		}
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return nil, torznab.IncorrectParameter(p)
		}
		if p == "imdbid" {
			return &sniffarrv1.ExternalId{Provider: provider, Id: fmt.Sprintf("tt%07d", n)}, nil
		}
		return &sniffarrv1.ExternalId{Provider: provider, Id: strconv.Itoa(n)}, nil
	}
	return nil, nil
}

// episodeOf reads the season and ep parameters of a TV search. An ep in
// Sonarr's month/day form, for a daily series, is dated: names give no air
// dates, so it matches nothing.
func episodeOf(season, ep string) (s, e *int32, dated bool, err *torznab.Error) {
	season, ep = strings.TrimSpace(season), strings.TrimSpace(ep)
	if season != "" {
		n, perr := strconv.ParseInt(season, 10, 32)
		if perr != nil || n < 0 {
			return nil, nil, false, torznab.IncorrectParameter("season")
		}
		s = new(int32(n))
	}
	if ep == "" {
		return s, nil, false, nil
	}
	if s == nil {
		return nil, nil, false, torznab.IncorrectParameter("ep")
	}
	if month, day, ok := strings.Cut(ep, "/"); ok {
		m, merr := strconv.Atoi(month)
		d, derr := strconv.Atoi(day)
		if merr != nil || derr != nil || m < 1 || m > 12 || d < 1 || d > 31 {
			return nil, nil, false, torznab.IncorrectParameter("ep")
		}
		return s, nil, true, nil
	}
	n, perr := strconv.ParseInt(ep, 10, 32)
	if perr != nil || n <= 0 {
		return nil, nil, false, torznab.IncorrectParameter("ep")
	}
	return s, new(int32(n)), false, nil
}

var (
	uhd = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(?:2160p|4k|uhd)(?:$|[^\p{L}\p{N}])`)
	hd  = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(?:1080[pi]|720p)(?:$|[^\p{L}\p{N}])`)
)

// category is the subcategory of a release: its kind's, by the resolution
// its name gives in digits.
func category(r *sniffarrv1.Release) int {
	sd, hdCat, uhdCat := catMoviesSD, catMoviesHD, catMoviesUHD
	if r.GetKind() == sniffarrv1.Kind_KIND_SERIES {
		sd, hdCat, uhdCat = catTVSD, catTVHD, catTVUHD
	}
	name := names.InDigits(r.GetName())
	switch {
	case uhd.MatchString(name):
		return uhdCat
	case hd.MatchString(name):
		return hdCat
	}
	return sd
}

// russian is how sure a classifier must be of a Russian audio track for the
// title to name it.
const russian = 0.9

// title is the release's name with its year and resolution in digits,
// followed by markers in brackets for the release-title custom formats of
// Radarr and Sonarr. With a Russian audio track probed, they are the labels
// of the Russian tracks' names and RUS, as in " [Dub, MVO, RUS]"; else,
// when saysRussian, the labels of the name's translation codes and RUS, as
// in " [MVO, RUS]" for P; else, with probed audio tracks all of other
// languages, NORUS and the tracks' languages, as in " [NORUS ENG, UKR]".
// RUS carries the Russian audio's studios; with no label and no studio the
// block is " [RUS, -]".
func title(r *sniffarrv1.Release) string {
	name := names.InDigits(r.GetName())
	var ru *sniffarrv1.Audio
	for _, a := range r.GetAudio() {
		if a.GetLanguage() == "ru" {
			ru = a
		}
	}
	rus := strings.Join(append([]string{"RUS"}, ru.GetStudios()...), " ")
	tracks := audioTracks(r.GetTracks())
	if labels, ok := russianLabels(tracks); ok {
		return name + bracket(labels, rus)
	}
	if saysRussian(ru) {
		return name + bracket(codeLabels(name), rus)
	}
	if langs := otherLanguages(tracks); langs != "" {
		return name + " [" + langs + "]"
	}
	return name
}

// saysRussian reports whether the Russian audio has a probability of at
// least russian, or, with none known, a marker.
func saysRussian(ru *sniffarrv1.Audio) bool {
	if ru == nil {
		return false
	}
	if ru.Probability == nil {
		return ru.GetMarker()
	}
	return ru.GetProbability() >= russian
}

// bracket is " [", the labels of voices, rus, and "]", whatever labels the
// name has: the custom formats rank a release by the best kind they read,
// so a label twice costs nothing. A block of rus alone, when rus is a single
// word, gets ", -" after it, since Radarr reads a bare trailing word in
// brackets as the release group.
func bracket(voices []voice, rus string) string {
	labels := make([]string, 0, len(voices)+1)
	for _, v := range voices {
		labels = append(labels, v.label)
	}
	if len(labels) == 0 && !strings.Contains(rus, " ") {
		return " [" + rus + ", -]"
	}
	return " [" + strings.Join(append(labels, rus), ", ") + "]"
}

// itemOf is the item of a release. Its seeders and peers are both the
// release's distinct peers over the last 7 days, peers_week: the DHT does
// not tell seeders apart, and a count over a shorter window would take a
// sketch union per item.
func itemOf(r *sniffarrv1.Release) torznab.Item {
	magnet := r.GetMagnet()
	cat := category(r)
	peers := strconv.FormatInt(r.GetPeersWeek(), 10)
	it := torznab.Item{
		Title:      title(r),
		GUID:       torznab.GUID{Value: magnet},
		Link:       magnet,
		PubDate:    r.GetFirstSeen().AsTime(),
		Size:       r.GetSize(),
		Categories: []int{cat},
		Enclosure:  torznab.Enclosure{URL: magnet, Length: r.GetSize(), Type: torznab.EnclosureType},
		Attrs: []torznab.Attr{
			{Name: torznab.AttrCategory, Value: strconv.Itoa(cat)},
			{Name: torznab.AttrSize, Value: strconv.FormatInt(r.GetSize(), 10)},
			{Name: torznab.AttrInfohash, Value: hex.EncodeToString(r.GetInfohash())},
			{Name: torznab.AttrMagnetURL, Value: magnet},
			{Name: torznab.AttrSeeders, Value: peers},
			{Name: torznab.AttrPeers, Value: peers},
		},
	}
	for _, id := range r.GetIds() {
		switch id.GetProvider() {
		case "tmdb":
			it.Attrs = append(it.Attrs, torznab.Attr{Name: torznab.AttrTMDbID, Value: id.GetId()})
		case "tvdb":
			it.Attrs = append(it.Attrs, torznab.Attr{Name: torznab.AttrTVDbID, Value: id.GetId()})
		case "imdb":
			it.Attrs = append(it.Attrs, torznab.Attr{Name: torznab.AttrIMDbID, Value: strings.TrimPrefix(id.GetId(), "tt")})
		}
	}
	return it
}

// check logs the error of writing a response.
func (h *Handler) check(err error) {
	if err != nil {
		h.Log.Error("torznab response", "err", err)
	}
}

func (h *Handler) fail(w http.ResponseWriter, e *torznab.Error) {
	h.check(torznab.WriteError(w, e))
}
