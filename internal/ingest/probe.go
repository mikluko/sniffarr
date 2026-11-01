package ingest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"connectrpc.com/connect"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/store"
)

const (
	// DefaultProbeDailyBytes is the default ProbeConfig.DailyBytes, 100 GiB.
	DefaultProbeDailyBytes = 100 << 30
	// DefaultProbeSearchReserve is the default ProbeConfig.SearchReserve.
	DefaultProbeSearchReserve = 25
	DefaultProbeMaxBytes      = 64 << 20
	DefaultProbeLease         = 15 * time.Minute
	DefaultProbeAttempts      = 3
	DefaultProbeBackoff       = 6 * time.Hour
)

// ProbeConfig tunes the probe tasks of a Service. Zero fields other than
// DailyBytes and SearchReserve take the default named in their doc.
type ProbeConfig struct {
	// DailyBytes caps the piece data probes download per UTC day; 0 is no
	// cap. A lease counts as MaxBytes until its result reports what it
	// downloaded.
	DailyBytes int64
	// SearchReserve is the percentage of DailyBytes that only releases a
	// search returned may use.
	SearchReserve int
	// MaxBytes is the most piece data one probe may download;
	// DefaultProbeMaxBytes.
	MaxBytes int64
	// Lease is how long a fetcher holds a probe task; DefaultProbeLease.
	Lease time.Duration
	// Attempts is how many failed probes end a release's probing;
	// DefaultProbeAttempts.
	Attempts int
	// Backoff is the wait before a failed probe is repeated, doubled for
	// each failure before; DefaultProbeBackoff.
	Backoff time.Duration
}

// admits reports whether a probe may start when used bytes were downloaded
// today and reserved are held by leases; a probe of a release no search
// returned leaves SearchReserve percent of DailyBytes.
func (c ProbeConfig) admits(used, reserved int64, wanted bool) bool {
	if c.DailyBytes <= 0 {
		return true
	}
	limit := c.DailyBytes
	if !wanted {
		limit = int64(float64(c.DailyBytes) * float64(100-c.SearchReserve) / 100)
	}
	return used+reserved < limit
}

func (c ProbeConfig) maxBytes() int64 {
	if c.MaxBytes > 0 {
		return c.MaxBytes
	}
	return DefaultProbeMaxBytes
}

// probeLease is a probe task handed out and not yet reported.
type probeLease struct {
	id    int64
	until time.Time
}

// probeLeases are the probe tasks in flight. They live in memory: a lease
// lost to a restart frees its release and its share of the budget.
type probeLeases struct {
	mu sync.Mutex
	m  map[store.Hash]probeLease
}

// active drops the leases expired at now and returns the release IDs of
// the rest.
func (l *probeLeases) active(now time.Time) []int64 {
	var ids []int64
	for h, p := range l.m {
		if !now.Before(p.until) {
			delete(l.m, h)
			continue
		}
		ids = append(ids, p.id)
	}
	return ids
}

func (l *probeLeases) add(h store.Hash, p probeLease) {
	if l.m == nil {
		l.m = map[store.Hash]probeLease{}
	}
	l.m[h] = p
}

func (s *Service) ProbeTask(ctx context.Context, _ *connect.Request[sniffarrv1.ProbeTaskRequest]) (*connect.Response[sniffarrv1.ProbeTaskResponse], error) {
	cfg := s.Probe
	s.probes.mu.Lock()
	defer s.probes.mu.Unlock()
	now := s.now()
	leased := s.probes.active(now)
	reserved := int64(len(leased)) * cfg.maxBytes()
	used, err := s.Store.ProbeBytesUsed(ctx, day(now))
	if err != nil {
		return nil, s.internal(ctx, "probe bytes used", err)
	}
	res := connect.NewResponse(&sniffarrv1.ProbeTaskResponse{})
	if !cfg.admits(used, reserved, true) {
		return res, nil
	}
	c, ok, err := s.Store.NextProbe(ctx, now, cfg.admits(used, reserved, false), leased)
	if err != nil {
		return nil, s.internal(ctx, "next probe", err)
	}
	if !ok {
		return res, nil
	}
	s.probes.add(c.Hash, probeLease{id: c.ID, until: now.Add(or(cfg.Lease, DefaultProbeLease))})
	s.metrics().probeTasks.Add(ctx, 1, priorityAttr(c.Wanted))
	res.Msg.Task = &sniffarrv1.ProbeTask{Infohash: c.Hash[:], MaxBytes: cfg.maxBytes()}
	return res, nil
}

