package ingest

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/container"
	"sarak.sh/mikluko/sniffarr/internal/orders"
	"sarak.sh/mikluko/sniffarr/internal/store"
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

// backgroundBatch is how many background probe orders ScheduleProbes
// publishes at once.
const backgroundBatch = 64

// probeSchedule is how often ScheduleProbes looks for background probes.
const probeSchedule = time.Minute

// ProbeConfig tunes the probe tasks of a Service. Zero fields other than
// DailyBytes and SearchReserve take the default named in their doc.
type ProbeConfig struct {
	// DailyBytes caps the piece data probes download per UTC day; 0 is no
	// cap. A probe handed out counts as MaxBytes until its result reports
	// what it downloaded.
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
// today and reserved are held by probes handed out; a probe of a release no
// search returned leaves SearchReserve percent of DailyBytes.
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

func (s *Service) ProbeTask(ctx context.Context, req *connect.Request[sniffarrv1.ProbeTaskRequest]) (*connect.Response[sniffarrv1.ProbeTaskResponse], error) {
	res := connect.NewResponse(&sniffarrv1.ProbeTaskResponse{})
	if !s.Probes {
		return res, nil
	}
	now := s.now()
	s.Probers.Seen(req.Msg.GetProber(), now)
	o, err := s.takeProbe(ctx, now)
	if err != nil {
		return nil, s.internal(ctx, "take probe", err)
	}
	if o == nil {
		return res, nil
	}
	cfg := s.Probe
	if o.Leases == 1 {
		l := store.ProbeLease{Hash: o.Hash, Priority: o.Priority, MaxBytes: cfg.maxBytes(), Until: now.Add(s.ProbeOrders.Lease())}
		if err := s.Store.RecordProbeLease(ctx, l, now); err != nil {
			s.Log.WarnContext(ctx, "journal probe lease", "err", err)
		}
	}
	s.metrics().probeTasks.Add(ctx, 1, priorityAttr(o.Priority <= store.PriorityArr))
	res.Msg.Task = &sniffarrv1.ProbeTask{Infohash: o.Hash[:], MaxBytes: cfg.maxBytes()}
	return res, nil
}

// takeProbe hands out the next probe order the day's budget admits at now,
// acknowledging those up to it whose release is no longer due a probe, and
// returns nil for none. A request is due whatever the release's last probe.
func (s *Service) takeProbe(ctx context.Context, now time.Time) (*orders.Order, error) {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	cfg := s.Probe
	traffic, err := s.Store.ProbeTraffic(ctx, day(now))
	if err != nil {
		return nil, err
	}
	reserved := int64(s.ProbeOrders.Held(store.TaskProbe)) * cfg.maxBytes()
	if !cfg.admits(traffic.Rx, reserved, true) {
		return nil, nil
	}
	upTo := store.PrioritySample
	if !cfg.admits(traffic.Rx, reserved, false) {
		upTo = store.PriorityArr
	}
	owner := s.owners.Add(1)
	for {
		got, err := s.ProbeOrders.Take(owner, 1, upTo)
		if err != nil || len(got) == 0 {
			return nil, err
		}
		o := got[0]
		if o.Priority == store.PriorityRequest {
			return o, nil
		}
		due, err := s.Store.ProbeDue(ctx, o.Hash, now)
		if err != nil {
			s.ProbeOrders.Release(owner)
			return nil, err
		}
		if due {
			return o, nil
		}
		if o := s.ProbeOrders.DoneBy(store.TaskProbe, o.Hash, owner); o != nil {
			if err := s.ProbeOrders.Ack(ctx, o); err != nil {
				return nil, err
			}
		}
	}
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
	r := probeRecord{s: s, h: h, now: s.now(), downloaded: m.GetDownloaded(), leases: 1}
	switch o := m.GetOutcome().(type) {
	case *sniffarrv1.ProbeResultRequest_File:
		r.probe, r.name = probedFile(o.File, m.GetDownloaded())
	case *sniffarrv1.ProbeResultRequest_Failure:
		switch o.Failure {
		case sniffarrv1.ProbeFailure_PROBE_FAILURE_UNKNOWN:
			r.probe, r.name = store.Probe{Outcome: store.ProbeUnknown, Downloaded: m.GetDownloaded()}, "unknown"
		case sniffarrv1.ProbeFailure_PROBE_FAILURE_NO_PEERS:
			r.name, r.failed = store.ProbeNoPeers, true
		case sniffarrv1.ProbeFailure_PROBE_FAILURE_TIMEOUT:
			r.name, r.failed = store.ProbeTimeout, true
		default:
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("probe failure %v", o.Failure))
		}
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("probe result without outcome"))
	}
	if s.ProbeOrders != nil {
		r.order = s.ProbeOrders.Done(store.TaskProbe, h)
		s.ProbeOrders.Finished(store.TaskProbe, r.now)
	}
	if r.order != nil {
		r.leases = r.order.Leases
	}
	if n := m.GetDownloaded(); n > 0 {
		if err := s.Store.AddProbeTraffic(ctx, day(r.now), store.ProbeTraffic{Rx: n}); err != nil {
			r.nak()
			return nil, s.internal(ctx, "add probe traffic", err)
		}
		s.metrics().probeBytes.Add(ctx, n)
	}
	if err := r.record(ctx); err != nil {
		r.nak()
		return nil, s.internal(ctx, "record probe", err)
	}
	s.metrics().probeResults.Add(ctx, 1, probeOutcomeAttr(r.name))
	return connect.NewResponse(&sniffarrv1.ProbeResultResponse{}), nil
}

