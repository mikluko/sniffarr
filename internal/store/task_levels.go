package store

import "time"

// Queue is the tasks of one kind queued, by priority.
type Queue struct {
	// PerSecond is the kind's pace, 0 for none.
	PerSecond float64
	// Levels run from PriorityRequest to PrioritySample.
	Levels []Level
}

// Level is the tasks of one kind queued at one priority.
type Level struct {
	Priority int
	// Tasks are the tasks queued at the priority, handed out or not.
	Tasks int64
	// ETA is how long until every task at the priority or ahead is
	// finished at the kind's pace, and Known whether that pace is known.
	ETA   time.Duration
	Known bool
}

// Levels returns the Queue of the tasks counted at each priority from
// PriorityRequest, none when counts is nil, worked at perSecond.
func Levels(counts []int64, perSecond float64) Queue {
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
