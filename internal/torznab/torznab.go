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
	"github.com/mikluko/sniffarr/internal/search"
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

// Handler serves the Torznab API at any path. Every request must carry
// Token as its apikey parameter.
type Handler struct {
	Search Searcher
	Token  string
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
	for _, r := range releases[:min(limit, len(releases))] {
		f.Channel.Items = append(f.Channel.Items, itemOf(r))
	}
	return f, 0, nil
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

var (
	uhd = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(?:2160p|4k|uhd)(?:$|[^\p{L}\p{N}])`)
	hd  = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(?:1080[pi]|720p)(?:$|[^\p{L}\p{N}])`)
)

// category is the subcategory of a release: its kind's, by the resolution
// its name gives.
func category(r *sniffarrv1.Release) int {
	sd, hdCat, uhdCat := catMoviesSD, catMoviesHD, catMoviesUHD
	if r.GetKind() == sniffarrv1.Kind_KIND_SERIES {
		sd, hdCat, uhdCat = catTVSD, catTVHD, catTVUHD
	}
	switch {
	case uhd.MatchString(r.GetName()):
		return uhdCat
	case hd.MatchString(r.GetName()):
		return hdCat
	}
	return sd
}

func itemOf(r *sniffarrv1.Release) item {
	magnet := r.GetMagnet()
	cat := strconv.Itoa(category(r))
	it := item{
		Title:     r.GetName(),
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
