package ingest

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"sarak.sh/mikluko/libarr/database"
	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/bus"
	"sarak.sh/mikluko/sniffarr/internal/bus/bustest"
	"sarak.sh/mikluko/sniffarr/internal/container"
	"sarak.sh/mikluko/sniffarr/internal/orders"
	"sarak.sh/mikluko/sniffarr/internal/rules"
	"sarak.sh/mikluko/sniffarr/internal/store"
)

func TestProbeBudgetAdmits(t *testing.T) {
	b := ProbeConfig{DailyBytes: 100, SearchReserve: 25}
	for _, tc := range []struct {
		used, reserved int64
		wanted, want   bool
	}{
		{0, 0, false, true},
		{70, 4, false, true},
		{70, 5, false, false},
		{70, 5, true, true},
		{99, 0, true, true},
		{60, 40, true, false},
	} {
		require.Equal(t, tc.want, b.admits(tc.used, tc.reserved, tc.wanted), "%+v", tc)
	}
	unlimited := ProbeConfig{SearchReserve: 25}
	require.True(t, unlimited.admits(1<<60, 1<<60, false), "0 is no limit")
}

type probeFixture struct {
	s  *Service
	st *store.Store
	db *database.DB
	js jetstream.JetStream
}

// newProbeFixture serves a store and a bus, with the probe orders' queue
// started on it, on the wall clock that the bus schedules by.
func newProbeFixture(t *testing.T, cfg ProbeConfig) *probeFixture {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{Service: "sniffarr"})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	st, err := store.New(ctx, db, store.Options{Languages: []string{"ru"}})
	require.NoError(t, err)
	js, err := jetstream.New(bustest.Conn(t))
	require.NoError(t, err)
	probes := orders.New(orders.Config{Stream: bus.StreamProbeOrders, Lanes: bus.ProbeLanes, Lease: or(cfg.Lease, DefaultProbeLease), Heartbeat: 50 * time.Millisecond})
	require.NoError(t, probes.Start(ctx, js))
	f := &probeFixture{st: st, db: db, js: js}
	f.s = &Service{Store: st, ProbeOrders: probes, Probers: &store.Probers{}, Log: slog.New(slog.DiscardHandler), Probe: cfg, Probes: true}
	return f
}

// today is the day the store keys the fixture's probe traffic by.
func (f *probeFixture) today() string {
	return day(f.s.now())
}

func (f *probeFixture) insert(t *testing.T, b byte, name string, peers int64) store.Hash {
	t.Helper()
	ctx := context.Background()
	h := store.Hash{b}
	_, _, err := f.st.Insert(ctx, store.Torrent{Hash: h, Name: name, Size: 8 << 30, Files: []rules.File{{Path: name + ".mkv", Size: 8 << 30}},
		Discovery: store.Sample, SeenAt: f.s.now(), Kind: "movie", KindLayer: store.LayerRules})
	require.NoError(t, err)
	_, err = f.db.W.ExecContext(ctx, "UPDATE torrent_releases SET peers_week = ? WHERE infohash = ?", peers, h[:])
	require.NoError(t, err)
	return h
}

func (f *probeFixture) task(t *testing.T) *sniffarrv1.ProbeTask {
	t.Helper()
	res, err := f.s.ProbeTask(context.Background(), connect.NewRequest(&sniffarrv1.ProbeTaskRequest{}))
	require.NoError(t, err)
	return res.Msg.GetTask()
}

func (f *probeFixture) result(t *testing.T, r *sniffarrv1.ProbeResultRequest) {
	t.Helper()
	_, err := f.s.ProbeResult(context.Background(), connect.NewRequest(r))
	require.NoError(t, err)
}

// order publishes a probe order of each hash at the priority.
func (f *probeFixture) order(t *testing.T, priority int, hs ...store.Hash) {
	t.Helper()
	l, ok := f.s.ProbeOrders.Lane(store.TaskProbe, priority)
	require.True(t, ok)
	items := make([]orders.Item, len(hs))
	for i, h := range hs {
		items[i] = orders.Item{Hash: h}
	}
	_, err := f.s.ProbeOrders.Publish(t.Context(), l, items)
	require.NoError(t, err)
}

// schedule orders the background probes due now.
func (f *probeFixture) schedule(t *testing.T) int {
	t.Helper()
	n, err := f.s.scheduleProbes(t.Context(), f.s.now())
	require.NoError(t, err)
	return n
}

