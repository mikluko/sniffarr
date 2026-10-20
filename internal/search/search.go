// Package search implements sniffarr.v1.SearchService over the store.
package search

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/store"
)

const (
	// DefaultLimit is the number of releases Search returns when asked for
	// none in particular.
	DefaultLimit = 50
	// MaxLimit is the most releases Search returns.
	MaxLimit = 500
)

// hidden are the kinds Search never returns.
var hidden = []string{"adult", "software"}

// Service implements sniffarrv1connect.SearchServiceHandler.
type Service struct {
	Store *store.Store
	Log   *slog.Logger
	// Lazy, when set, has Search classify candidates first; see Lazy.
	Lazy *Lazy
}

var kinds = map[sniffarrv1.Kind]string{
	sniffarrv1.Kind_KIND_UNSPECIFIED: "",
	sniffarrv1.Kind_KIND_MOVIE:       "movie",
	sniffarrv1.Kind_KIND_SERIES:      "series",
	sniffarrv1.Kind_KIND_MUSIC:       "music",
	sniffarrv1.Kind_KIND_BOOK:        "book",
	sniffarrv1.Kind_KIND_SOFTWARE:    "software",
	sniffarrv1.Kind_KIND_ADULT:       "adult",
	sniffarrv1.Kind_KIND_OTHER:       "other",
}

var layers = map[string]sniffarrv1.Layer{
	store.LayerRules: sniffarrv1.Layer_LAYER_RULES,
	store.LayerReuse: sniffarrv1.Layer_LAYER_REUSE,
	store.LayerJev:   sniffarrv1.Layer_LAYER_CLASSIFIER,
}

var sources = map[string]sniffarrv1.VideoSource{
	"disc_image": sniffarrv1.VideoSource_VIDEO_SOURCE_DISC_IMAGE,
	"remux":      sniffarrv1.VideoSource_VIDEO_SOURCE_REMUX,
	"encode":     sniffarrv1.VideoSource_VIDEO_SOURCE_ENCODE,
	"web":        sniffarrv1.VideoSource_VIDEO_SOURCE_WEB,
	"other":      sniffarrv1.VideoSource_VIDEO_SOURCE_OTHER,
}

func (s *Service) Search(ctx context.Context, req *connect.Request[sniffarrv1.SearchRequest]) (*connect.Response[sniffarrv1.SearchResponse], error) {
	m := req.Msg
	q := store.Query{Text: m.GetQuery(), Exclude: hidden, Limit: int(m.GetLimit())}
	switch {
	case q.Limit < 0:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("negative limit"))
	case q.Limit == 0:
		q.Limit = DefaultLimit
	case q.Limit > MaxLimit:
		q.Limit = MaxLimit
	}
	for _, k := range m.GetKinds() {
		name, ok := kinds[k]
		if !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown kind %d", k))
		}
		q.Kinds = append(q.Kinds, name)
	}
	if m.MinAudioProbability != nil {
		if m.GetAudioLanguage() == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("min_audio_probability without audio_language"))
		}
		q.AudioLanguage, q.MinAudioProbability = m.GetAudioLanguage(), new(float64(m.GetMinAudioProbability()))
	}
	var found []store.Release
	var err error
	if s.Lazy != nil {
		found, err = s.Lazy.search(ctx, s.Store, q)
	} else {
		found, err = s.Store.Search(ctx, q)
	}
	if err != nil {
		s.Log.ErrorContext(ctx, "search", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("search failed"))
	}
	res := &sniffarrv1.SearchResponse{Releases: make([]*sniffarrv1.Release, len(found))}
	for i, r := range found {
		res.Releases[i] = release(r)
	}
	return connect.NewResponse(res), nil
}

func (s *Service) Get(ctx context.Context, req *connect.Request[sniffarrv1.GetRequest]) (*connect.Response[sniffarrv1.GetResponse], error) {
	b := req.Msg.GetInfohash()
	if len(b) != len(store.Hash{}) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("infohash of %d bytes, want 20", len(b)))
	}
	r, err := s.Store.Get(ctx, store.Hash(b))
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if err != nil {
		s.Log.Error("get", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("get failed"))
	}
	return connect.NewResponse(&sniffarrv1.GetResponse{Release: release(r)}), nil
}

func release(r store.Release) *sniffarrv1.Release {
	out := &sniffarrv1.Release{
		Infohash:         r.Hash[:],
		Name:             r.Name,
		Size:             r.Size,
		FileCount:        r.FileCount,
		Source:           sources[r.Source],
		KindConfidence:   float32(r.KindConfidence),
		SourceConfidence: float32(r.SourceConfidence),
		FirstSeen:        timestamppb.New(r.FirstSeen),
		LastSeen:         timestamppb.New(r.LastSeen),
		Magnet:           Magnet(r.Hash, r.Name),
		Layer:            layers[r.KindLayer],
		Classified:       r.Classified,
	}
	for k, name := range kinds {
		if name == r.Kind {
			out.Kind = k
		}
	}
	for _, a := range r.Audio {
		pa := &sniffarrv1.Audio{Language: a.Language, Marker: a.Marker, ScriptHint: a.ScriptHint}
		if a.Probability != nil {
			pa.Probability = new(float32(*a.Probability))
		}
		out.Audio = append(out.Audio, pa)
	}
	return out
}

// Magnet returns the magnet URI of the torrent with the infohash and display
// name.
func Magnet(h store.Hash, name string) string {
	return "magnet:?xt=urn:btih:" + hex.EncodeToString(h[:]) + "&dn=" + strings.ReplaceAll(url.QueryEscape(name), "+", "%20")
}
