// Package classify asks TypeSafe's Jev what a torrent is, whether it carries
// Russian audio, and what kind of video copy it is, and records the answers.
package classify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mikluko/sniffarr/internal/rot13"
	"github.com/mikluko/sniffarr/internal/rules"
)

// DefaultURL is TypeSafe's evaluation endpoint.
const DefaultURL = "https://api.typesafe.ai/v1/systemone"

// Client calls the TypeSafe evaluation endpoint.
type Client struct {
	Key   string
	URL   string
	Model string
	HTTP  *http.Client
	// Retries is how many times a 429, 529 or other 5xx answer is retried.
	Retries int
	// Backoff is the wait before the first retry; it doubles each time.
	Backoff time.Duration
}

// New returns a Client for the API key with the production endpoint and the
// jev-latest model.
func New(key string) *Client {
	return &Client{
		Key:     key,
		URL:     DefaultURL,
		Model:   "jev-latest",
		HTTP:    &http.Client{Timeout: 60 * time.Second},
		Retries: 4,
		Backoff: 2 * time.Second,
	}
}

// Input is what the classifier sees of a torrent.
type Input struct {
	Name  string
	Size  int64
	Files []rules.File
	// AskKind asks for the kind too; otherwise Result.Kind is empty.
	AskKind bool
}

// Result is the classifier's answer.
type Result struct {
	Kind             string
	KindConfidence   float64
	Source           string
	SourceConfidence float64
	RussianAudio     float64
	Tokens           int64
}

// APIError is a non-200 answer from the endpoint.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("typesafe: %d %s: %s", e.Status, http.StatusText(e.Status), e.Body)
}

// Permanent reports whether repeating the same request cannot succeed.
func (e *APIError) Permanent() bool {
	return e.Status >= 400 && e.Status < 500 && e.Status != http.StatusTooManyRequests && e.Status != http.StatusUnauthorized
}

func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

func questions(askKind bool) map[string]any {
	q := map[string]any{
		"kind": map[string]any{
			"type":         "choice",
			"instructions": "What is this torrent, judging by its `name` and `files`?",
			"criteria": map[string]any{
				"movie":    "A feature film or short film",
				"series":   "Episodes or seasons of a TV series, including animated series",
				"music":    "Music albums, tracks or discographies",
				"book":     "E-books or audiobooks",
				"software": "Programs, operating systems, games",
				"adult":    rot13.String("Cbeabtencul"),
				"other":    "Anything else",
			},
		},
		"russian_audio": map[string]any{
			"type":         "noul",
			"instructions": "Does this release carry a Russian-language audio track (dubbing, voice-over, or Russian original), judging by its `name` and `files`?",
			"criteria": map[string]any{
				"true": map[string]any{
					"signs":   "Russian audio is named or clearly implied: Rus, RUS, rus in a file or folder name, Дубляж, Dub, MVO, DVO, AVO, ПМ, a separate Rus*.ac3/.mka/.aac track, a Russian-language title of a video release",
					"studios": "A Russian dubbing or voice-over studio or release group: " + strings.Join(rules.Studios(), ", "),
				},
				"false": "No sign of Russian audio, or not a video release",
			},
		},
		"source": map[string]any{
			"type":         "choice",
			"instructions": "If this is a video release, what kind of copy is it?",
			"criteria": map[string]any{
				"disc_image": "A Blu-ray or DVD disc image: BDMV, VIDEO_TS, ISO, m2ts streams",
				"remux":      "A remux: the disc's streams in a container without re-encoding",
				"encode":     "A re-encoded copy: BDRip, BRRip, x264/x265 encodes",
				"web":        "A WEB-DL or WEBRip copy",
				"other":      "Another video source, or not a video release",
			},
		},
	}
	if !askKind {
		delete(q, "kind")
	}
	return q
}

type answer struct {
	Choice     string   `json:"choice"`
	Noul       *float64 `json:"noul"`
	Confidence float64  `json:"confidence"`
}

type response struct {
	Answers map[string]answer `json:"answers"`
	Usage   struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

// Classify asks the classifier about the torrent, retrying overload and
// rate-limit answers. Errors from the endpoint are *APIError.
func (c *Client) Classify(ctx context.Context, in Input) (Result, error) {
	files := make([]string, len(in.Files))
	for i, f := range in.Files {
		files[i] = fmt.Sprintf("%s (%d MiB)", f.Path, f.Size>>20)
	}
	body, err := json.Marshal(map[string]any{
		"model": c.Model,
		"state": map[string]any{
			"name":     in.Name,
			"size_gib": fmt.Sprintf("%.2f", float64(in.Size)/(1<<30)),
			"files":    files,
		},
		"questions": questions(in.AskKind),
	})
	if err != nil {
		return Result{}, err
	}
	wait := c.Backoff
	for attempt := 0; ; attempt++ {
		res, err := c.post(ctx, body, in.AskKind)
		var apiErr *APIError
		if err == nil || !errors.As(err, &apiErr) || !retryable(apiErr.Status) || attempt >= c.Retries {
			return res, err
		}
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-time.After(wait):
		}
		wait *= 2
	}
}

func (c *Client) post(ctx context.Context, body []byte, askKind bool) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 1000))
		return Result{}, &APIError{Status: res.StatusCode, Body: strings.TrimSpace(string(msg))}
	}
	var r response
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		return Result{}, fmt.Errorf("typesafe: decode answer: %w", err)
	}
	kind, source, russian := r.Answers["kind"], r.Answers["source"], r.Answers["russian_audio"]
	if askKind && kind.Choice == "" || source.Choice == "" || russian.Noul == nil {
		return Result{}, errors.New("typesafe: incomplete answer")
	}
	out := Result{
		Source:           source.Choice,
		SourceConfidence: source.Confidence,
		RussianAudio:     *russian.Noul,
		Tokens:           r.Usage.InputTokens + r.Usage.OutputTokens,
	}
	if askKind {
		out.Kind, out.KindConfidence = kind.Choice, kind.Confidence
	}
	return out, nil
}
