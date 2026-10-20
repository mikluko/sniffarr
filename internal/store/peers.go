package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/mikluko/sniffarr/internal/sketch"
)

// Retention of peer sketches: hours older than HourRetention are unioned
// into their day and deleted, days older than DayRetention deleted.
const (
	HourRetention = 8 * 24 * time.Hour
	DayRetention  = 65 * 24 * time.Hour
)

// The settings keys of the peer hash key and the DHT node ID.
const (
	hashKeySetting = "peer_hash_key"
	nodeIDSetting  = "dht_node_id"
)

// PeerSketch is a sketch of the peers of one torrent seen in one hour.
type PeerSketch struct {
	Hash Hash
	// Hour is a time within the hour.
	Hour   time.Time
	Sketch []byte
}

// Window is a sliding period of whole hours ending at the current one.
type Window struct {
	Name   string
	Length time.Duration
}

// The windows peers are counted over.
var (
	WindowDay   = Window{"24h", 24 * time.Hour}
	WindowWeek  = Window{"7d", 7 * 24 * time.Hour}
	WindowMonth = Window{"30d", 30 * 24 * time.Hour}
	Windows     = []Window{WindowDay, WindowWeek, WindowMonth}
)

// since returns the start of the window's earliest hour, as Unix seconds,
// and the start of that hour's day.
func (w Window) since(now time.Time) (hour, day int64) {
	hour = now.Truncate(time.Hour).Add(time.Hour - w.Length).Unix()
	return hour, hour - hour%int64(24*time.Hour/time.Second)
}

// PeerCounts are distinct peers over the three windows.
type PeerCounts struct {
	Day, Week, Month int64
}

// HashKey returns the key peers are hashed with, generating it on the first
// call and keeping it in the store.
func (s *Store) HashKey(ctx context.Context) (sketch.Key, error) {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	if s.keySet {
		return s.key, nil
	}
	fresh, err := sketch.NewKey()
	if err != nil {
		return sketch.Key{}, err
	}
	raw, err := s.setting(ctx, hashKeySetting, fresh[:])
	if err != nil {
		return sketch.Key{}, err
	}
	k, err := sketch.ParseKey(raw)
	if err != nil {
		return sketch.Key{}, fmt.Errorf("stored hash key: %w", err)
	}
	s.key, s.keySet = k, true
	return k, nil
}

// NodeID returns the DHT node ID of the sniffer that discovers, generating
// it on the first call and keeping it in the store.
func (s *Store) NodeID(ctx context.Context) ([20]byte, error) {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	if s.nodeIDSet {
		return s.nodeID, nil
	}
	var fresh [20]byte
	if _, err := rand.Read(fresh[:]); err != nil {
		return [20]byte{}, err
	}
	raw, err := s.setting(ctx, nodeIDSetting, fresh[:])
	if err != nil {
		return [20]byte{}, err
	}
	if len(raw) != len(fresh) {
		return [20]byte{}, fmt.Errorf("stored node ID of %d bytes, want %d", len(raw), len(fresh))
	}
	s.nodeID, s.nodeIDSet = [20]byte(raw), true
	return s.nodeID, nil
}

// setting returns the value of the setting key, storing fresh as it when
// none is stored.
func (s *Store) setting(ctx context.Context, key string, fresh []byte) ([]byte, error) {
	var raw []byte
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO NOTHING", key, fresh); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&raw)
	})
	return raw, err
}

// MergePeers unions each sketch into the stored sketch of its torrent and
// hour, and returns how many it merged and how many it dropped for an
// infohash that is not stored. A sketch that does not parse fails the batch.
func (s *Store) MergePeers(ctx context.Context, batch []PeerSketch) (merged, dropped int, err error) {
	for _, p := range batch {
		if _, err := sketch.Parse(p.Sketch); err != nil {
			return 0, 0, fmt.Errorf("sketch of %x: %w", p.Hash, err)
		}
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		for _, p := range batch {
			res, err := tx.ExecContext(ctx, `INSERT INTO peers_hour (torrent_id, hour, sketch)
				SELECT id, ?, ? FROM torrents WHERE infohash = ?
				ON CONFLICT (torrent_id, hour) DO UPDATE SET sketch = hll_union(sketch, excluded.sketch)`,
				p.Hour.Truncate(time.Hour).Unix(), p.Sketch, p.Hash[:])
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n > 0 {
				merged++
			} else {
				dropped++
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return merged, dropped, nil
}

// Rolled is what RollupPeers did.
type Rolled struct {
	// Hours unioned into days and deleted, and days deleted.
	Hours, Days int64
}

// RollupPeers unions the hour sketches older than HourRetention into their
// day sketches and deletes them, and deletes the day sketches older than
// DayRetention.
func (s *Store) RollupPeers(ctx context.Context, now time.Time) (Rolled, error) {
	var r Rolled
	const day = int64(24 * time.Hour / time.Second)
	hours := now.Add(-HourRetention).Truncate(time.Hour).Unix()
	days := now.Add(-DayRetention).Unix()
	days -= days % day
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO peers_day (torrent_id, day, sketch)
			SELECT torrent_id, hour - hour % ?, hll_union_agg(sketch) FROM peers_hour WHERE hour < ? GROUP BY 1, 2
			ON CONFLICT (torrent_id, day) DO UPDATE SET sketch = hll_union(sketch, excluded.sketch)`, day, hours); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "DELETE FROM peers_hour WHERE hour < ?", hours)
		if err != nil {
			return err
		}
		if r.Hours, err = res.RowsAffected(); err != nil {
			return err
		}
		res, err = tx.ExecContext(ctx, "DELETE FROM peers_day WHERE day < ?", days)
		if err != nil {
			return err
		}
		r.Days, err = res.RowsAffected()
		return err
	})
	return r, err
}

// peersOf is the SQL for the distinct peers of the torrent t.id over a
// window; it takes the window's hour and day starts as arguments.
const peersOf = `coalesce((SELECT hll_card(hll_union_agg(sketch)) FROM (
	SELECT sketch FROM peers_hour WHERE torrent_id = t.id AND hour >= ?
	UNION ALL SELECT sketch FROM peers_day WHERE torrent_id = t.id AND day >= ?)), 0)`

// Peers returns the distinct peers of the torrent with the ID over each
// window ending at now.
func (s *Store) Peers(ctx context.Context, id int64, now time.Time) (PeerCounts, error) {
	var c PeerCounts
	var args []any
	for _, w := range Windows {
		h, d := w.since(now)
		args = append(args, h, d)
	}
	err := s.r.QueryRowContext(ctx, "SELECT "+peersOf+", "+peersOf+", "+peersOf+" FROM torrents t WHERE t.id = ?", append(args, id)...).Scan(&c.Day, &c.Week, &c.Month)
	if errors.Is(err, sql.ErrNoRows) {
		return PeerCounts{}, ErrNotFound
	}
	return c, err
}
