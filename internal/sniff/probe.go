package sniff

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
	"github.com/mikluko/sniffarr/internal/container"
)

const (
	DefaultProbers      = 4
	DefaultProbeTimeout = 10 * time.Minute
	DefaultProbePoll    = 30 * time.Second
)

// ErrTracksUnknown is returned by a ProbeSource that cannot learn a
// torrent's tracks: it has no file of a format container reads, or the
// track list is out of reach.
var ErrTracksUnknown = errors.New("tracks unknown")

// errTooFar is returned by a pieceReader asked for data that would take it
// past its download budget.
var errTooFar = errors.New("beyond the probe's download budget")

// Probed is what a probe read of a torrent's main video file.
type Probed struct {
	// Path is the file's path inside the torrent, components joined by "/".
	Path   string
	Format container.Format
	Tracks []container.Track
	// Downloaded is the piece data the probe downloaded, in bytes, whatever
	// its outcome.
	Downloaded int64
}

// ProbeSource reads the tracks of a torrent's main video file, downloading
// maxBytes of piece data at most. It fails with ErrNoPeers, ErrNoMetadata,
// ErrTracksUnknown, or the context's error once it is done.
type ProbeSource interface {
	Probe(ctx context.Context, h Hash, maxBytes int64) (Probed, error)
}

// probeExtensions are the extensions of the files container reads.
var probeExtensions = []string{".mkv", ".webm", ".mp4", ".m4v", ".mov"}

// mainFile returns the index of the largest file of a format container
// reads; false when there is none.
func mainFile(files []File) (int, bool) {
	best := -1
	for i, f := range files {
		ext := strings.ToLower(path.Ext(f.Path))
		if !slices.Contains(probeExtensions, ext) {
			continue
		}
		if best < 0 || f.Size > files[best].Size {
			best = i
		}
	}
	return best, best >= 0
}

// pieces is the piece layer of a torrent whose info is known.
type pieces interface {
	pieceLength() int64
	numPieces() int
	// pieceSize is the length of piece i, shorter for the last.
	pieceSize(i int) int64
	complete(i int) bool
	// request asks for piece i to be downloaded first.
	request(i int)
	// wait returns once piece i is downloaded and verified, or ctx is done.
	wait(ctx context.Context, i int) error
	// readAt reads verified data at the torrent offset off.
	readAt(ctx context.Context, p []byte, off int64) (int, error)
}

// pieceReader reads a file of a torrent, downloading the pieces each read
// needs, up to max bytes of piece data over all reads.
type pieceReader struct {
	ctx context.Context
	src pieces
	// base is the file's offset in the torrent and size its length.
	base, size int64
	max        int64
	downloaded int64
}

func (r *pieceReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("offset %d", off)
	}
	if off >= r.size {
		return 0, io.EOF
	}
	n := min(int64(len(p)), r.size-off)
	if n == 0 {
		return 0, nil
	}
	pl := r.src.pieceLength()
	first, last := int((r.base+off)/pl), int((r.base+off+n-1)/pl)
	var need []int
	var cost int64
	for i := first; i <= last; i++ {
		if !r.src.complete(i) {
			need = append(need, i)
			cost += r.src.pieceSize(i)
		}
	}
	if r.downloaded+cost > r.max {
		return 0, errTooFar
	}
	for _, i := range need {
		r.src.request(i)
	}
	for _, i := range need {
		if err := r.src.wait(r.ctx, i); err != nil {
			return 0, err
		}
		r.downloaded += r.src.pieceSize(i)
	}
	got, err := r.src.readAt(r.ctx, p[:n], r.base+off)
	if err == nil && int64(got) < int64(len(p)) {
		err = io.EOF
	}
	return got, err
}

// ProberConfig tunes a Prober. Zero fields take the default named in their
// doc.
type ProberConfig struct {
	Ingest sniffarrv1connect.IngestServiceClient
	// Workers is how many probes run at once; DefaultProbers.
	Workers int
	// Timeout bounds one probe, from looking for peers to the tracks;
	// DefaultProbeTimeout.
	Timeout time.Duration
	// Poll is the wait after the server had no task or failed;
	// DefaultProbePoll.
	Poll time.Duration
	// Log discards everything when nil.
	Log *slog.Logger
	// Meter records metrics; the global MeterProvider when nil.
	Meter metric.MeterProvider
}

// Prober takes probe tasks from the server's ProbeTask, reads the tracks
// with a ProbeSource, and returns them with ProbeResult.
type Prober struct {
	cfg      ProberConfig
	source   ProbeSource
	outcomes metric.Int64Counter
	// id is sent with every ProbeTask, for the server to count probers by.
	id string
}

