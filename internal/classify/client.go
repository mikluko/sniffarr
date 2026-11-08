// Package classify asks a System One endpoint, TypeSafe's Jev or another
// model serving its API, what a torrent is, whether it carries an audio
// track in each audio language, and what kind of video copy it is, and
// records the answers.
package classify

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"sarak.sh/mikluko/libarr/names"
	"sarak.sh/mikluko/libarr/names/jev"
	libtmdb "sarak.sh/mikluko/libarr/tmdb"
	"sarak.sh/mikluko/libarr/typesafe"
	"sarak.sh/mikluko/sniffarr/internal/audio"
	"sarak.sh/mikluko/sniffarr/internal/rot13"
	"sarak.sh/mikluko/sniffarr/internal/rules"
)

// DefaultURL is TypeSafe's evaluation endpoint.
const DefaultURL = typesafe.DefaultURL

// Client asks a System One endpoint, TypeSafe's by default.
type Client struct {
	*typesafe.Client
	// Languages are asked about, one audio question each.
	Languages []audio.Language
}

// New returns a Client for the API key with the production endpoint and the
// jev-latest model; an empty key sends no Authorization header.
func New(key string) *Client {
	c := typesafe.NewClient(key, typesafe.DefaultRate)
	c.HTTP = &http.Client{Timeout: 60 * time.Second}
	if key == "" {
		c.HTTP.Transport = keyless{http.DefaultTransport}
	}
	c.Backoff = 2 * time.Second
	c.Service = "sniffarr"
	return &Client{Client: c}
}

// keyless drops the empty bearer token typesafe.Client sends for an empty
// key.
type keyless struct{ base http.RoundTripper }

func (t keyless) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("Authorization") == "Bearer " {
		r = r.Clone(r.Context())
		r.Header.Del("Authorization")
	}
	return t.base.RoundTrip(r)
}

// Input is what the classifier sees of a torrent.
type Input struct {
	Name  string
	Size  int64
	Files []rules.File
	// AskKind reads the name with jev.Reader for the kind too; otherwise
	// Result.Kind is empty and Result.Reading nil.
	AskKind bool
}

// Result is the classifier's answer.
type Result struct {
	Kind             string
	KindConfidence   float64
	Source           string
	SourceConfidence float64
	// Audio maps the tag of each of Client.Languages to the probability of
	// an audio track in it.
	Audio map[string]float64
	// Reading is Jev's reading of the name when the kind was asked.
	Reading *jev.Reading
	Usage   typesafe.Usage
}

// categories are the kinds besides movie and series, each with its
// description.
var categories = map[string]string{
	"music":    "Music albums, tracks or discographies",
	"book":     "E-books or audiobooks",
	"software": "Programs, operating systems, games",
	"adult":    rot13.String("Cbeabtencul"),
	"other":    "Anything else",
}

// audioQuestion is the key of the question about an audio track in l.
func audioQuestion(l audio.Language) string {
	return "audio_" + l.Tag
}

// questions are the source and audio questions, and with askKind the
// category question for a torrent that is neither a film nor a series.
func questions(askKind bool, langs []audio.Language) map[string]typesafe.Question {
	q := map[string]typesafe.Question{
		"source": typesafe.Choice("If this is a video release, what kind of copy is it?", map[string]string{
			"disc_image": "A Blu-ray or DVD disc image: BDMV, VIDEO_TS, ISO, m2ts streams",
			"remux":      "A remux: the disc's streams in a container without re-encoding",
			"encode":     "A re-encoded copy: BDRip, BRRip, x264/x265 encodes",
			"web":        "A WEB-DL or WEBRip copy",
			"other":      "Another video source, or not a video release",
		}),
	}
	if askKind {
		q["category"] = typesafe.Choice("If this torrent is not a film or a TV series, what is it, judging by its `name` and `files`?", categories)
	}
	for _, l := range langs {
		q[audioQuestion(l)] = typesafe.Question{
			Type:         typesafe.TypeNoul,
			Instructions: "Does this release carry a " + l.Name + "-language audio track (dubbing, voice-over, or " + l.Name + " original), judging by its `name` and `files`?",
			Criteria: map[string]any{
				"true": map[string]any{
					"signs":   l.Signs,
					"studios": "A " + l.Name + " dubbing or voice-over studio or release group: " + strings.Join(l.Studios, ", "),
				},
				"false": "No sign of " + l.Name + " audio, or not a video release",
			},
		}
	}
	return q
}

