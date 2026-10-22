package search

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mikluko/sniffarr/internal/store"
)

// Snapshot is the store's counts at At, with MaxStatsDays periods in
// Stats.Days. It is never modified once made.
type Snapshot struct {
	At    time.Time
	Stats store.Stats
}

// Counts keeps the latest Snapshot of Store for Browser.Stats and
// Browser.Home. It is safe for concurrent use, and ready once Store is set.
type Counts struct {
	Store *store.Store
	// Now is time.Now when nil.
	Now func() time.Time
	// Log gets the failures of Run.
	Log *slog.Logger

	latest atomic.Pointer[Snapshot]
	once   sync.Once
	sem    chan struct{}
}

// Latest returns the latest snapshot, computing one when there is none yet.
// A computation under way is waited for rather than repeated.
func (c *Counts) Latest(ctx context.Context) (*Snapshot, error) {
	if s := c.latest.Load(); s != nil {
		return s, nil
	}
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.unlock()
	if s := c.latest.Load(); s != nil {
		return s, nil
	}
	return c.compute(ctx)
}

// Refresh computes a snapshot and makes it the latest.
func (c *Counts) Refresh(ctx context.Context) (*Snapshot, error) {
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.unlock()
	return c.compute(ctx)
}

// Run refreshes the snapshot at once and then every interval until ctx is
// done; a failed refresh is logged and keeps the previous snapshot.
func (c *Counts) Run(ctx context.Context, interval time.Duration) error {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		start := time.Now()
		if _, err := c.Refresh(ctx); err != nil && !errors.Is(err, context.Canceled) {
			c.Log.ErrorContext(ctx, "compute counts", "err", err)
		} else if err == nil {
			c.Log.Debug("computed counts", "took", time.Since(start))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

func (c *Counts) compute(ctx context.Context) (*Snapshot, error) {
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	st, err := c.Store.Stats(ctx, now, MaxStatsDays)
	if err != nil {
		return nil, err
	}
	s := &Snapshot{At: now, Stats: st}
	c.latest.Store(s)
	return s, nil
}

func (c *Counts) lock(ctx context.Context) error {
	c.once.Do(func() { c.sem = make(chan struct{}, 1) })
	select {
	case c.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Counts) unlock() { <-c.sem }
