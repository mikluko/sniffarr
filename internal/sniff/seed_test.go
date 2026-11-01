package sniff

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPlan(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	why := plan([]seedStatus{
		{added: ago(10 * 24 * time.Hour), downloaded: 16, uploaded: 20},
		{added: ago(8 * 24 * time.Hour), downloaded: 16, uploaded: 3},
		{added: ago(3 * time.Hour), downloaded: 40, uploaded: 0},
		{added: ago(2 * time.Hour), downloaded: 30, uploaded: 29},
		{added: ago(time.Hour), downloaded: 30, uploaded: 0},
	}, now, 64, 7*24*time.Hour)
	require.Equal(t, []string{seedRatio, seedAge, seedSize, "", ""}, why,
		"ratio first, then age, then the oldest beyond the cap: 30 + 30 fit in 64")

	require.Equal(t, []string{"", ""}, plan([]seedStatus{{added: now, downloaded: 1}, {added: now, downloaded: 1}}, now, 2, time.Hour))
	require.Empty(t, plan(nil, now, 0, time.Hour))
}

type fakeSeed struct {
	up      atomic.Int64
	removed atomic.Bool
	ann     atomic.Int64
}

func (f *fakeSeed) uploaded() int64 { return f.up.Load() }
func (f *fakeSeed) announce()       { f.ann.Add(1) }
func (f *fakeSeed) remove() error   { f.removed.Store(true); return nil }

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
	}))
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
	require.NoError(t, restored.Restore(func(Hash, []byte) (seedTorrent, error) { return nil, os.ErrInvalid }))
	require.False(t, restored.Has(Hash{1}))
	require.NoError(t, NewSeeder(SeederConfig{Dir: t.TempDir()}).Restore(nil), "no state file restores none")
}