// probeRecord is a probe result to record, with the order it settles, nil
// when none is held for it.
type probeRecord struct {
	s          *Service
	h          store.Hash
	now        time.Time
	order      *orders.Order
	name       string
	probe      store.Probe
	failed     bool
	downloaded int64
	leases     int
}

// record stores the result, then acknowledges the order, or for a failure
// short of the last attempt of a probe a request or a search ordered,
// schedules it again on PriorityArr once its backoff is out.
func (r *probeRecord) record(ctx context.Context) error {
	s, cfg := r.s, r.s.Probe
	if !r.failed {
		r.probe.Leases = r.leases
		if err := s.Store.ProbeDone(ctx, r.h, r.probe, r.now); err != nil {
			return err
		}
		return r.ack(ctx)
	}
	final, retry, err := s.Store.ProbeFailed(ctx, r.h, r.name, r.downloaded, r.leases, r.now, or(cfg.Attempts, DefaultProbeAttempts), or(cfg.Backoff, DefaultProbeBackoff))
	if err != nil {
		return err
	}
	if r.order == nil || final || r.order.Priority > store.PriorityArr {
		return r.ack(ctx)
	}
	l, _ := s.ProbeOrders.Lane(store.TaskProbe, store.PriorityArr)
	return s.ProbeOrders.Retry(ctx, r.order, l, retry)
}

func (r *probeRecord) ack(ctx context.Context) error {
	if r.order == nil {
		return nil
	}
	return r.s.ProbeOrders.Ack(ctx, r.order)
}

func (r *probeRecord) nak() {
	if r.order != nil {
		r.s.ProbeOrders.Nak(r.order)
	}
}

// WantProbe orders, on PriorityArr, the probes of the stored releases of the
// hashes a search w returned at the time, as store.WantProbe selects them.
func (s *Service) WantProbe(ctx context.Context, hashes []store.Hash, w store.ProbeWant, at time.Time) error {
	want, err := s.Store.WantProbe(ctx, hashes, w, at)
	if err != nil || len(want) == 0 {
		return err
	}
	items := make([]orders.Item, len(want))
	for i, o := range want {
		items[i] = orders.Item{Hash: o.Hash, At: o.At, ID: "search:" + hex.EncodeToString(o.Hash[:])}
	}
	l, _ := s.ProbeOrders.Lane(store.TaskProbe, store.PriorityArr)
	_, err = s.ProbeOrders.Publish(ctx, l, items)
	return err
}

