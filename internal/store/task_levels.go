package store

import (
	"context"
	"sync"
	"time"
)

// PaceWindow is how far back Queues reads the pace of finished tasks.
const PaceWindow = 15 * time.Minute

// Queue is the tasks of one kind queued, by priority.
type Queue struct {
	// PerSecond is the kind's pace over the last PaceWindow, 0 for none.
	PerSecond float64
	// Levels run from PriorityRequest to PrioritySample.
	Levels []Level
}

// Level is the tasks of one kind queued at one priority.
type Level struct {
	Priority int
	// Tasks are the tasks queued at the priority, leased or not.
	Tasks int64
	// ETA is how long until every task at the priority or ahead is
	// finished at the kind's pace, and Known whether that pace is known.
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

// perSecond returns the tasks of the kind finished per second over the
// last PaceWindow at now, or over the time since the earliest it counted
// of any kind when that is shorter, and at least a minute.
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
	span := now.Sub(from).Seconds()
	if span < 60 {
		span = 60
	}
	var n int64
	for b, c := range p.buckets[kind] {
		if b > now.Unix()/60-int64(PaceWindow/time.Minute) {
			n += c
		}
	}
	return float64(n) / span
}

// Finished counts a task of the kind as finished at the time, for the
// pace Queues reads. The count lives in memory and starts over with the
// process.
func (q *Tasks) Finished(kind string, at time.Time) {
	q.pace.add(kind, at)
}

// Queues returns the queue of each task kind at now, keyed by kind.
func (q *Tasks) Queues(ctx context.Context, now time.Time) (map[string]Queue, error) {
	counts, err := q.queued(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]Queue{}
	for _, kind := range []string{TaskFetch, TaskPeers, TaskProbe} {
		out[kind] = queue(counts[kind], q.pace.perSecond(now, kind))
	}
	return out, nil
}

// queued returns the tasks queued per kind at each priority from
// PriorityRequest to PrioritySample.
func (q *Tasks) queued(ctx context.Context) (map[string][]int64, error) {
	rows, err := q.db.R.QueryContext(ctx, "SELECT priority, kind, count(*) FROM task_queue GROUP BY priority, kind")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string][]int64{}
	for rows.Next() {
		var p int
		var kind string
		var n int64
		if err := rows.Scan(&p, &kind, &n); err != nil {
			return nil, err
		}
		if p < PriorityRequest || p > PrioritySample {
			continue
		}
		if counts[kind] == nil {
			counts[kind] = make([]int64, PrioritySample-PriorityRequest+1)
		}
		counts[kind][p-PriorityRequest] += n
	}
	return counts, rows.Err()
}

// queue is the Queue of the tasks counted at each priority from
// PriorityRequest, none when counts is nil, worked at perSecond.
func queue(counts []int64, perSecond float64) Queue {
	out := Queue{PerSecond: perSecond, Levels: make([]Level, PrioritySample-PriorityRequest+1)}
	var ahead int64
	for i := range out.Levels {
		var n int64
		if counts != nil {
			n = counts[i]
		}
		ahead += n
		out.Levels[i] = Level{Priority: PriorityRequest + i, Tasks: n}
		out.Levels[i].ETA, out.Levels[i].Known = eta(ahead, perSecond)
	}
	return out
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
