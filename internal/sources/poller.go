package sources

import (
	"context"
	"hash/fnv"
	"log/slog"
	"time"
)

// Defaults of Poller.
const (
	DefaultInterval = 15 * time.Minute
	DefaultRetry    = 5 * time.Minute
	DefaultPause    = time.Second
)

// Page is how many listings one request asks for, and MaxPages how many
// requests one round makes at most.
const (
	Page     = 100
	MaxPages = 5
)

// Poller records what its source lists latest, at start and then once an
// Interval, at the moment of the interval Offset names. Zero fields take the
// default named in their doc.
type Poller struct {
	Client   *Client
	Recorder *Recorder
	// Interval is how often Run asks the source; DefaultInterval. Retry is
	// how long it waits after a round that failed, at most until the next
	// round; DefaultRetry.
	Interval time.Duration
	Retry    time.Duration
	// Pause is the wait between two requests of a round; DefaultPause.
	Pause time.Duration
	Log   *slog.Logger
	// Now is time.Now when nil.
	Now func() time.Time
	// Node tells this node from others polling the same sources, so their
	// rounds fall at different moments; the store's hash key when empty.
	Node []byte
}

// Run polls the source at once, then at every Interval of wall-clock time
// plus the source's Offset, until ctx is done. The rounds fall at the same
// moments across restarts, and sources with different names at different
// ones.
func (p *Poller) Run(ctx context.Context) error {
	if len(p.Node) == 0 {
		key, err := p.Recorder.Store.HashKey(ctx)
		if err != nil {
			return err
		}
		p.Node = key[:]
	}
	for {
		next := p.Next(p.now())
		if err := p.Round(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			p.Log.ErrorContext(ctx, "poll source", "source", p.Client.Source.Name, "err", err)
			if retry := p.now().Add(p.retry()); retry.Before(next) {
				next = retry
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Until(next)):
		}
	}
}

// Next is the first moment after now that falls on an Interval of
// wall-clock time plus Offset of the source's name and Node.
func (p *Poller) Next(now time.Time) time.Time {
	interval := p.interval()
	next := now.Truncate(interval).Add(Offset(p.Client.Source.Name, p.Node, interval))
	for !next.After(now) {
		next = next.Add(interval)
	}
	return next
}

// Offset is the share of interval the rounds of the source name fall at on
// the node: whole seconds fixed by the two, so two sources are polled at
// different moments, and so is one source by two nodes.
func Offset(name string, node []byte, interval time.Duration) time.Duration {
	seconds := uint32(interval / time.Second)
	if seconds == 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write(node)
	_, _ = h.Write([]byte(name))
	return time.Duration(h.Sum32()%seconds) * time.Second
}

// Round asks the source for its latest listings, a Page at a time, and
// records each, until a page holds a listing recorded before, comes back
// short, or MaxPages were asked for; it logs what came of them when any was
// new. An item without an infohash is skipped.
func (p *Poller) Round(ctx context.Context) error {
	name := p.Client.Source.Name
	counts := map[string]int{}
	items, skipped := 0, 0
	for page := range MaxPages {
		if page > 0 {
			if err := sleep(ctx, p.pause()); err != nil {
				return err
			}
		}
		got, err := p.Client.Latest(ctx, page*Page, Page)
		p.Recorder.request(ctx, name, err)
		if err != nil {
			return err
		}
		items += len(got)
		seen := false
		for _, it := range got {
			l, ok := Listing(name, it)
			if !ok {
				skipped++
				continue
			}
			outcome, err := p.Recorder.Record(ctx, l)
			if err != nil {
				return err
			}
			counts[outcome]++
			seen = seen || outcome == Duplicate
		}
		if seen || len(got) < Page {
			break
		}
	}
	if items > counts[Duplicate] {
		p.Log.InfoContext(ctx, "polled source", "source", name, "listings", items, "queued", counts[Queued], "stored", counts[Stored],
			"rejected", counts[Rejected], "skipped", skipped)
	}
	return nil
}

func (p *Poller) interval() time.Duration {
	if p.Interval > 0 {
		return p.Interval
	}
	return DefaultInterval
}

func (p *Poller) retry() time.Duration {
	if p.Retry > 0 {
		return p.Retry
	}
	return DefaultRetry
}

func (p *Poller) pause() time.Duration {
	if p.Pause > 0 {
		return p.Pause
	}
	return DefaultPause
}

func (p *Poller) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
