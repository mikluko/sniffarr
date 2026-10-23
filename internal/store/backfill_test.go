package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBackfillResumes(t *testing.T) {
	ctx := context.Background()
	s, db := openAt(t, filepath.Join(t.TempDir(), "test.db"))
	defer db.Close()
	var ids []int64
	for i := byte(1); i <= 7; i++ {
		tr := torrent(i, fmt.Sprintf("Film.%c.Rus", 'A'+i), "film.mkv")
		tr.Audio = []Audio{{Language: "ru", Marker: true}}
		_, _, err := s.Insert(ctx, tr)
		require.NoError(t, err)
		r, err := s.Get(ctx, hash(i))
		require.NoError(t, err)
		ids = append(ids, r.ID)
	}
	s.backfillBatch = 2

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var first []Backfilled
	err := s.Backfill(cctx, func(b Backfilled) {
		first = append(first, b)
		cancel()
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, []Backfilled{{Name: "release features", Rows: 2}}, first)
	after, err := s.position(ctx, featuresFilled)
	require.NoError(t, err)
	require.Equal(t, ids[1], after, "the last row of the committed batch")

	got := map[string]Backfilled{}
	require.NoError(t, s.Backfill(ctx, func(b Backfilled) { got[b.Name] = b }))
	require.Equal(t, Backfilled{Name: "release features", Rows: 5, Done: true}, got["release features"], "resumed after the saved row")
	require.Equal(t, Backfilled{Name: "audio marks ru", Rows: 7, Done: true}, got["audio marks ru"])
	for _, flag := range []string{featuresFilled, marksFilled + "ru"} {
		done, err := s.flag(ctx, flag)
		require.NoError(t, err)
		require.True(t, done)
		after, err := s.position(ctx, flag)
		require.NoError(t, err)
		require.Zero(t, after, "the position goes with the flag")
	}
	r, err := s.Get(ctx, hash(7))
	require.NoError(t, err)
	require.Equal(t, "Rus", r.Audio[0].Mark)

	var again []Backfilled
	require.NoError(t, s.Backfill(ctx, func(b Backfilled) { again = append(again, b) }))
	require.Empty(t, again, "done backfills do not run again")
}