// stream returns the messages left on the probe orders' stream.
func (f *probeFixture) stream(t *testing.T) []*jetstream.RawStreamMsg {
	t.Helper()
	stream, err := f.js.Stream(t.Context(), bus.StreamProbeOrders)
	require.NoError(t, err)
	info, err := stream.Info(t.Context())
	require.NoError(t, err)
	var out []*jetstream.RawStreamMsg
	for seq := info.State.FirstSeq; info.State.Msgs > 0 && seq <= info.State.LastSeq; seq++ {
		if m, err := stream.GetMsg(t.Context(), seq); err == nil {
			out = append(out, m)
		}
	}
	return out
}

func timeout(h store.Hash, downloaded int64) *sniffarrv1.ProbeResultRequest {
	return &sniffarrv1.ProbeResultRequest{Infohash: h[:], Downloaded: downloaded, Outcome: &sniffarrv1.ProbeResultRequest_Failure{Failure: sniffarrv1.ProbeFailure_PROBE_FAILURE_TIMEOUT}}
}

func TestProbeTaskBudget(t *testing.T) {
	ctx := context.Background()
	f := newProbeFixture(t, ProbeConfig{DailyBytes: 100 << 20, SearchReserve: 50, MaxBytes: 40 << 20})
	a := f.insert(t, 1, "Alien.1979.1080p", 30)
	b := f.insert(t, 2, "Brazil.1985.1080p", 20)
	c := f.insert(t, 3, "Coraline.2009.2160p", 10)
	require.Equal(t, 3, f.schedule(t))
	require.Zero(t, f.schedule(t), "nothing more while background orders are queued")

	first := f.task(t)
	require.Equal(t, a[:], first.GetInfohash(), "most peers first")
	require.EqualValues(t, 40<<20, first.GetMaxBytes())
	require.Equal(t, b[:], f.task(t).GetInfohash(), "an order held is not handed out twice")
	require.Nil(t, f.task(t), "background probes stop at half the budget, counting what the probes held may download")

	require.NoError(t, f.s.WantProbe(ctx, []store.Hash{c}, store.ProbeWant{T: "movie", Query: "coraline"}, f.s.now()))
	require.Equal(t, c[:], f.task(t).GetInfohash(), "a searched release uses the reserve")
	require.Nil(t, f.task(t), "the whole budget is held")

	f.result(t, &sniffarrv1.ProbeResultRequest{Infohash: a[:], Downloaded: 16 << 20, Outcome: &sniffarrv1.ProbeResultRequest_File{File: &sniffarrv1.ProbedFile{
		Path: "Alien.1979.1080p.mkv", Format: "matroska", Tracks: []*sniffarrv1.Track{
			{Kind: sniffarrv1.TrackKind_TRACK_KIND_VIDEO, Codec: "V_MPEG4/ISO/AVC", Width: 1920, Height: 800, Default: true},
			{Kind: sniffarrv1.TrackKind_TRACK_KIND_AUDIO, Language: "rus", Name: "MVO", Codec: "A_AC3", Channels: 6},
		}}}})
	traffic, err := f.st.ProbeTraffic(ctx, f.today())
	require.NoError(t, err)
	require.Equal(t, store.ProbeTraffic{Rx: 16 << 20}, traffic)
	r, err := f.st.Get(ctx, a)
	require.NoError(t, err)
	require.Len(t, r.Tracks, 2)
	require.Equal(t, store.Track{Kind: "audio", Language: "rus", Name: "MVO", Codec: "A_AC3", Channels: 6}, r.Tracks[1])
	require.True(t, r.Tracks[0].Default)
	require.Equal(t, "V_MPEG4/ISO/AVC", r.Codec, "the video track's codec goes to the release")
	require.Equal(t, store.LayerProbe, r.Audio[0].Layer)

	f.result(t, timeout(b, 0))
	f.result(t, timeout(c, 1<<20))
	require.Nil(t, f.task(t), "a probed release is not probed again, a failed one waits out its backoff")
	require.Equal(t, []string{"fetched/stored", "probe_leased/background", "probed/tracks"}, journal(t, f.st, a))
	require.Equal(t, []string{"fetched/stored", "probe_wanted/search", "probe_leased/search", "probed/timeout"}, journal(t, f.st, c))
	got, err := f.st.Journal(ctx, b, 10)
	require.NoError(t, err)
	require.Contains(t, got[1].Detail, `"attempt":1,"lease_until":`, "an order's first hand-out is journaled")

	left := f.stream(t)
	require.Len(t, left, 1, "the results acknowledged their orders, and the stale background order of c went when pulled")
	require.Equal(t, bus.SubjectProbeRetry+"0300000000000000000000000000000000000000", left[0].Subject, "the search's retry, scheduled; the background failure's is not")
	require.Equal(t, bus.SubjectProbeP1, left[0].Header.Get(jetstream.ScheduleTargetHeader))
}