var errIncomplete = errors.New("classifier: incomplete answer")

// Classify asks the classifier about the torrent in one request, or with
// Input.AskKind in the requests of jev.Reader.Read, its own questions riding
// the reading's. Errors from the endpoint are *typesafe.Error.
func (c *Client) Classify(ctx context.Context, in Input) (Result, error) {
	files := make([]string, len(in.Files))
	for i, f := range in.Files {
		files[i] = fmt.Sprintf("%s (%d MiB)", f.Path, f.Size>>20)
	}
	state := map[string]any{
		"size_gib": fmt.Sprintf("%.2f", float64(in.Size)/(1<<30)),
		"files":    files,
	}
	qs := questions(in.AskKind, c.Languages)
	var out Result
	var answers map[string]typesafe.Answer
	if in.AskKind {
		reading, err := (&jev.Reader{Client: c.Client}).Read(ctx, in.Name, jev.Extra{State: state, Questions: qs})
		if err != nil {
			return Result{}, err
		}
		answers, out.Reading, out.Usage = reading.Extra, &reading, reading.Usage
		if out.Kind, out.KindConfidence = kind(reading); out.Kind == "" {
			return Result{}, errIncomplete
		}
	} else {
		state["name"] = in.Name
		res, err := c.Ask(ctx, state, qs)
		if err != nil {
			return Result{}, err
		}
		answers, out.Usage = res.Answers, res.Usage
	}
	source := answers["source"]
	if source.Choice == "" {
		return Result{}, errIncomplete
	}
	out.Source, out.SourceConfidence = source.Choice, source.Confidence
	for _, l := range c.Languages {
		a := answers[audioQuestion(l)]
		if a.Type != typesafe.TypeNoul {
			return Result{}, errIncomplete
		}
		if out.Audio == nil {
			out.Audio = map[string]float64{}
		}
		out.Audio[l.Tag] = a.Noul
	}
	return out, nil
}

// kind is the sniffarr kind of the reading and the confidence in it: the
// reading's movie or series, or else the category answer at the product of
// both confidences; "" when the category was not answered.
func kind(r jev.Reading) (string, float64) {
	switch r.Kind {
	case names.Movie:
		return "movie", r.KindConfidence
	case names.Series:
		return "series", r.KindConfidence
	}
	a := r.Extra["category"]
	if _, ok := categories[a.Choice]; !ok {
		return "", 0
	}
	return a.Choice, r.KindConfidence * a.Confidence
}

// ChoiceInput is a single choice question about a state.
type ChoiceInput struct {
	State        map[string]any
	Instructions string
	// Options maps each option to its description.
	Options map[string]string
}

// ChoiceResult is the classifier's answer to a choice question.
type ChoiceResult struct {
	Choice     string
	Confidence float64
	Usage      typesafe.Usage
}

// Choose asks the classifier one choice question with typesafe.Chooser; an
// answer outside the options is an error.
func (c *Client) Choose(ctx context.Context, in ChoiceInput) (ChoiceResult, error) {
	var usage typesafe.Usage
	ch := &typesafe.Chooser{Client: c.Client, OnUsage: func(u typesafe.Usage) { usage = u }}
	r, err := ch.Choose(ctx, libtmdb.ChoiceInput{State: in.State, Instructions: in.Instructions, Options: in.Options})
	if err != nil {
		return ChoiceResult{}, err
	}
	if _, ok := in.Options[r.Choice]; !ok {
		return ChoiceResult{}, errIncomplete
	}
	return ChoiceResult{Choice: r.Choice, Confidence: r.Confidence, Usage: usage}, nil
}
