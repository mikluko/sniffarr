package store

import (
	"context"
	"sync"
	"time"
)

// PaceWindow is how far back Levels reads the pace of finished tasks.
const PaceWindow = 15 * time.Minute

// Level is the queue at one priority.
type Level struct {
	Priority int
	// Tasks are the tasks queued at the priority, leased or not, by kind.
	Tasks map[string]int64
	// ETA is how long until every task at the priority or ahead is
	// finished at the pace of the last PaceWindow, and Known whether that
	// pace is known for every kind queued there.
	ETA   time.Duration
	Known bool
}

// pace counts the tasks finished per kind in minute buckets over the last
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

// perSecond returns the tasks of the kinds finished per second over the
// last PaceWindow at now, or over the time since the earliest it counted
// when that is shorter, and at least a minute.
func (p *pace) perSecond(now time.Time, kinds ...string) float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.buckets == nil {
		return 0
	}
	from := now.Add(-PaceWindow)
	if p.since.After(from) {
		from = p.since
	}
	span := now.Sub(from).Seconds()
	if span < 60 {
		span = 60
	}
	var n int64
	for _, k := range kinds {
		for b, c := range p.buckets[k] {
			if b > now.Unix()/60-int64(PaceWindow/time.Minute) {
				n += c
			}
		}
	}
	return float64(n) / span
}

// Finished counts a task of the kind as finished at the time, for the
// pace Levels reads. The count lives in memory and starts over with the
// process.
func (q *Tasks) Finished(kind string, at time.Time) {
	q.pace.add(kind, at)
}

// Levels returns the queue at each priority from PriorityRequest to
// PrioritySample at now. Fetchers work fetches and peer lookups, and
// probers probes, so each priority's ETA is the longer of the two: the
// tasks of each pool at the priority or ahead over that pool's pace.
func (q *Tasks) Levels(ctx context.Context, now time.Time) ([]Level, error) {
	levels := make([]Level, PrioritySample-PriorityRequest+1)
	for i := range levels {
		levels[i] = Level{Priority: PriorityRequest + i, Tasks: map[string]int64{TaskFetch: 0, TaskPeers: 0, TaskProbe: 0}}
	}
	rows, err := q.db.R.QueryContext(ctx, "SELECT priority, kind, count(*) FROM task_queue GROUP BY priority, kind")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p int
		var kind string
		var n int64
		if err := rows.Scan(&p, &kind, &n); err != nil {
			return nil, err
		}
		if p >= PriorityRequest && p <= PrioritySample {
			levels[p-PriorityRequest].Tasks[kind] += n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	fetchers := q.pace.perSecond(now, TaskFetch, TaskPeers)
	probers := q.pace.perSecond(now, TaskProbe)
	var fetches, probes int64
	for i := range levels {
		fetches += levels[i].Tasks[TaskFetch] + levels[i].Tasks[TaskPeers]
		probes += levels[i].Tasks[TaskProbe]
		f, fok := eta(fetches, fetchers)
		p, pok := eta(probes, probers)
		levels[i].ETA, levels[i].Known = max(f, p), fok && pok
	}
	return levels, nil
}

// eta is how long n tasks take at perSecond, and whether that is known.
func eta(n int64, perSecond float64) (time.Duration, bool) {
	if n == 0 {
		return 0, true
	}
	if perSecond == 0 {
		return 0, false
	}
	return time.Duration(float64(n) / perSecond * float64(time.Second)), true
}
