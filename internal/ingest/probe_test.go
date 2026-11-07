package ingest

import (
	"context"
	"log/slog"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/container"
	"github.com/mikluko/sniffarr/internal/database"
	"github.com/mikluko/sniffarr/internal/rules"
	"github.com/mikluko/sniffarr/internal/store"
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
	s   *Service
	st  *store.Store
	db  *database.DB
	now time.Time
}

func newProbeFixture(t *testing.T, cfg ProbeConfig) *probeFixture {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "test.db"), database.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	st, err := store.New(ctx, db, store.Options{Languages: []string{"ru"}})
	require.NoError(t, err)
	q, err := store.NewTasks(ctx, db, store.TaskOptions{})
	require.NoError(t, err)
	f := &probeFixture{st: st, db: db, now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	f.s = &Service{Store: st, Tasks: q, Log: slog.New(slog.DiscardHandler), Probe: cfg, Now: func() time.Time { return f.now }}
	return f
}

func (f *probeFixture) insert(t *testing.T, b byte, name string, peers int64) store.Hash {
	t.Helper()
	ctx := context.Background()
	h := store.Hash{b}
	_, _, err := f.st.Insert(ctx, store.Torrent{Hash: h, Name: name, Size: 8 << 30, Files: []rules.File{{Path: name + ".mkv", Size: 8 << 30}},
		Discovery: store.Sample, SeenAt: f.now, Kind: "movie", KindLayer: store.LayerRules})
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

func TestProbeTaskBudget(t *testing.T) {
	ctx := context.Background()
	f := newProbeFixture(t, ProbeConfig{DailyBytes: 100 << 20, SearchReserve: 50, MaxBytes: 40 << 20, Lease: 10 * time.Minute})
	a := f.insert(t, 1, "Alien.1979.1080p", 30)
	b := f.insert(t, 2, "Brazil.1985.1080p", 20)
	c := f.insert(t, 3, "Coraline.2009.2160p", 10)

	first := f.task(t)
	require.Equal(t, a[:], first.GetInfohash(), "most peers first")
	require.EqualValues(t, 40<<20, first.GetMaxBytes())
	second := f.task(t)
	require.Equal(t, b[:], second.GetInfohash(), "a leased release is not handed out twice")
	require.Nil(t, f.task(t), "background probes stop at half the budget, counting what the leases may download")

	require.NoError(t, f.st.WantProbe(ctx, []store.Hash{c}, store.ProbeWant{T: "movie", Query: "coraline"}, f.now))
	wanted := f.task(t)
	require.Equal(t, c[:], wanted.GetInfohash(), "a searched release uses the reserve")
	require.Nil(t, f.task(t), "the whole budget is held by leases")

	f.result(t, &sniffarrv1.ProbeResultRequest{Infohash: a[:], Downloaded: 16 << 20, Outcome: &sniffarrv1.ProbeResultRequest_File{File: &sniffarrv1.ProbedFile{
		Path: "Alien.1979.1080p.mkv", Format: "matroska", Tracks: []*sniffarrv1.Track{
			{Kind: sniffarrv1.TrackKind_TRACK_KIND_VIDEO, Codec: "V_MPEG4/ISO/AVC", Width: 1920, Height: 800},
			{Kind: sniffarrv1.TrackKind_TRACK_KIND_AUDIO, Language: "rus", Name: "MVO", Codec: "A_AC3", Channels: 6},
		}}}})
	traffic, err := f.st.ProbeTraffic(ctx, "2026-11-01")
	require.NoError(t, err)
	require.Equal(t, store.ProbeTraffic{Rx: 16 << 20}, traffic)
	r, err := f.st.Get(ctx, a)
	require.NoError(t, err)
	require.Len(t, r.Tracks, 2)
	require.Equal(t, store.Track{Kind: "audio", Language: "rus", Name: "MVO", Codec: "A_AC3", Channels: 6}, r.Tracks[1])
	require.Equal(t, store.LayerProbe, r.Audio[0].Layer)

	f.result(t, &sniffarrv1.ProbeResultRequest{Infohash: b[:], Outcome: &sniffarrv1.ProbeResultRequest_Failure{Failure: sniffarrv1.ProbeFailure_PROBE_FAILURE_TIMEOUT}})
	f.result(t, &sniffarrv1.ProbeResultRequest{Infohash: c[:], Downloaded: 1 << 20, Outcome: &sniffarrv1.ProbeResultRequest_Failure{Failure: sniffarrv1.ProbeFailure_PROBE_FAILURE_TIMEOUT}})
	require.Nil(t, f.task(t), "a probed release is not probed again, a failed one waits out its backoff")

	f.now = f.now.Add(DefaultProbeBackoff)
	require.Equal(t, c[:], f.task(t).GetInfohash(), "the searched release first")
	f.result(t, &sniffarrv1.ProbeResultRequest{Infohash: c[:], Outcome: &sniffarrv1.ProbeResultRequest_Failure{Failure: sniffarrv1.ProbeFailure_PROBE_FAILURE_UNKNOWN}})
	require.Equal(t, b[:], f.task(t).GetInfohash())
	f.result(t, &sniffarrv1.ProbeResultRequest{Infohash: b[:], Outcome: &sniffarrv1.ProbeResultRequest_Failure{Failure: sniffarrv1.ProbeFailure_PROBE_FAILURE_NO_PEERS}})
	n, _, err := f.st.RequestProbe(ctx, []store.Hash{a}, f.now)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, a[:], f.task(t).GetInfohash(), "a requested release is probed again, whatever its last probe")

	require.Equal(t, []string{"fetched/stored", "probe_leased/background", "probed/tracks", "probe_wanted/request", "probe_leased/request"}, journal(t, f.st, a))
	require.Equal(t, []string{"fetched/stored", "probe_wanted/search", "probe_leased/search", "probed/timeout", "probe_leased/retry", "probed/unknown"}, journal(t, f.st, c),
		"a searched release probed again is a retry")
	require.Equal(t, []string{"fetched/stored", "probe_leased/background", "probed/timeout", "probe_leased/retry", "probed/no_peers"}, journal(t, f.st, b),
		"no peers and a timeout are told apart")
	got, err := f.st.Journal(ctx, b, 10)
	require.NoError(t, err)
	until := func(e store.JournalEntry) string { return strconv.FormatInt(e.At.Add(10*time.Minute).Unix(), 10) }
	require.JSONEq(t, `{"attempt":1,"max_bytes":41943040,"lease_until":`+until(got[1])+`}`, got[1].Detail, "a's lease, held while b is handed out, is not b's")
	require.JSONEq(t, `{"attempt":2,"max_bytes":41943040,"lease_until":`+until(got[3])+`}`, got[3].Detail, "nor what a's and c's probes downloaded today")
}

func TestProbeLeaseExpires(t *testing.T) {
	f := newProbeFixture(t, ProbeConfig{DailyBytes: 50 << 20, MaxBytes: 50 << 20, Lease: 10 * time.Minute})
	a := f.insert(t, 1, "Alien.1979.1080p", 30)
	f.insert(t, 2, "Brazil.1985.1080p", 20)
	require.Equal(t, a[:], f.task(t).GetInfohash())
	require.Nil(t, f.task(t))
	f.now = f.now.Add(10 * time.Minute)
	require.Equal(t, a[:], f.task(t).GetInfohash(), "an expired lease frees its release and its reservation")
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
	traffic, err := f.st.ProbeTraffic(ctx, "2026-11-01")
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
	traffic, err := f.st.ProbeTraffic(ctx, "2026-11-01")
	require.NoError(t, err)
	require.Equal(t, store.ProbeTraffic{Tx: 8 << 20}, traffic, "upload counts against no budget")
	_, err = f.s.ProbeUpload(ctx, connect.NewRequest(&sniffarrv1.ProbeUploadRequest{Uploaded: -1}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}
