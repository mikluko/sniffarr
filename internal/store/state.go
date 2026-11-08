package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"

	"sarak.sh/mikluko/sniffarr/internal/sketch"
)

// The state keys of the peer hash key and the DHT node ID.
const (
	hashKeyState = "peer_hash_key"
	nodeIDState  = "dht_node_id"
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
	raw, err := s.stateValue(ctx, hashKeyState, fresh[:])
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
	raw, err := s.stateValue(ctx, nodeIDState, fresh[:])
	if err != nil {
		return [20]byte{}, err
	}
	if len(raw) != len(fresh) {
		return [20]byte{}, fmt.Errorf("stored node ID of %d bytes, want %d", len(raw), len(fresh))
	}
	s.nodeID, s.nodeIDSet = [20]byte(raw), true
	return s.nodeID, nil
}

// stateValue returns the value of the state key, storing fresh as it when
// none is stored.
func (s *Store) stateValue(ctx context.Context, key string, fresh []byte) ([]byte, error) {
	var raw []byte
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO state (key, value) VALUES (?, ?) ON CONFLICT (key) DO NOTHING", key, fresh); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, "SELECT value FROM state WHERE key = ?", key).Scan(&raw)
	})
	return raw, err
}
