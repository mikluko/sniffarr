// Package torznab serves the search API as a Torznab indexer, for Prowlarr,
// Radarr and Sonarr: movies and series only, searchable by text and by the
// TMDB, IMDb and TVDB IDs of their works.
package torznab

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/names"
	"github.com/mikluko/sniffarr/internal/search"
	"github.com/mikluko/sniffarr/internal/store"
)

const (
	// DefaultLimit is how many items a search returns when asked for none
	// in particular.
	DefaultLimit = 100
	// MaxLimit is the most items a search returns, offset included.
	MaxLimit = search.MaxLimit
)

// Newznab error codes.
const (
	errCredentials = 100
	errMissing     = 200
	errParameter   = 201
	errFunction    = 202
	errUnknown     = 900
)

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
	WantProbe(ctx context.Context, hashes []store.Hash, at time.Time) error
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
		h.fail(w, errCredentials, "Incorrect user credentials")
		return
	}
	switch t := q.Get("t"); t {
	case "caps":
		h.write(w, capabilities())
	case "search", "movie", "tvsearch":
		feed, code, err := h.search(r.Context(), t, q)
		if err != nil {
			h.fail(w, code, err.Error())
			return
		}
		h.write(w, feed)
	case "":
		h.fail(w, errMissing, "Missing parameter (t)")
	default:
		h.fail(w, errFunction, "No such function ("+t+")")
	}
}

// search runs the search function t, returning a Newznab error code with
// the error.
func (h *Handler) search(ctx context.Context, t string, q map[string][]string) (*feed, int, error) {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	offset, err := number(get("offset"), 0)
	if err != nil {
		return nil, errParameter, fmt.Errorf("Incorrect parameter (offset)")
	}
	limit, err := number(get("limit"), DefaultLimit)
	if err != nil {
		return nil, errParameter, fmt.Errorf("Incorrect parameter (limit)")
	}
	kinds, err := kindsOf(t, get("cat"))
	if err != nil {
		return nil, errParameter, err
	}
	f := newFeed()
	if len(kinds) == 0 || offset >= MaxLimit || limit == 0 {
		return f, 0, nil
	}
	req := &sniffarrv1.SearchRequest{Query: get("q"), Kinds: kinds, Limit: int32(min(offset+limit, MaxLimit))}
	if req.Id, err = idOf(t, get); err != nil {
		return nil, errParameter, err
	}
	if t == "tvsearch" {
		var dated bool
		if req.Season, req.Episode, dated, err = episodeOf(get("season"), get("ep")); err != nil {
			return nil, errParameter, err
		}
		if dated {
			return f, 0, nil
		}
	}
	res, err := h.Search.Search(ctx, connect.NewRequest(req))
	if connect.CodeOf(err) == connect.CodeInvalidArgument {
		return nil, errParameter, err
	}
	if err != nil {
		h.Log.ErrorContext(ctx, "torznab search", "err", err)
		return nil, errUnknown, errors.New("Search failed")
	}
	releases := res.Msg.GetReleases()
	releases = releases[min(offset, len(releases)):]
	releases = releases[:min(limit, len(releases))]
	for _, r := range releases {
		f.Channel.Items = append(f.Channel.Items, itemOf(r))
	}
	if h.Probes != nil && (req.GetQuery() != "" || req.GetId() != nil) {
		h.want(ctx, releases)
	}
	return f, 0, nil
}

// want records the releases with Probes.
func (h *Handler) want(ctx context.Context, releases []*sniffarrv1.Release) {
	hashes := make([]store.Hash, 0, len(releases))
	for _, r := range releases {
		if len(r.GetInfohash()) == len(store.Hash{}) {
			hashes = append(hashes, store.Hash(r.GetInfohash()))
		}
	}
	if err := h.Probes.WantProbe(ctx, hashes, time.Now()); err != nil {
		h.Log.ErrorContext(ctx, "torznab want probes", "err", err)
	}
}

func number(s string, def int) (int, error) {
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, errors.New("not a non-negative integer")
	}
	return n, nil
}

