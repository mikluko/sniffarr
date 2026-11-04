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
	probed := torrent(8, "Severance.S02.1080p", "Severance.S02E01.mkv")
	probed.Kind, probed.KindLayer = "series", LayerRules
	_, _, err := s.Insert(ctx, probed)
	require.NoError(t, err)
	require.NoError(t, s.ProbeDone(ctx, probed.Hash, Probe{Outcome: ProbeTracks, Tracks: []Track{{Kind: "audio", Language: "rus", Name: "MVO LostFilm"}}}, t0))
	_, err = s.w.ExecContext(ctx, "UPDATE torrent_audio SET studios = NULL WHERE torrent_id = (SELECT id FROM torrent_releases WHERE infohash = ?)", probed.Hash[:])
	require.NoError(t, err, "probed before track names were read for studios")
	s.backfillBatch = 2

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var first []Backfilled
	err = s.Backfill(cctx, func(b Backfilled) {
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
	require.Equal(t, Backfilled{Name: "release features", Rows: 6, Done: true}, got["release features"], "resumed after the saved row")
	require.Equal(t, Backfilled{Name: "audio marks ru", Rows: 7, Done: true}, got["audio marks ru"])
	require.Equal(t, Backfilled{Name: "probe studios ru", Rows: 1, Done: true}, got["probe studios ru"])
	require.Equal(t, []string{"LostFilm"}, mustGet(t, s, probed.Hash).Audio[0].Studios, "the studio the probed track names")
	for _, flag := range []string{featuresFilled, marksFilled + "ru", probeStudiosFilled + "ru"} {
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
