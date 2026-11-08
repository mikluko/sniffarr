package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"

	"sarak.sh/mikluko/sniffarr/internal/sketch"
)

// The state keys of the peer hash key, the DHT node ID and the port the ID
// was handed out with.
const (
	hashKeyState  = "peer_hash_key"
	nodeIDState   = "dht_node_id"
	nodePortState = "dht_node_port"
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
// it on the first call and keeping it in the store. A port other than 0 that
// differs from the one stored with the ID, or comes when none is, replaces
// the ID with a new one: nodes that met the old ID hold it at the old port.
func (s *Store) NodeID(ctx context.Context, port uint16) ([20]byte, error) {
	var fresh [20]byte
	if _, err := rand.Read(fresh[:]); err != nil {
		return [20]byte{}, err
	}
	var id []byte
	err := s.tx(ctx, func(tx *sql.Tx) error {
		stored, err := stateRow(ctx, tx, nodeIDState)
		if err != nil {
			return err
		}
		storedPort, err := stateRow(ctx, tx, nodePortState)
		if err != nil {
			return err
		}
		id = stored
		if id != nil && (port == 0 || bytes.Equal(storedPort, portBytes(port))) {
			return nil
		}
		id = fresh[:]
		if err := putState(ctx, tx, nodeIDState, id); err != nil {
			return err
		}
		if port == 0 {
			return nil
		}
		return putState(ctx, tx, nodePortState, portBytes(port))
	})
	if err != nil {
		return [20]byte{}, err
	}
	if len(id) != len(fresh) {
		return [20]byte{}, fmt.Errorf("stored node ID of %d bytes, want %d", len(id), len(fresh))
	}
	return [20]byte(id), nil
}

// portBytes is port as stored beside the node ID, big-endian.
func portBytes(port uint16) []byte {
	return binary.BigEndian.AppendUint16(nil, port)
}

// stateRow returns the value of the state key, nil when none is stored.
func stateRow(ctx context.Context, tx *sql.Tx, key string) ([]byte, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT value FROM state WHERE key = ?", key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return raw, err
}

// putState stores value as the state key, replacing any stored.
func putState(ctx context.Context, tx *sql.Tx, key string, value []byte) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO state (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value", key, value)
	return err
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
