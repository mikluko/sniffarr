package store

import (
	"sync"
	"time"
)

// ProberWindow is how long Probers counts a prober after it last asked for
// a probe task. It outlasts a fetcher's 30-second poll while idle and its
// default 10-minute probe timeout while every worker is busy.
const ProberWindow = 15 * time.Minute

// probers holds when each prober last asked for a probe task. Its zero
// value is ready to use.
type probers struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// ProberSeen records that the prober with the ID asked for a probe task at
// the time. The record lives in memory and starts over with the process.
func (q *Tasks) ProberSeen(id string, at time.Time) {
	p := &q.probers
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.seen == nil {
		p.seen = map[string]time.Time{}
	}
	if at.After(p.seen[id]) {
		p.seen[id] = at
	}
}

// Probers returns how many probers ProberSeen recorded within ProberWindow
// before now.
func (q *Tasks) Probers(now time.Time) int {
	p := &q.probers
	p.mu.Lock()
	defer p.mu.Unlock()
	from := now.Add(-ProberWindow)
	n := 0
	for id, at := range p.seen {
		if at.Before(from) {
			delete(p.seen, id)
			continue
		}
		n++
	}
	return n
}
