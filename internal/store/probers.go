package store

import (
	"sync"
	"time"
)

// ProberWindow is how long Probers counts a prober after it last asked for
// a probe task. It outlasts a fetcher's 30-second poll while idle and its
// default 10-minute probe timeout while every worker is busy.
const ProberWindow = 15 * time.Minute

// Probers holds when each prober last asked for a probe task, in memory. Its
// zero value is ready to use, and it is safe for concurrent use.
type Probers struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// Seen records that the prober with the ID asked for a probe task at the
// time.
func (p *Probers) Seen(id string, at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.seen == nil {
		p.seen = map[string]time.Time{}
	}
	if at.After(p.seen[id]) {
		p.seen[id] = at
	}
}

// Count returns how many probers Seen recorded within ProberWindow before
// now.
func (p *Probers) Count(now time.Time) int {
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
