package store

import (
	"context"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/sketch"
)

// sketchOf returns a sketch of n peers numbered from first, hashed with k.
func sketchOf(k sketch.Key, first, n int) []byte {
	s := sketch.New()
	for i := first; i < first+n; i++ {
		s.Add(k.Hash(netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}), 6881)))
	}
	return s.Bytes()
}

func TestHashKeyOutlivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	s, db := openAt(t, path)
	k1, err := s.HashKey(ctx)
	require.NoError(t, err)
	k2, err := s.HashKey(ctx)
	require.NoError(t, err)
	require.Equal(t, k1, k2)
	require.NotEqual(t, sketch.Key{}, k1)
	require.NoError(t, db.Close())

	s, db = openAt(t, path)
	defer db.Close()
	k3, err := s.HashKey(ctx)
	require.NoError(t, err)
	require.Equal(t, k1, k3, "the key is kept in the store")
}

func TestMergePeers(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	k, err := s.HashKey(ctx)
	require.NoError(t, err)
	_, _, err = s.Insert(ctx, torrent(1, "Alien.1979.mkv"))
	require.NoError(t, err)

	merged, dropped, err := s.MergePeers(ctx, []PeerSketch{
		{Hash: hash(1), Hour: t0, Sketch: sketchOf(k, 0, 100)},
		{Hash: hash(1), Hour: t0.Add(20 * time.Minute), Sketch: sketchOf(k, 50, 100)},
		{Hash: hash(1), Hour: t0.Add(-time.Hour), Sketch: sketchOf(k, 1000, 30)},
		{Hash: hash(2), Hour: t0, Sketch: sketchOf(k, 0, 10)},
	})
	require.NoError(t, err)
	require.Equal(t, 3, merged)
	require.Equal(t, 1, dropped, "a sketch of an unknown infohash is dropped")

	var n int
	require.NoError(t, s.r.QueryRowContext(ctx, "SELECT count(*) FROM peers_hour").Scan(&n))
	require.Equal(t, 2, n, "two sketches of one hour merge into one row")
	r, err := s.Get(ctx, hash(1))
	require.NoError(t, err)
	c, err := s.Peers(ctx, r.ID, t0)
	require.NoError(t, err)
	require.InDelta(t, 180, c.Day, 18, "the union of 0-150 and 1000-1030")
	require.Equal(t, c.Day, c.Week)
	require.Equal(t, c.Day, c.Month)
	c, err = s.Peers(ctx, r.ID, t0.Add(25*time.Hour))
	require.NoError(t, err)
	require.Zero(t, c.Day, "the day window slid past both hours")
	require.InDelta(t, 180, c.Week, 18)

	_, _, err = s.MergePeers(ctx, []PeerSketch{{Hash: hash(1), Hour: t0, Sketch: []byte{1, 2, 3}}})
	require.Error(t, err, "garbage is not a sketch")
	_, err = s.Peers(ctx, 404, t0)
	require.ErrorIs(t, err, ErrNotFound)

	_, err = s.Sweep(ctx, t0.Add(time.Hour), 10)
	require.NoError(t, err)
	require.NoError(t, s.r.QueryRowContext(ctx, "SELECT count(*) FROM peers_hour").Scan(&n))
	require.Zero(t, n, "sketches go with their torrent")
}

func TestRollupPeers(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	k, err := s.HashKey(ctx)
	require.NoError(t, err)
	_, _, err = s.Insert(ctx, torrent(1, "Alien.1979.mkv"))
	require.NoError(t, err)
	r, err := s.Get(ctx, hash(1))
	require.NoError(t, err)
	old := t0.Add(-9 * 24 * time.Hour)
	_, _, err = s.MergePeers(ctx, []PeerSketch{
		{Hash: hash(1), Hour: t0, Sketch: sketchOf(k, 0, 10)},
		{Hash: hash(1), Hour: t0.Add(-20 * 24 * time.Hour), Sketch: sketchOf(k, 500, 50)},
		{Hash: hash(1), Hour: old, Sketch: sketchOf(k, 0, 100)},
		{Hash: hash(1), Hour: old.Add(time.Hour), Sketch: sketchOf(k, 50, 100)},
		{Hash: hash(1), Hour: t0.Add(-70 * 24 * time.Hour), Sketch: sketchOf(k, 2000, 100)},
	})
	require.NoError(t, err)

	rolled, err := s.RollupPeers(ctx, t0)
	require.NoError(t, err)
	require.Equal(t, Rolled{Hours: 4, Days: 1}, rolled, "hours older than 8 days roll up; the day older than 65 is deleted")
	var hours, days int
	require.NoError(t, s.r.QueryRowContext(ctx, "SELECT count(*) FROM peers_hour").Scan(&hours))
	require.NoError(t, s.r.QueryRowContext(ctx, "SELECT count(*) FROM peers_day").Scan(&days))
	require.Equal(t, 1, hours)
	require.Equal(t, 2, days)
	var card int64
	require.NoError(t, s.r.QueryRowContext(ctx, "SELECT hll_card(sketch) FROM peers_day WHERE day = ?", old.Truncate(24*time.Hour).Unix()).Scan(&card))
	require.InDelta(t, 150, card, 15, "the two hours of the day are unioned")

	c, err := s.Peers(ctx, r.ID, t0)
	require.NoError(t, err)
	require.InDelta(t, 10, c.Day, 2)
	require.InDelta(t, 10, c.Week, 2)
	require.InDelta(t, 210, c.Month, 21, "the month counts the days too")

	rolled, err = s.RollupPeers(ctx, t0)
	require.NoError(t, err)
	require.Equal(t, Rolled{}, rolled, "a second rollup finds nothing")
}

func TestWorkKeysFilled(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	s, db := openAt(t, path)
	tr := torrent(1, "Severance.S02E01.1080p.WEB-DL")
	tr.Kind, tr.KindLayer, tr.TitleKey = "series", LayerRules, "s:severance"
	_, _, err := s.Insert(ctx, tr)
	require.NoError(t, err)
	r, err := s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.Empty(t, r.WorkKey, "Insert takes the work key it is given")
	require.NoError(t, db.Close())

	s, db = openAt(t, path)
	defer db.Close()
	r, err = s.Get(ctx, hash(1))
	require.NoError(t, err)
	require.Equal(t, "s:severance/2", r.WorkKey, "New derives the work key of a torrent stored without one")
	require.Equal(t, 2, r.Season)
	require.Zero(t, r.Year)
}
