package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

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

func TestBackfillLetterO(t *testing.T) {
	ctx := context.Background()
	s, db := openAt(t, filepath.Join(t.TempDir(), "test.db"))
	defer db.Close()
	episode := torrent(1, "Show.S01E02.1O8Op.WEB-DL", "Show.S01E02.1O8Op.WEB-DL.mkv")
	episode.Season, episode.Episode = 1, 2
	movie := torrent(2, "Marshall.2O17.P.BDRip.1080p", "Marshall.2O17.P.BDRip.1080p.mkv")
	movie.Quality = "1080p"
	for _, tr := range []Torrent{episode, movie} {
		_, _, err := s.Insert(ctx, tr)
		require.NoError(t, err)
	}
	require.NoError(t, s.Backfill(ctx, nil))
	r, err := s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.Equal(t, "1080p", r.Quality, "a release stored with an episode and no quality")
	r, err = s.Get(ctx, hash(2))
	require.NoError(t, err)
	require.Equal(t, 2017, r.Year, "a release stored with a quality and no year")
}

func TestBackfillSources(t *testing.T) {
	ctx := context.Background()
	s, db := openAt(t, filepath.Join(t.TempDir(), "test.db"))
	defer db.Close()
	for _, tr := range []Torrent{
		torrent(1, "Movie.2026.2160p.UHD.BluRay.REMUX", "Movie.mkv"),
		torrent(2, "Heart.of.the.Beast.2026.720p.AMZN.WEB-DLRip", "Heart.mkv"),
		torrent(3, "Movie.2026.1080p", "Movie.mkv"),
		torrent(4, "Movie.2026.WEB-DL", "Movie.mkv"),
	} {
		_, _, err := s.Insert(ctx, tr)
		require.NoError(t, err)
	}
	h4 := hash(4)
	_, err := s.w.ExecContext(ctx, "UPDATE torrent_releases SET source = 'encode', source_confidence = 0.9 WHERE infohash = ?", h4[:])
	require.NoError(t, err)

	got := map[string]Backfilled{}
	require.NoError(t, s.Backfill(ctx, func(b Backfilled) { got[b.Name] = b }))
	require.Equal(t, Backfilled{Name: "release sources", Rows: 3, Done: true}, got["release sources"])
	for i, want := range map[byte]string{1: "remux", 2: "web", 3: "", 4: "encode"} {
		r, err := s.Get(ctx, hash(i))
		require.NoError(t, err)
		require.Equal(t, want, r.Source, r.Name)
	}
	r, err := s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.InDelta(t, 1, r.SourceConfidence, 0)
	r, err = s.Get(ctx, hash(4))
	require.NoError(t, err)
	require.InDelta(t, 0.9, r.SourceConfidence, 1e-9, "a stored source stays")
}

func TestBackfillAudioCodes(t *testing.T) {
	ctx := context.Background()
	s, db := openAt(t, filepath.Join(t.TempDir(), "test.db"))
	defer db.Close()
	coded := torrent(1, "Bad.Apples.2026.P.WEB-DL.1O8Op.mkv", "Bad.Apples.2026.P.WEB-DL.1O8Op.mkv")
	coded.Audio = []Audio{{Language: "ru", Probability: new(0.03), Layer: LayerClassifier}}
	plain := torrent(2, "Plan.B.2001.WEB-DL", "Plan.B.2001.WEB-DL.mkv")
	plain.Audio = []Audio{{Language: "ru", Probability: new(0.03), Layer: LayerClassifier}}
	for _, tr := range []Torrent{coded, plain} {
		_, _, err := s.Insert(ctx, tr)
		require.NoError(t, err)
	}
	got := map[string]Backfilled{}
	require.NoError(t, s.Backfill(ctx, func(b Backfilled) { got[b.Name] = b }))
	require.True(t, got["audio codes ru"].Done)
	a := mustGet(t, s, hash(1)).Audio[0]
	require.True(t, a.Marker)
	require.Equal(t, "P", a.Mark)
	require.Equal(t, 1.0, *a.Probability)
	require.Equal(t, LayerRules, a.Layer)
	a = mustGet(t, s, hash(2)).Audio[0]
	require.False(t, a.Marker, "a code alone is no marker")
	require.Equal(t, 0.03, *a.Probability)
}