// NewProber returns a Prober for cfg that probes with source.
func NewProber(cfg ProberConfig, source ProbeSource) *Prober {
	orDefault(&cfg.Workers, DefaultProbers)
	orDefault(&cfg.Timeout, DefaultProbeTimeout)
	orDefault(&cfg.Poll, DefaultProbePoll)
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	return &Prober{cfg: cfg, source: source, id: rand.Text(),
		outcomes: counter(meter(cfg.Meter), "sniffarr.sniff.probe.outcomes", "{task}", "Probe tasks finished, by outcome.")}
}

// Run probes until ctx is done. A probe in flight at shutdown is abandoned,
// and its lease expires on the server.
func (p *Prober) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for range p.cfg.Workers {
		wg.Go(func() {
			for ctx.Err() == nil {
				if !p.once(ctx) && sleep(ctx, p.cfg.Poll) != nil {
					return
				}
			}
		})
	}
	wg.Wait()
	return ctx.Err()
}

// once runs one task and reports whether there was one.
func (p *Prober) once(ctx context.Context) bool {
	res, err := p.cfg.Ingest.ProbeTask(ctx, connect.NewRequest(&sniffarrv1.ProbeTaskRequest{Prober: p.id}))
	if err != nil {
		if ctx.Err() == nil {
			p.cfg.Log.Warn("probe task", "err", err)
		}
		return false
	}
	task := res.Msg.GetTask()
	if task == nil || len(task.GetInfohash()) != len(Hash{}) {
		return false
	}
	h := Hash(task.GetInfohash())
	pctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	probed, err := p.source.Probe(pctx, h, task.GetMaxBytes())
	cancel()
	if ctx.Err() != nil {
		return true
	}
	req, outcome := probeResult(h, probed, err)
	p.outcomes.Add(ctx, 1, metric.WithAttributes(attribute.String("sniffarr.probe.outcome", outcome)))
	p.cfg.Log.Debug("probed", "infohash", hex.EncodeToString(h[:]), "outcome", outcome, "path", probed.Path,
		"tracks", len(probed.Tracks), "downloaded", probed.Downloaded, "err", err)
	if _, err := p.cfg.Ingest.ProbeResult(ctx, connect.NewRequest(req)); err != nil && ctx.Err() == nil {
		p.cfg.Log.Warn("probe result", "infohash", hex.EncodeToString(h[:]), "err", err)
	}
	return true
}

// probeResult is the result of a probe and the name of its outcome.
func probeResult(h Hash, probed Probed, err error) (*sniffarrv1.ProbeResultRequest, string) {
	req := &sniffarrv1.ProbeResultRequest{Infohash: h[:], Downloaded: probed.Downloaded}
	failure := func(f sniffarrv1.ProbeFailure, name string) (*sniffarrv1.ProbeResultRequest, string) {
		req.Outcome = &sniffarrv1.ProbeResultRequest_Failure{Failure: f}
		return req, name
	}
	switch {
	case err == nil:
		file := &sniffarrv1.ProbedFile{Path: strings.ToValidUTF8(probed.Path, "�"), Format: string(probed.Format)}
		for _, t := range probed.Tracks {
			file.Tracks = append(file.Tracks, trackOf(t))
		}
		req.Outcome = &sniffarrv1.ProbeResultRequest_File{File: file}
		return req, "tracks"
	case errors.Is(err, ErrNoPeers):
		return failure(sniffarrv1.ProbeFailure_PROBE_FAILURE_NO_PEERS, "no_peers")
	case errors.Is(err, ErrTracksUnknown):
		return failure(sniffarrv1.ProbeFailure_PROBE_FAILURE_UNKNOWN, "unknown")
	}
	return failure(sniffarrv1.ProbeFailure_PROBE_FAILURE_TIMEOUT, "timeout")
}

var trackKinds = map[container.Kind]sniffarrv1.TrackKind{
	container.Video:    sniffarrv1.TrackKind_TRACK_KIND_VIDEO,
	container.Audio:    sniffarrv1.TrackKind_TRACK_KIND_AUDIO,
	container.Subtitle: sniffarrv1.TrackKind_TRACK_KIND_SUBTITLE,
}

// trackOf is the API's view of a track; invalid UTF-8 in its strings is
// replaced by U+FFFD, which a proto string requires.
func trackOf(t container.Track) *sniffarrv1.Track {
	valid := func(s string) string { return strings.ToValidUTF8(s, "�") }
	return &sniffarrv1.Track{Kind: trackKinds[t.Kind], Language: valid(t.Language), LanguageBcp47: valid(t.LanguageBCP47), Name: valid(t.Name),
		Codec: valid(t.Codec), Channels: int32(t.Channels), Width: int32(t.Width), Height: int32(t.Height), Hdr: t.HDR}
}
