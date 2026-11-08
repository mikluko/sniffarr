package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"sarak.sh/mikluko/sniffarr/internal/rules"
)

func lastSeen(t *testing.T, s *Store, h Hash) int64 {
	t.Helper()
	var at int64
	require.NoError(t, s.r.QueryRowContext(context.Background(), "SELECT last_seen FROM torrent_releases WHERE infohash = ?", h[:]).Scan(&at))
	return at
}

func TestScreen(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	tr := torrent(1, "Alien.1979.mkv")
	tr.SeenAt = t0
	_, _, err := s.Insert(ctx, tr)
	require.NoError(t, err)
	_, err = s.Reject(ctx, hash(2), rules.Software, "Чужой", 1, t0)
	require.NoError(t, err)
	now := t0.Add(time.Hour)
	h3, h4 := hash(3), hash(4)
	_, err = s.w.ExecContext(ctx, "INSERT INTO task_dead (key, until) VALUES (?, ?), (?, ?)",
		deadKey(h3[:]), now.Add(time.Minute).UnixMilli(), deadKey(h4[:]), now.UnixMilli())
	require.NoError(t, err)

	hashes := []Hash{hash(1), hash(2), hash(3), hash(4), hash(5)}
	for i := range 2 * screenChunk {
		hashes = append(hashes, Hash{0xff, byte(i >> 8), byte(i)})
	}
	got, err := s.Screen(ctx, hashes, now)
	require.NoError(t, err)
	require.Equal(t, map[Hash]Screened{
		hash(1): {Stored: true},
		hash(2): {Rejected: true},
		hash(3): {Dead: true},
	}, got, "4's entry has expired; 5 and the rest are unknown")
	require.Equal(t, t0.Unix(), lastSeen(t, s, hash(1)), "Screen only reads")

	require.NoError(t, s.Seen(ctx, []Hash{hash(1), hash(5)}, now))
	require.Equal(t, now.Unix(), lastSeen(t, s, hash(1)))
	require.NoError(t, s.Seen(ctx, []Hash{hash(1)}, t0))
	require.Equal(t, now.Unix(), lastSeen(t, s, hash(1)), "the last sighting never moves back")
}
