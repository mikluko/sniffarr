// Package sniffer is the replica side of the sniffarr work queue: a
// Discoverer reports the infohashes the DHT reveals to the server, and a
// Fetcher takes tasks from the server, fetches their metadata from peers,
// and returns it. A sniffer never downloads pieces, uploads, or announces
// itself, and it keeps peer addresses only for the duration of a fetch.
package sniffer

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Hash is a BitTorrent v1 infohash.
type Hash [20]byte

// Metadata is what a torrent's peers tell about it.
type Metadata struct {
	Name  string
	Size  int64
	Files []File
}

// File is one file of a torrent.
type File struct {
	Path string
	Size int64
}

var (
	// ErrNoPeers is returned by a MetadataSource that found nobody sharing
	// the torrent.
	ErrNoPeers = errors.New("no peers")
	// ErrNoMetadata is returned by a MetadataSource whose peers did not give
	// up the metadata in time.
	ErrNoMetadata = errors.New("no metadata")
)

// MetadataSource gets a torrent's metadata.
type MetadataSource interface {
	Metadata(ctx context.Context, h Hash) (Metadata, error)
}

const (
	DefaultFetchers  = 2000
	DefaultQueue     = 10000
	DefaultRemember  = 1000000
	DefaultStreamFor = time.Minute
	DefaultBackoff   = 5 * time.Second
	DefaultReport    = 30 * time.Second
)

// flushTimeout bounds the final Sight after a Discoverer's context is done.
const flushTimeout = 5 * time.Second

// orDefault sets a zero or negative *v to d.
func orDefault[T int | time.Duration](v *T, d T) {
	if *v <= 0 {
		*v = d
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// every calls f every d until ctx is done.
func every(ctx context.Context, d time.Duration, f func()) {
	tick := time.NewTicker(d)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			f()
		}
	}
}

// recent is a bounded set: once it holds size keys it forgets the older
// half.
type recent[K comparable] struct {
	mu        sync.Mutex
	size      int
	cur, prev map[K]struct{}
}

func newRecent[K comparable](size int) *recent[K] {
	return &recent[K]{size: size, cur: map[K]struct{}{}, prev: map[K]struct{}{}}
}

// add adds h and reports whether it was absent.
func (r *recent[K]) add(h K) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.cur[h]; ok {
		return false
	}
	if _, ok := r.prev[h]; ok {
		return false
	}
	if len(r.cur) >= r.size/2 {
		r.prev, r.cur = r.cur, make(map[K]struct{}, len(r.cur))
	}
	r.cur[h] = struct{}{}
	return true
}