// kindsOf returns the kinds the function t searches, narrowed by the
// comma-separated categories cat when it is set.
func kindsOf(t, cat string) ([]sniffarrv1.Kind, error) {
	movies, series := t != "tvsearch", t != "movie"
	if cat != "" {
		var m, s bool
		for c := range strings.SplitSeq(cat, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(c))
			if err != nil {
				return nil, errors.New("Incorrect parameter (cat)")
			}
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
	return out, nil
}

// idOf returns the first ID parameter the function t takes that is set.
func idOf(t string, get func(string) string) (*sniffarrv1.ExternalId, error) {
	var params []string
	switch t {
	case "movie":
		params = []string{"tmdbid", "imdbid"}
	case "tvsearch":
		params = []string{"tvdbid", "tmdbid", "imdbid"}
	}
	for _, p := range params {
		v := strings.TrimSpace(get(p))
		if v == "" {
			continue
		}
		provider := strings.TrimSuffix(p, "id")
		if p == "imdbid" {
			v = strings.TrimPrefix(v, "tt")
		}
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("Incorrect parameter (%s)", p)
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
func episodeOf(season, ep string) (s, e *int32, dated bool, err error) {
	season, ep = strings.TrimSpace(season), strings.TrimSpace(ep)
	if season != "" {
		n, err := strconv.ParseInt(season, 10, 32)
		if err != nil || n < 0 {
			return nil, nil, false, errors.New("Incorrect parameter (season)")
		}
		s = new(int32(n))
	}
	if ep == "" {
		return s, nil, false, nil
	}
	if s == nil {
		return nil, nil, false, errors.New("Incorrect parameter (ep)")
	}
	if month, day, ok := strings.Cut(ep, "/"); ok {
		m, merr := strconv.Atoi(month)
		d, derr := strconv.Atoi(day)
		if merr != nil || derr != nil || m < 1 || m > 12 || d < 1 || d > 31 {
			return nil, nil, false, errors.New("Incorrect parameter (ep)")
		}
		return s, nil, true, nil
	}
	n, err := strconv.ParseInt(ep, 10, 32)
	if err != nil || n <= 0 {
		return nil, nil, false, errors.New("Incorrect parameter (ep)")
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
func itemOf(r *sniffarrv1.Release) item {
	magnet := r.GetMagnet()
	cat := strconv.Itoa(category(r))
	peers := strconv.FormatInt(r.GetPeersWeek(), 10)
	it := item{
		Title:     title(r),
		GUID:      guid{Value: magnet},
		Link:      magnet,
		PubDate:   r.GetFirstSeen().AsTime().Format(time.RFC1123Z),
		Size:      r.GetSize(),
		Category:  cat,
		Enclosure: enclosure{URL: magnet, Length: r.GetSize(), Type: "application/x-bittorrent"},
		Attrs: []attr{
			{Name: "category", Value: cat},
			{Name: "size", Value: strconv.FormatInt(r.GetSize(), 10)},
			{Name: "infohash", Value: hex.EncodeToString(r.GetInfohash())},
			{Name: "magneturl", Value: magnet},
			{Name: "seeders", Value: peers},
			{Name: "peers", Value: peers},
		},
	}
	for _, id := range r.GetIds() {
		switch id.GetProvider() {
		case "tmdb", "tvdb":
			it.Attrs = append(it.Attrs, attr{Name: id.GetProvider() + "id", Value: id.GetId()})
		case "imdb":
			it.Attrs = append(it.Attrs, attr{Name: "imdbid", Value: strings.TrimPrefix(id.GetId(), "tt")})
		}
	}
	return it
}

func (h *Handler) write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write([]byte(xml.Header))
	if err := xml.NewEncoder(w).Encode(v); err != nil {
		h.Log.Error("torznab response", "err", err)
	}
}

func (h *Handler) fail(w http.ResponseWriter, code int, description string) {
	h.write(w, newznabError{Code: code, Description: description})
}
