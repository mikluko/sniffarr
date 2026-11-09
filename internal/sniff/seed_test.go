package sniff

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
)

func TestPlan(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	cfg := SeederConfig{MaxBytes: 64, MaxAge: 7 * 24 * time.Hour, Idle: 15 * time.Minute}
	why := plan([]seedStatus{
		{added: ago(10 * 24 * time.Hour), downloaded: 16, uploaded: 20, active: now},
		{added: ago(8 * 24 * time.Hour), downloaded: 16, uploaded: 3, active: now},
		{added: ago(3 * time.Hour), downloaded: 40, uploaded: 0, active: now},
		{added: ago(2 * time.Hour), downloaded: 30, uploaded: 29, active: now},
		{added: ago(time.Hour), downloaded: 30, uploaded: 0, active: now},
	}, now, cfg)
	require.Equal(t, []string{seedRatio, seedAge, seedSize, "", ""}, why,
		"ratio first, then age, then the oldest beyond the cap: 30 + 30 fit in 64")

	tests := []struct {
		name string
		st   seedStatus
		want string
	}{
		{name: "fresh and needed", st: seedStatus{added: now, downloaded: 8, fresh: true, needed: true, active: now}},
		{name: "fresh and not needed", st: seedStatus{added: now, downloaded: 8, fresh: true, active: now}, want: seedUseless},
		{name: "fresh, not needed, at ratio 1", st: seedStatus{added: now, downloaded: 8, uploaded: 8, fresh: true, active: now}, want: seedRatio},
		{name: "not needed after the first check", st: seedStatus{added: ago(time.Hour), downloaded: 8, active: ago(14 * time.Minute)}},
		{name: "inactive for the idle window", st: seedStatus{added: ago(time.Hour), downloaded: 8, active: ago(15 * time.Minute)}, want: seedIdle},
		{name: "past the age cap and idle", st: seedStatus{added: ago(8 * 24 * time.Hour), downloaded: 8, active: ago(time.Hour)}, want: seedAge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, []string{tt.want}, plan([]seedStatus{tt.st}, now, cfg))
		})
	}

	small := SeederConfig{MaxBytes: 2, MaxAge: time.Hour, Idle: time.Minute}
	require.Equal(t, []string{"", ""}, plan([]seedStatus{{added: now, downloaded: 1, active: now}, {added: now, downloaded: 1, active: now}}, now, small))
	require.Equal(t, []string{seedIdle, ""}, plan([]seedStatus{{added: now, downloaded: 2, active: ago(time.Minute)}, {added: now, downloaded: 2, active: now}}, now, small),
		"an idle torrent's bytes leave room under the cap")
	require.Empty(t, plan(nil, now, small))
}

// fakeSeed is a seedTorrent some peer needs until unneeded is set.
type fakeSeed struct {
	up       atomic.Int64
	unneeded atomic.Bool
	removed  atomic.Bool
	ann      atomic.Int64
}

func (f *fakeSeed) uploaded() int64 { return f.up.Load() }
func (f *fakeSeed) needed() bool    { return !f.unneeded.Load() }
func (f *fakeSeed) announce()       { f.ann.Add(1) }
func (f *fakeSeed) remove() error   { f.removed.Store(true); return nil }

func TestSeederUselessAndIdle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	reader := sdkmetric.NewManualReader()
	cfg := SeederConfig{Dir: dir, Idle: 15 * time.Minute, Now: clock, Meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))}
	s := NewSeeder(cfg)
	useless, needed, uploading := &fakeSeed{}, &fakeSeed{}, &fakeSeed{}
	useless.unneeded.Store(true)
	require.NoError(t, s.Add(ctx, Hash{1}, useless, 100, nil))
	require.NoError(t, s.Add(ctx, Hash{2}, needed, 100, nil))
	require.NoError(t, s.Add(ctx, Hash{3}, uploading, 100, nil))
	s.check(ctx)
	require.True(t, useless.removed.Load(), "no peer needs it at the first check")
	require.False(t, needed.removed.Load())
	require.False(t, uploading.removed.Load())

	needed.unneeded.Store(true)
	uploading.unneeded.Store(true)
	for range 14 {
		now = now.Add(time.Minute)
		uploading.up.Add(1)
		s.check(ctx)
	}
	require.False(t, needed.removed.Load(), "needed 14 minutes ago")
	now = now.Add(time.Minute)
	s.check(ctx)
	require.True(t, needed.removed.Load(), "neither needed nor uploading for 15 minutes")
	require.False(t, uploading.removed.Load(), "uploaded 1 minute ago")

	restored := NewSeeder(SeederConfig{Dir: dir, Idle: 15 * time.Minute, Now: clock})
	again := &fakeSeed{}
	again.unneeded.Store(true)
	require.NoError(t, restored.Restore(func(Hash, []byte) (seedTorrent, error) { return again, nil }, keepAll(t)))
	restored.check(ctx)
	require.False(t, again.removed.Load(), "a restored torrent's peers have yet to connect")
	now = now.Add(15 * time.Minute)
	restored.check(ctx)
	require.True(t, again.removed.Load(), "the idle window starts at the restore")

	require.Equal(t, map[string]int64{seedUseless: 1, seedIdle: 1}, removals(t, reader))
}