func TestBackfillSubtitleMarks(t *testing.T) {
	ctx := context.Background()
	s, db := openAt(t, filepath.Join(t.TempDir(), "test.db"))
	defer db.Close()
	marked := func(b byte, mark, layer, name string, paths ...string) Torrent {
		tr := torrent(b, name, paths...)
		tr.Audio = []Audio{{Language: "ru", Marker: true, Mark: mark, Probability: new(1.0), Layer: layer}}
		return tr
	}
	for _, tr := range []Torrent{
		marked(1, "Rus", LayerRules, "Alien.1979.DVDRip", "Alien.1979.DVDRip.avi", "Subs [Rus]/Alien.1979.DVDRip.srt"),
		marked(2, "Rus", LayerRules, "Alien.1979.DVDRip", "Alien.1979.DVDRip.avi", "Rus/Alien.ac3", "Rus/Alien.srt"),
		marked(3, "Rus", LayerProbe, "Alien.1979.DVDRip", "Alien.1979.DVDRip.avi", "Alien.Rus.srt"),
		marked(4, "LostFilm", LayerRules, "Alien.1979.DVDRip.Rus", "Alien.1979.DVDRip.avi", "LostFilm/Alien.srt"),
	} {
		_, _, err := s.Insert(ctx, tr)
		require.NoError(t, err)
	}
	got := map[string]Backfilled{}
	require.NoError(t, s.Backfill(ctx, func(b Backfilled) { got[b.Name] = b }))
	require.True(t, got["subtitle marks ru"].Done)
	a := mustGet(t, s, hash(1)).Audio[0]
	require.False(t, a.Marker, "a word in a subtitle path alone")
	require.Empty(t, a.Mark)
	require.Nil(t, a.Probability)
	require.Empty(t, a.Layer)
	a = mustGet(t, s, hash(2)).Audio[0]
	require.True(t, a.Marker, "a word in an audio path too")
	require.Equal(t, 1.0, *a.Probability)
	a = mustGet(t, s, hash(3)).Audio[0]
	require.True(t, a.Marker, "a probe layer stays")
	require.Equal(t, LayerProbe, a.Layer)
	a = mustGet(t, s, hash(4)).Audio[0]
	require.True(t, a.Marker)
	require.Equal(t, "Rus", a.Mark, "the mark of the name over a studio in a subtitle path")
}

func TestBackfillProbeOverMarkers(t *testing.T) {
	ctx := context.Background()
	s, db := openAt(t, filepath.Join(t.TempDir(), "test.db"))
	defer db.Close()
	only := torrent(1, "Resident.Evil.2026.WEB-DL.2160p.rus.sub", "Resident.Evil.2026.WEB-DL.2160p.mkv")
	separate := torrent(2, "Alien.1979.1080p.Rus", "Alien.1979.1080p.mkv", "Rus Sound/Alien.1979.Rus.ac3")
	undetermined := torrent(4, "Heat.1995.1080p.Rus", "Heat.1995.1080p.mkv")
	for _, tr := range []Torrent{only, separate, undetermined} {
		tr.Audio = []Audio{{Language: "ru", Marker: true, Mark: "Rus", Probability: new(1.0), Layer: LayerRules}}
		_, _, err := s.Insert(ctx, tr)
		require.NoError(t, err)
	}
	for _, tr := range []Torrent{only, separate} {
		require.NoError(t, s.ProbeDone(ctx, tr.Hash, Probe{Outcome: ProbeTracks, Tracks: []Track{{Kind: "audio", Language: "eng"}}}, t0))
	}
	require.NoError(t, s.ProbeDone(ctx, undetermined.Hash, Probe{Outcome: ProbeTracks, Tracks: []Track{{Kind: "audio"}}}, t0))
	_, err := s.w.ExecContext(ctx, "UPDATE torrent_audio SET probability = 1, layer = ?", LayerRules)
	require.NoError(t, err, "probed while a marker outranked a probe")

	got := map[string]Backfilled{}
	require.NoError(t, s.Backfill(ctx, func(b Backfilled) { got[b.Name] = b }))
	require.Equal(t, Backfilled{Name: "probe over markers ru", Rows: 3, Done: true}, got["probe over markers ru"])
	requireAudio(t, mustGet(t, s, only.Hash), 0, LayerProbe)
	requireAudio(t, mustGet(t, s, separate.Hash), 1, LayerRules)
	requireAudio(t, mustGet(t, s, undetermined.Hash), 1, LayerRules)
}

func TestBackfillProbeAttempts(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	read := insertProbeable(t, s, 1, "Alien.1979.1080p", 5, "Alien.1979.1080p.mkv")
	unknown := insertProbeable(t, s, 2, "Heat.1995.1080p", 5, "Heat.1995.1080p.mkv")
	failed := insertProbeable(t, s, 4, "Brazil.1985.1080p", 5, "Brazil.1985.1080p.mkv")
	require.NoError(t, s.ProbeDone(ctx, read, Probe{Outcome: ProbeTracks}, t0))
	require.NoError(t, s.ProbeDone(ctx, unknown, Probe{Outcome: ProbeUnknown}, t0))
	for range 2 {
		_, _, err := s.ProbeFailed(ctx, failed, ProbeTimeout, 0, 1, t0, 3, time.Hour)
		require.NoError(t, err)
	}
	_, err := s.w.ExecContext(ctx, "UPDATE probe_releases SET attempts = 2 WHERE outcome != ?", ProbeFailed)
	require.NoError(t, err, "read while a read kept the failed attempts")

	got := map[string]Backfilled{}
	require.NoError(t, s.Backfill(ctx, func(b Backfilled) { got[b.Name] = b }))
	require.Equal(t, Backfilled{Name: "probe attempts", Rows: 2, Done: true}, got["probe attempts"])
	for h, want := range map[Hash]int{read: 0, unknown: 0, failed: 2} {
		var n int
		require.NoError(t, s.r.QueryRowContext(ctx, "SELECT p.attempts FROM probe_releases p JOIN torrent_releases t ON t.id = p.torrent_id WHERE t.infohash = ?", h[:]).Scan(&n))
		require.Equal(t, want, n, "a failed probe keeps its attempts")
	}
	final, _, err := s.ProbeFailed(ctx, read, ProbeTimeout, 0, 1, t0, 3, time.Hour)
	require.NoError(t, err)
	require.False(t, final, "the first failure after a read")
}