func (s *Service) ProbeResult(ctx context.Context, req *connect.Request[sniffarrv1.ProbeResultRequest]) (*connect.Response[sniffarrv1.ProbeResultResponse], error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resultTimeout)
	defer cancel()
	m := req.Msg
	h, err := hash(m.GetInfohash())
	if err != nil {
		return nil, err
	}
	if m.GetDownloaded() < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("downloaded %d", m.GetDownloaded()))
	}
	var record func() error
	var name string
	now := s.now()
	switch o := m.GetOutcome().(type) {
	case *sniffarrv1.ProbeResultRequest_File:
		name = "tracks"
		p := store.Probe{Outcome: store.ProbeTracks, Path: o.File.GetPath(), Format: o.File.GetFormat(), Downloaded: m.GetDownloaded()}
		for _, t := range o.File.GetTracks() {
			p.Tracks = append(p.Tracks, trackOf(t))
		}
		record = func() error { return s.Store.ProbeDone(ctx, h, p, now) }
	case *sniffarrv1.ProbeResultRequest_Failure:
		switch o.Failure {
		case sniffarrv1.ProbeFailure_PROBE_FAILURE_UNKNOWN:
			name = "unknown"
			record = func() error {
				return s.Store.ProbeDone(ctx, h, store.Probe{Outcome: store.ProbeUnknown, Downloaded: m.GetDownloaded()}, now)
			}
		case sniffarrv1.ProbeFailure_PROBE_FAILURE_NO_PEERS, sniffarrv1.ProbeFailure_PROBE_FAILURE_TIMEOUT:
			name = "timeout"
			if o.Failure == sniffarrv1.ProbeFailure_PROBE_FAILURE_NO_PEERS {
				name = "no_peers"
			}
			cfg := s.Probe
			record = func() error {
				_, err := s.Store.ProbeFailed(ctx, h, now, or(cfg.Attempts, DefaultProbeAttempts), or(cfg.Backoff, DefaultProbeBackoff))
				return err
			}
		default:
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("probe failure %v", o.Failure))
		}
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("probe result without outcome"))
	}
	s.probes.mu.Lock()
	delete(s.probes.m, h)
	s.probes.mu.Unlock()
	if n := m.GetDownloaded(); n > 0 {
		if err := s.Store.AddProbeBytes(ctx, day(now), n); err != nil {
			return nil, s.internal(ctx, "add probe bytes", err)
		}
		s.metrics().probeBytes.Add(ctx, n)
	}
	if err := record(); err != nil {
		return nil, s.internal(ctx, "record probe", err)
	}
	s.metrics().probeResults.Add(ctx, 1, probeOutcomeAttr(name))
	return connect.NewResponse(&sniffarrv1.ProbeResultResponse{}), nil
}

func trackOf(t *sniffarrv1.Track) store.Track {
	kinds := map[sniffarrv1.TrackKind]string{
		sniffarrv1.TrackKind_TRACK_KIND_VIDEO:    "video",
		sniffarrv1.TrackKind_TRACK_KIND_AUDIO:    "audio",
		sniffarrv1.TrackKind_TRACK_KIND_SUBTITLE: "subtitle",
	}
	return store.Track{Kind: kinds[t.GetKind()], Language: t.GetLanguage(), LanguageBCP47: t.GetLanguageBcp47(), Name: t.GetName(),
		Codec: t.GetCodec(), Channels: int(t.GetChannels()), Width: int(t.GetWidth()), Height: int(t.GetHeight()), HDR: t.GetHdr()}
}

// day is the UTC day of t, as the store keys daily usage.
func day(t time.Time) string {
	return t.UTC().Format(time.DateOnly)
}
