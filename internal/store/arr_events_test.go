package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// grab is a Radarr grab of Alien, TMDB 348, with the infohash of hash(b).
func grab(b byte, key string, at time.Time) ArrEvent {
	h := hash(b)
	return ArrEvent{App: AppRadarr, Event: EventGrab, Key: key, Hash: &h, Type: TypeMovie, TMDBID: 348,
		IDs: []ExternalID{{Provider: ProviderIMDb, ID: "tt0078748"}}, Title: "Alien.1979.1080p.BluRay", Indexer: "tracker", Size: 1 << 30, At: at}
}

func titled(b byte, name, key string) Torrent {
	x := torrent(b, name)
	x.TitleKey, x.Kind, x.KindLayer = key, "movie", LayerRules
	return x
}

func match(t *testing.T, s *Store, key string) Match {
	t.Helper()
	m, err := s.matches(context.Background(), []string{key})
	require.NoError(t, err)
	return m[key]
}

func TestRecordArrStoredRelease(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for _, x := range []Torrent{titled(1, "Alien.1979.1080p.BluRay", "y:alien 1979"), titled(2, "Alien.1979.2160p", "y:alien 1979")} {
		_, _, err := s.Insert(ctx, x)
		require.NoError(t, err)
	}
	other, _, err := s.RecordMatch(ctx, "y:alien 1979", Found{Hit: &Hit{Type: TypeMovie, TMDBID: 999, Title: "Alien Resurrection"}, Confidence: 0.8, Layer: LayerJev}, t0)
	require.NoError(t, err)

	r, err := s.RecordArr(ctx, grab(1, "abc", t0), t0)
	require.NoError(t, err)
	require.True(t, r.New)
	require.True(t, r.Stored)
	require.NotZero(t, r.Work)
	require.NotEqual(t, other, r.Work)

	for _, b := range []byte{1, 2} {
		rel, err := s.Get(ctx, hash(b))
		require.NoError(t, err)
		require.Equal(t, r.Work, rel.Work, "every release of the title key takes the work")
	}
	m := match(t, s, "y:alien 1979")
	require.Equal(t, LayerArr, m.Layer, "arr replaces a classifier match")
	require.Equal(t, 1.0, m.Confidence)
	require.Equal(t, r.Work, m.Work.ID)
	require.Equal(t, []ExternalID{{Provider: ProviderIMDb, ID: "tt0078748"}}, m.Work.IDs)
	require.True(t, m.Work.DetailsAt.IsZero(), "the work waits for the details backfill")
	due, err := s.WithoutDetails(ctx, 10)
	require.NoError(t, err)
	require.Contains(t, due, Due{Work: r.Work, Type: TypeMovie, TMDBID: 348})

	for _, f := range []Found{{Hit: &Hit{Type: TypeMovie, TMDBID: 999}, Confidence: 1, Layer: LayerTMDB}, {}, {Error: "tmdb refuses"}} {
		_, _, err := s.RecordMatch(ctx, "y:alien 1979", f, t0.Add(time.Hour))
		require.NoError(t, err)
		m := match(t, s, "y:alien 1979")
		require.Equal(t, LayerArr, m.Layer, "a later match of another layer leaves the arr match")
		require.Equal(t, r.Work, m.Work.ID)
		rel, err := s.Get(ctx, hash(2))
		require.NoError(t, err)
		require.Equal(t, r.Work, rel.Work)
	}
}

func TestRecordArrPendingRelease(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	r, err := s.RecordArr(ctx, grab(1, "abc", t0), t0)
	require.NoError(t, err)
	require.False(t, r.Stored)
	require.NotZero(t, r.Work)

	_, _, err = s.Insert(ctx, titled(2, "Alien.1979.720p", "y:alien 1979"))
	require.NoError(t, err)
	rel, err := s.Get(ctx, hash(2))
	require.NoError(t, err)
	require.Zero(t, rel.Work, "a torrent no event names keeps no work")

	_, _, err = s.Insert(ctx, titled(1, "Alien.1979.1080p.BluRay", "y:alien 1979"))
	require.NoError(t, err)
	for _, b := range []byte{1, 2} {
		rel, err := s.Get(ctx, hash(b))
		require.NoError(t, err)
		require.Equal(t, r.Work, rel.Work, "the stored torrent of the event takes the work, and so its title key")
	}
	require.Equal(t, LayerArr, match(t, s, "y:alien 1979").Layer)
}