func TestProbeRetry(t *testing.T) {
	ctx := context.Background()
	f := newProbeFixture(t, ProbeConfig{Attempts: 2, Backoff: time.Second})
	h := f.insert(t, 1, "Alien.1979.1080p", 30)
	f.order(t, store.PriorityArr, h)
	require.Equal(t, h[:], f.task(t).GetInfohash())
	f.result(t, timeout(h, 0))
	require.Nil(t, f.task(t), "the retry waits for its schedule")
	require.Eventually(t, func() bool { return f.task(t) != nil }, 10*time.Second, 50*time.Millisecond, "the retry fires once the backoff is out")
	f.result(t, timeout(h, 0))
	require.Empty(t, f.stream(t), "the last attempt schedules nothing")
	require.Equal(t, []string{"fetched/stored", "probe_leased/search", "probed/timeout", "probe_leased/retry", "probed/timeout"}, journal(t, f.st, h))
	got, err := f.st.Journal(ctx, h, 1)
	require.NoError(t, err)
	require.Contains(t, got[0].Detail, `"final":true`)
}

func TestProbeRequestedFirst(t *testing.T) {
	ctx := context.Background()
	f := newProbeFixture(t, ProbeConfig{})
	done := f.insert(t, 1, "Alien.1979.1080p", 30)
	searched := f.insert(t, 2, "Brazil.1985.1080p", 20)
	require.NoError(t, f.st.ProbeDone(ctx, done, store.Probe{Outcome: store.ProbeUnknown}, f.s.now()))
	f.order(t, store.PrioritySample, searched)
	f.order(t, store.PriorityArr, done, searched)
	f.order(t, store.PriorityRequest, done)

	require.Equal(t, done[:], f.task(t).GetInfohash(), "a request first, whatever the release's last probe")
	require.Equal(t, searched[:], f.task(t).GetInfohash(), "a search's order of a probe done is dropped")
	require.Nil(t, f.task(t), "the background order of a release held is dropped")
	for _, h := range []store.Hash{done, searched} {
		f.result(t, &sniffarrv1.ProbeResultRequest{Infohash: h[:], Outcome: &sniffarrv1.ProbeResultRequest_Failure{Failure: sniffarrv1.ProbeFailure_PROBE_FAILURE_UNKNOWN}})
	}
	require.Empty(t, f.stream(t))
}

func TestProbeLeaseExpires(t *testing.T) {
	f := newProbeFixture(t, ProbeConfig{DailyBytes: 50 << 20, MaxBytes: 50 << 20, Lease: 200 * time.Millisecond})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() { _ = f.s.ProbeOrders.Run(ctx) }()
	a := f.insert(t, 1, "Alien.1979.1080p", 30)
	f.schedule(t)
	require.Equal(t, a[:], f.task(t).GetInfohash())
	require.Nil(t, f.task(t), "the budget is held")
	var again *sniffarrv1.ProbeTask
	require.Eventually(t, func() bool { again = f.task(t); return again != nil }, 5*time.Second, 50*time.Millisecond)
	require.Equal(t, a[:], again.GetInfohash(), "an expired hold hands its order back and frees its reservation")
	require.Equal(t, []string{"fetched/stored", "probe_leased/background"}, journal(t, f.st, a), "a hand-out again after one expired is the same attempt")
	f.result(t, timeout(a, 0))
	got, err := f.st.Journal(t.Context(), a, 1)
	require.NoError(t, err)
	require.Contains(t, got[0].Detail, `"leases":2`)
}

func TestProbeResultWithoutOrder(t *testing.T) {
	ctx := context.Background()
	f := newProbeFixture(t, ProbeConfig{})
	h := f.insert(t, 1, "Alien.1979.1080p", 30)
	f.result(t, timeout(h, 0))
	require.Equal(t, []string{"fetched/stored", "probed/timeout"}, journal(t, f.st, h), "a result no order is held for still records")
	due, err := f.st.ProbeDue(ctx, h, f.s.now())
	require.NoError(t, err)
	require.False(t, due)
	require.Empty(t, f.stream(t))
}