// removals returns sniffarr.seed.removals by reason.
func removals(t *testing.T, reader sdkmetric.Reader) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	byReason := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "sniffarr.seed.removals" {
				continue
			}
			for _, p := range m.Data.(metricdata.Sum[int64]).DataPoints {
				r, _ := p.Attributes.Value("sniffarr.seed.reason")
				byReason[r.AsString()] = p.Value
			}
		}
	}
	return byReason
}

// keepAll is a seedDiscarder that fails the test when called.
func keepAll(t *testing.T) seedDiscarder {
	return func(h Hash, _ []byte) error {
		t.Errorf("discarded %x", h)
		return nil
	}
}

func TestSeederReAddIsJudgedAnew(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	s := NewSeeder(SeederConfig{Dir: t.TempDir(), Now: func() time.Time { return now }})
	first, second := &fakeSeed{}, &fakeSeed{}
	require.NoError(t, s.Add(ctx, Hash{1}, first, 100, nil))
	s.check(ctx)
	second.unneeded.Store(true)
	require.NoError(t, s.Add(ctx, Hash{1}, second, 50, nil))
	s.check(ctx)
	require.True(t, second.removed.Load(), "the torrent given last is the one judged, at the first check after it")
	require.False(t, first.removed.Load())
}

func TestSeederRatioAndRestore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	s := NewSeeder(SeederConfig{Dir: dir, MaxBytes: 1 << 30, MaxAge: 24 * time.Hour, Now: clock})
	a, b := &fakeSeed{}, &fakeSeed{}
	require.NoError(t, s.Add(ctx, Hash{1}, a, 100, []byte("info a")))
	now = now.Add(time.Minute)
	require.NoError(t, s.Add(ctx, Hash{2}, b, 200, []byte("info b")))
	require.True(t, s.Has(Hash{1}))
	n, bytes := s.Size()
	require.Equal(t, 2, n)
	require.EqualValues(t, 300, bytes)

	a.up.Store(100)
	b.up.Store(50)
	s.check(ctx)
	require.True(t, a.removed.Load(), "a reached ratio 1")
	require.False(t, b.removed.Load())
	require.False(t, s.Has(Hash{1}))
	_, err := os.Stat(filepath.Join(dir, "info", "0100000000000000000000000000000000000000"))
	require.ErrorIs(t, err, os.ErrNotExist, "a's info is deleted")

	restored := NewSeeder(SeederConfig{Dir: dir, MaxBytes: 1 << 30, MaxAge: 24 * time.Hour, Now: clock})
	b2 := &fakeSeed{}
	var opened []string
	require.NoError(t, restored.Restore(func(h Hash, info []byte) (seedTorrent, error) {
		opened = append(opened, string(info))
		return b2, nil
	}, keepAll(t)))
	require.Equal(t, []string{"info b"}, opened)
	b2.up.Store(149)
	restored.check(ctx)
	require.False(t, b2.removed.Load(), "50 before the restart and 149 since are short of 200")
	b2.up.Store(150)
	restored.check(ctx)
	require.True(t, b2.removed.Load(), "the upload before the restart counts")
}

func TestSeederEvicts(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	s := NewSeeder(SeederConfig{Dir: t.TempDir(), MaxBytes: 250, MaxAge: 24 * time.Hour, Now: func() time.Time { return now }})
	seeds := []*fakeSeed{{}, {}, {}}
	for i, f := range seeds {
		require.NoError(t, s.Add(ctx, Hash{byte(i + 1)}, f, 100, nil))
		now = now.Add(time.Hour)
	}
	s.check(ctx)
	require.True(t, seeds[0].removed.Load(), "the oldest goes when the cap is passed")
	require.False(t, seeds[1].removed.Load())
	now = now.Add(22 * time.Hour)
	s.check(ctx)
	require.True(t, seeds[1].removed.Load(), "a day old")
	require.False(t, seeds[2].removed.Load())
	s.announce()
	require.EqualValues(t, 1, seeds[2].ann.Load())
}

func TestSeederRestoreForgetsWhatFailsToOpen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := NewSeeder(SeederConfig{Dir: dir})
	require.NoError(t, s.Add(ctx, Hash{1}, &fakeSeed{}, 10, []byte("info")))
	restored := NewSeeder(SeederConfig{Dir: dir})
	require.NoError(t, restored.Restore(func(Hash, []byte) (seedTorrent, error) { return nil, os.ErrInvalid }, keepAll(t)))
	require.False(t, restored.Has(Hash{1}))
	require.NoError(t, NewSeeder(SeederConfig{Dir: t.TempDir()}).Restore(nil, nil), "no state file restores none")
}

