package orders

import (
	"sync"
	"time"
)

// PaceWindow is how far back Levels reads the pace of finished orders.
const PaceWindow = 15 * time.Minute

// pace counts the orders finished per kind in minute buckets over the last
// PaceWindow, since the earliest it counted. Its zero value is ready to use.
type pace struct {
	mu      sync.Mutex
	since   time.Time
	buckets map[string]map[int64]int64
}

func (p *pace) add(kind string, at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.buckets == nil {
		p.buckets = map[string]map[int64]int64{}
		p.since = at
	}
	if at.Before(p.since) {
		p.since = at
	}
	m := p.buckets[kind]
	if m == nil {
		m = map[int64]int64{}
		p.buckets[kind] = m
	}
	minute := at.Unix() / 60
	m[minute]++
	for b := range m {
		if b <= minute-int64(PaceWindow/time.Minute) {
			delete(m, b)
		}
	}
}

// perSecond returns the orders of the kind finished per second over the last
// PaceWindow at now, or over the time since the earliest it counted of any
// kind when that is shorter, and at least a minute.
func (p *pace) perSecond(now time.Time, kind string) float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.buckets == nil {
		return 0
	}
	from := now.Add(-PaceWindow)
	if p.since.After(from) {
		from = p.since
	}
	span := max(now.Sub(from).Seconds(), 60)
	var n int64
	for b, c := range p.buckets[kind] {
		if b > now.Unix()/60-int64(PaceWindow/time.Minute) {
			n += c
		}
	}
	return float64(n) / span
}