// ScheduleProbes orders background probes on PrioritySample every
// probeSchedule until ctx ends.
func (s *Service) ScheduleProbes(ctx context.Context) error {
	t := time.NewTicker(probeSchedule)
	defer t.Stop()
	for {
		if _, err := s.scheduleProbes(ctx, s.now()); err != nil && ctx.Err() == nil {
			s.Log.ErrorContext(ctx, "schedule probes", "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// scheduleProbes publishes up to backgroundBatch background probes due at
// now on PrioritySample when none is queued there, and returns how many the
// stream took.
func (s *Service) scheduleProbes(ctx context.Context, now time.Time) (int, error) {
	levels, err := s.ProbeOrders.Levels(ctx, now)
	if err != nil {
		return 0, err
	}
	if q, ok := levels[store.TaskProbe]; ok && q.Levels[store.PrioritySample].Tasks > 0 {
		return 0, nil
	}
	hashes, err := s.Store.BackgroundProbes(ctx, backgroundBatch, now)
	if err != nil || len(hashes) == 0 {
		return 0, err
	}
	items := make([]orders.Item, len(hashes))
	for i, h := range hashes {
		items[i] = orders.Item{Hash: h, ID: "background:" + hex.EncodeToString(h[:])}
	}
	l, _ := s.ProbeOrders.Lane(store.TaskProbe, store.PrioritySample)
	return s.ProbeOrders.Publish(ctx, l, items)
}

func (s *Service) ProbeUpload(ctx context.Context, req *connect.Request[sniffarrv1.ProbeUploadRequest]) (*connect.Response[sniffarrv1.ProbeUploadResponse], error) {
	n := req.Msg.GetUploaded()
	if n < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("uploaded %d", n))
	}
	if n > 0 {
		if err := s.Store.AddProbeTraffic(ctx, day(s.now()), store.ProbeTraffic{Tx: n}); err != nil {
			return nil, s.internal(ctx, "add probe traffic", err)
		}
		s.metrics().probeUploaded.Add(ctx, n)
	}
	return connect.NewResponse(&sniffarrv1.ProbeUploadResponse{}), nil
}

// probedFile is the probe f records, with downloaded bytes, and the name of
// its outcome; a track list longer than container.TrackLimit is one a probe
// cannot read, as the fetcher's probe reports it.
func probedFile(f *sniffarrv1.ProbedFile, downloaded int64) (store.Probe, string) {
	if len(f.GetTracks()) > container.TrackLimit {
		return store.Probe{Outcome: store.ProbeUnknown, Downloaded: downloaded}, "unknown"
	}
	p := store.Probe{Outcome: store.ProbeTracks, Path: f.GetPath(), Format: f.GetFormat(), Downloaded: downloaded}
	for _, t := range f.GetTracks() {
		p.Tracks = append(p.Tracks, trackOf(t))
	}
	return p, "tracks"
}

func trackOf(t *sniffarrv1.Track) store.Track {
	kinds := map[sniffarrv1.TrackKind]string{
		sniffarrv1.TrackKind_TRACK_KIND_VIDEO:    "video",
		sniffarrv1.TrackKind_TRACK_KIND_AUDIO:    "audio",
		sniffarrv1.TrackKind_TRACK_KIND_SUBTITLE: "subtitle",
	}
	return store.Track{Kind: kinds[t.GetKind()], Language: t.GetLanguage(), LanguageBCP47: t.GetLanguageBcp47(), Name: t.GetName(),
		Codec: t.GetCodec(), Channels: int(t.GetChannels()), Width: int(t.GetWidth()), Height: int(t.GetHeight()), HDR: t.GetHdr(), Default: t.GetDefault()}
}

// day is the UTC day of t, as the store keys daily usage.
func day(t time.Time) string {
	return t.UTC().Format(time.DateOnly)
}
