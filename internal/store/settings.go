package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"

	"github.com/mikluko/sniffarr/internal/sketch"
)

// The settings keys of the peer hash key and the DHT node ID.
const (
	hashKeySetting = "peer_hash_key"
	nodeIDSetting  = "dht_node_id"
)

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