func TestSeederRestoreDiscardsWhatPlanRemoves(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	hash := func(n byte) string { return hex.EncodeToString([]byte{n, 19: 0}) }
	info := func(n byte) []byte { return []byte{'i', n} }
	states := []seedState{
		{Infohash: hash(4), Added: now.Add(-time.Hour), Downloaded: 100, Uploaded: 10},
		{Infohash: hash(1), Added: now.Add(-25 * time.Hour), Downloaded: 100},
		{Infohash: hash(2), Added: now.Add(-3 * time.Hour), Downloaded: 100, Uploaded: 100},
		{Infohash: hash(3), Added: now.Add(-2 * time.Hour), Downloaded: 200},
		{Infohash: hash(6), Added: now.Add(-48 * time.Hour), Downloaded: 10},
	}
	b, err := json.Marshal(states)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "seeds.json"), b, 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "info"), 0o755))
	for _, n := range []byte{1, 2, 3, 4} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "info", hash(n)), info(n), 0o644))
	}

	reader := sdkmetric.NewManualReader()
	s := NewSeeder(SeederConfig{Dir: dir, MaxBytes: 250, MaxAge: 24 * time.Hour, Idle: 15 * time.Minute,
		Now: func() time.Time { return now }, Meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))})
	kept := &fakeSeed{}
	kept.unneeded.Store(true)
	opened := map[Hash][]byte{}
	discarded := map[Hash][]byte{}
	require.NoError(t, s.Restore(func(h Hash, info []byte) (seedTorrent, error) {
		opened[h] = info
		return kept, nil
	}, func(h Hash, info []byte) error {
		discarded[h] = info
		return nil
	}))

	require.Equal(t, map[Hash][]byte{{4}: info(4)}, opened, "only the kept one is opened")
	require.Equal(t, map[Hash][]byte{{1}: info(1), {2}: info(2), {3}: info(3), {6}: nil}, discarded,
		"past its age, at ratio 1, the oldest beyond the cap, and one without info")
	require.Equal(t, map[string]int64{seedAge: 2, seedRatio: 1, seedSize: 1}, removals(t, reader))
	for _, n := range []byte{1, 2, 3} {
		_, err := os.Stat(filepath.Join(dir, "info", hash(n)))
		require.ErrorIs(t, err, os.ErrNotExist, "the info of %d is deleted", n)
	}
	require.FileExists(t, filepath.Join(dir, "info", hash(4)))

	b, err = os.ReadFile(filepath.Join(dir, "seeds.json"))
	require.NoError(t, err)
	var saved []seedState
	require.NoError(t, json.Unmarshal(b, &saved))
	require.Equal(t, []seedState{states[0]}, saved)

	s.check(context.Background())
	require.False(t, kept.removed.Load(), "restored, its peers have yet to connect")
}

// uploadIngest records what ProbeUpload is told, refusing while down.
type uploadIngest struct {
	sniffarrv1connect.UnimplementedIngestServiceHandler
	mu       sync.Mutex
	uploaded []int64
	down     bool
}

func (u *uploadIngest) ProbeUpload(_ context.Context, req *connect.Request[sniffarrv1.ProbeUploadRequest]) (*connect.Response[sniffarrv1.ProbeUploadResponse], error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.down {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("down"))
	}
	u.uploaded = append(u.uploaded, req.Msg.GetUploaded())
	return connect.NewResponse(&sniffarrv1.ProbeUploadResponse{}), nil
}

func (u *uploadIngest) setDown(down bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.down = down
}

func TestSeederReportsUpload(t *testing.T) {
	ctx := context.Background()
	ingest := &uploadIngest{}
	mux := http.NewServeMux()
	mux.Handle(sniffarrv1connect.NewIngestServiceHandler(ingest))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := SeederConfig{Dir: t.TempDir(), MaxBytes: 1 << 30, MaxAge: 24 * time.Hour,
		Ingest: sniffarrv1connect.NewIngestServiceClient(srv.Client(), srv.URL)}
	s := NewSeeder(cfg)
	a, b := &fakeSeed{}, &fakeSeed{}
	require.NoError(t, s.Add(ctx, Hash{1}, a, 1000, nil))
	require.NoError(t, s.Add(ctx, Hash{2}, b, 1000, nil))
	s.report(ctx, s.check(ctx))
	require.Empty(t, ingest.uploaded, "nothing uploaded, nothing reported")
	a.up.Store(100)
	b.up.Store(50)
	s.report(ctx, s.check(ctx))
	require.Equal(t, []int64{150}, ingest.uploaded)

	ingest.setDown(true)
	a.up.Store(130)
	s.report(ctx, s.check(ctx))
	ingest.setDown(false)
	b.up.Store(70)
	s.report(ctx, s.check(ctx))
	require.Equal(t, []int64{150, 50}, ingest.uploaded, "what a refused report held goes with the next")

	restored := NewSeeder(cfg)
	seeds := map[Hash]*fakeSeed{}
	require.NoError(t, restored.Restore(func(h Hash, _ []byte) (seedTorrent, error) {
		seeds[h] = &fakeSeed{}
		return seeds[h], nil
	}, keepAll(t)))
	seeds[Hash{1}].up.Store(10)
	restored.report(ctx, restored.check(ctx))
	require.Equal(t, []int64{150, 50, 10}, ingest.uploaded, "the upload before the restart is not reported again")
}