func TestProbeTaskOff(t *testing.T) {
	f := newProbeFixture(t, ProbeConfig{})
	f.s.Probes = false
	f.insert(t, 1, "Alien.1979.1080p", 30)
	f.schedule(t)
	require.Nil(t, f.task(t), "no background probe either")
	require.Zero(t, f.s.ProbeOrders.Held(store.TaskProbe))
	require.Zero(t, f.s.Probers.Count(f.s.now()), "a prober asking while probes are off is not counted")
}

func TestProbeTaskCountsProbers(t *testing.T) {
	f := newProbeFixture(t, ProbeConfig{})
	ask := func(id string) {
		t.Helper()
		_, err := f.s.ProbeTask(context.Background(), connect.NewRequest(&sniffarrv1.ProbeTaskRequest{Prober: id}))
		require.NoError(t, err)
	}
	ask("a")
	ask("a")
	ask("b")
	require.Equal(t, 2, f.s.Probers.Count(f.s.now()), "with nothing to probe as well")
}

func TestProbeResultRejectsBadInput(t *testing.T) {
	f := newProbeFixture(t, ProbeConfig{})
	_, err := f.s.ProbeResult(context.Background(), connect.NewRequest(&sniffarrv1.ProbeResultRequest{Infohash: []byte{1}}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = f.s.ProbeResult(context.Background(), connect.NewRequest(&sniffarrv1.ProbeResultRequest{Infohash: infohash(1)}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "no outcome")
	_, err = f.s.ProbeResult(context.Background(), connect.NewRequest(&sniffarrv1.ProbeResultRequest{Infohash: infohash(1), Downloaded: -1,
		Outcome: &sniffarrv1.ProbeResultRequest_Failure{Failure: sniffarrv1.ProbeFailure_PROBE_FAILURE_TIMEOUT}}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "negative bytes")
}

func TestProbeResultTrackLimit(t *testing.T) {
	ctx := context.Background()
	f := newProbeFixture(t, ProbeConfig{})
	file := func(n int) *sniffarrv1.ProbeResultRequest_File {
		tracks := make([]*sniffarrv1.Track, n)
		for i := range tracks {
			tracks[i] = &sniffarrv1.Track{Kind: sniffarrv1.TrackKind_TRACK_KIND_AUDIO, Language: "rus"}
		}
		return &sniffarrv1.ProbeResultRequest_File{File: &sniffarrv1.ProbedFile{Path: "a.mkv", Format: "matroska", Tracks: tracks}}
	}
	outcome := func(h store.Hash) string {
		var o string
		require.NoError(t, f.db.R.QueryRowContext(ctx, "SELECT p.outcome FROM probe_releases p JOIN torrent_releases t ON t.id = p.torrent_id WHERE t.infohash = ?", h[:]).Scan(&o))
		return o
	}
	within := f.insert(t, 1, "Alien.1979.1080p", 5)
	f.result(t, &sniffarrv1.ProbeResultRequest{Infohash: within[:], Outcome: file(container.TrackLimit)})
	require.Equal(t, store.ProbeTracks, outcome(within))
	r, err := f.st.Get(ctx, within)
	require.NoError(t, err)
	require.Len(t, r.Tracks, container.TrackLimit)

	over := f.insert(t, 2, "Heat.1995.1080p", 5)
	f.result(t, &sniffarrv1.ProbeResultRequest{Infohash: over[:], Downloaded: 1 << 20, Outcome: file(container.TrackLimit + 1)})
	require.Equal(t, store.ProbeUnknown, outcome(over), "a track list over the limit is one a probe cannot read")
	r, err = f.st.Get(ctx, over)
	require.NoError(t, err)
	require.Empty(t, r.Tracks)
	traffic, err := f.st.ProbeTraffic(ctx, f.today())
	require.NoError(t, err)
	require.Equal(t, int64(1<<20), traffic.Rx, "its download still counts")
}

func TestProbeUpload(t *testing.T) {
	ctx := context.Background()
	f := newProbeFixture(t, ProbeConfig{DailyBytes: 100 << 20})
	for _, n := range []int64{0, 3 << 20, 5 << 20} {
		_, err := f.s.ProbeUpload(ctx, connect.NewRequest(&sniffarrv1.ProbeUploadRequest{Uploaded: n}))
		require.NoError(t, err)
	}
	traffic, err := f.st.ProbeTraffic(ctx, f.today())
	require.NoError(t, err)
	require.Equal(t, store.ProbeTraffic{Tx: 8 << 20}, traffic, "upload counts against no budget")
	_, err = f.s.ProbeUpload(ctx, connect.NewRequest(&sniffarrv1.ProbeUploadRequest{Uploaded: -1}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}