func TestRecordArrDedupe(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	e := grab(1, "abc", t0)
	e.TMDBID = 0
	r, err := s.RecordArr(ctx, e, t0)
	require.NoError(t, err)
	require.True(t, r.New)
	require.Zero(t, r.Work)

	e = grab(1, "abc", t0.Add(time.Minute))
	r, err = s.RecordArr(ctx, e, t0.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, r.New, "one app, event and key is one event")
	require.NotZero(t, r.Work)

	imp := grab(1, "abc", t0)
	imp.Event = EventImport
	r2, err := s.RecordArr(ctx, imp, t0)
	require.NoError(t, err)
	require.True(t, r2.New, "an import is another event")

	var n int
	var work int64
	require.NoError(t, s.r.QueryRowContext(ctx, "SELECT count(*), max(work) FROM arr_events WHERE event = ?", EventGrab).Scan(&n, &work))
	require.Equal(t, 1, n)
	require.Equal(t, r.Work, work, "a duplicate fills in the work")
}

func TestGrabsWeek(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	now := time.Now()
	var work int64
	for i, e := range []ArrEvent{grab(1, "a", now.Add(-24*time.Hour)), grab(2, "b", now.Add(-6*24*time.Hour)), grab(3, "c", now.Add(-8*24*time.Hour))} {
		if i == 1 {
			e.Hash = nil
		}
		r, err := s.RecordArr(ctx, e, now)
		require.NoError(t, err)
		work = r.Work
	}
	imp := grab(1, "a", now)
	imp.Event = EventImport
	_, err := s.RecordArr(ctx, imp, now)
	require.NoError(t, err)
	tv := ArrEvent{App: AppSonarr, Event: EventGrab, Key: "x", Type: TypeTV, TMDBID: 348, At: now}
	_, err = s.RecordArr(ctx, tv, now)
	require.NoError(t, err)

	sums, err := s.Summaries(ctx, []int64{work})
	require.NoError(t, err)
	require.Equal(t, int64(2), sums[work].GrabsWeek, "grabs of the last 7 days, with or without an infohash; imports and other works do not count")
	require.Zero(t, sums[work].Releases)
}

func TestArrLookups(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	_, ok, err := s.LatestArr(ctx, AppSonarr, EventGrab)
	require.NoError(t, err)
	require.False(t, ok)
	_, err = s.TMDBByExternal(ctx, TypeTV, ProviderTVDB, "81189")
	require.ErrorIs(t, err, ErrNotFound)

	for i, at := range []time.Time{t0, t0.Add(time.Hour)} {
		_, err := s.RecordArr(ctx, ArrEvent{App: AppSonarr, Event: EventGrab, Key: string(rune('a' + i)), Type: TypeTV, TMDBID: 1396,
			IDs: []ExternalID{{Provider: ProviderTVDB, ID: "81189"}}, At: at}, at)
		require.NoError(t, err)
	}
	latest, ok, err := s.LatestArr(ctx, AppSonarr, EventGrab)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, t0.Add(time.Hour), latest)
	id, err := s.TMDBByExternal(ctx, TypeTV, ProviderTVDB, "81189")
	require.NoError(t, err)
	require.Equal(t, 1396, id)
	_, err = s.TMDBByExternal(ctx, TypeMovie, ProviderTVDB, "81189")
	require.ErrorIs(t, err, ErrNotFound)

	done, err := s.ArrImported(ctx, AppSonarr)
	require.NoError(t, err)
	require.False(t, done)
	require.NoError(t, s.SetArrImported(ctx, AppSonarr))
	done, err = s.ArrImported(ctx, AppSonarr)
	require.NoError(t, err)
	require.True(t, done)
	done, err = s.ArrImported(ctx, AppRadarr)
	require.NoError(t, err)
	require.False(t, done)
}
