package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/mikluko/batcher"
	"go.opentelemetry.io/otel/trace"
)

// DefaultProbeBatch is the default of Options.ProbeBatch.
const DefaultProbeBatch = 64

// probeBatchWait is the longest a LeaseProbe call waits for others to share
// its batch. A batch fills only from calls waiting at once, so a partly
// filled batch is the ordinary case, and its wait is added to every lease.
const probeBatchWait = 5 * time.Millisecond

// probeMargin is how many probes a batch looks up beyond its calls, for
// those another writer takes between the search and the write.
const probeMargin = 8

// ProbeRequest is a LeaseProbe call's ask.
type ProbeRequest struct {
	// Owner holds the lease from Now until Now+Lease.
	Owner int64
	Now   time.Time
	Lease time.Duration
	// MaxBytes is the most piece data the probe may download, journaled
	// with the lease.
	MaxBytes int64
	// Day is the key of the probes' traffic Admit is given, as ProbeTraffic
	// takes it.
	Day string
	// Admit reports, given the bytes probes downloaded on Day and how many
	// probes are leased and unexpired, whether a probe may start, and
	// whether a background one may; nil admits both. It is asked again
	// inside the write transaction, where the leases its batch granted
	// before count.
	Admit func(used int64, leased int) (start, background bool)
}

// probeLease is a LeaseProbe call waiting on its batch, and the lease the
// batch gave it.
type probeLease struct {
	ctx context.Context
	req ProbeRequest
	c   ProbeCandidate
	ok  bool
}

// LeaseProbe frees the expired probe leases, then leases to r.Owner the
// release to probe next and returns it, or false when there is none or r is
// not admitted. It takes the queued probe with the lowest priority, the
// newest first, among those at PriorityArr or ahead unless a background
// probe is admitted; then, for a background probe, the release never probed
// with the most peers over 7 days, else the one due a probe again with the
// most, which it queues at PrioritySample. Each is a movie or a series with
// peers and a stored file of a format a probe reads. The first lease of
// each attempt, and the first after a request or a search moved it ahead,
// is journaled with r.MaxBytes and when the lease ends; a lease after one
// expired or was freed is not.
//
// Calls waiting at once, up to Options.ProbeBatch, are served together, in
// the order they came, by one search on the read pool and one write
// transaction, so none gets a release another got. It fails once Close has
// been called.
func (s *Store) LeaseProbe(ctx context.Context, r ProbeRequest) (ProbeCandidate, bool, error) {
	l := &probeLease{ctx: ctx, req: r}
	t, err := s.probes.PushWait(ctx, l)
	if err != nil {
		return ProbeCandidate{}, false, err
	}
	if err := t.Wait(ctx); err != nil {
		return ProbeCandidate{}, false, err
	}
	return l.c, l.ok, nil
}

// newProbes returns the batcher LeaseProbe calls wait on, logging a batch
// that fails rather than panicking, as batcher's default does: the failure
// reaches each call's ticket as well. A batch's statements are traced under
// its first call's span.
func (s *Store) newProbes(size int) *batcher.Batcher[*probeLease] {
	return batcher.New(size, probeBatchWait,
		batcher.WithCallback[*probeLease](batcher.CallbackFunc[*probeLease](func(ctx context.Context, batch []*probeLease) error {
			return s.leaseProbes(trace.ContextWithSpan(ctx, trace.SpanFromContext(batch[0].ctx)), s.tx, batch)
		})),
		batcher.WithErrorHandler[*probeLease](func(ctx context.Context, err error) {
			s.log.ErrorContext(ctx, "probe lease batch failed", "error", err.Error())
		}),
	)
}

// Close stops LeaseProbe, serving the calls already waiting unless ctx
// expires first.
func (s *Store) Close(ctx context.Context) error {
	return s.probes.Close(ctx)
}

// leaseProbes serves the leases of batch whose callers still wait, in
// batch order, through run, a write transaction; it returns a
// batcher.ItemErrors with the context errors of the callers gone.
func (s *Store) leaseProbes(ctx context.Context, run func(context.Context, func(*sql.Tx) error) error, batch []*probeLease) error {
	live, gone := waiting(batch)
	if err := s.serveProbes(ctx, run, live); err != nil {
		return err
	}
	if gone != nil {
		return gone
	}
	return nil
}

// waiting returns the leases of batch whose callers still wait, and the
// errors of those gone, or nil when none is.
func waiting(batch []*probeLease) ([]*probeLease, batcher.ItemErrors) {
	live := make([]*probeLease, 0, len(batch))
	var gone batcher.ItemErrors
	for i, l := range batch {
		if err := l.ctx.Err(); err != nil {
			if gone == nil {
				gone = make(batcher.ItemErrors, len(batch))
			}
			gone[i] = err
			continue
		}
		live = append(live, l)
	}
	return live, gone
}

// serveProbes looks the probes up on the read pool, then leases them to
// live in one transaction of run; on its failure none holds a lease.
func (s *Store) serveProbes(ctx context.Context, run func(context.Context, func(*sql.Tx) error) error, live []*probeLease) error {
	if len(live) == 0 {
		return nil
	}
	offers, err := s.probeOffers(ctx, live)
	if err != nil || len(offers) == 0 {
		return err
	}
	err = run(ctx, func(tx *sql.Tx) error { return claimProbes(ctx, tx, live, offers) })
	if err != nil {
		for _, l := range live {
			l.c, l.ok = ProbeCandidate{}, false
		}
	}
	return err
}

// latest is the latest Now of the leases.
func latest(ls []*probeLease) time.Time {
	var now time.Time
	for _, l := range ls {
		if l.req.Now.After(now) {
			now = l.req.Now
		}
	}
	return now
}

// probeOffer is a probe a batch may lease: a queued task, by ID, or a
// release not queued, by infohash alone.
type probeOffer struct {
	id       int64
	hash     []byte
	priority int
	// retry is a release not queued due a probe again, not one never probed.
	retry bool
}

// background reports whether only a background lease may take o.
func (o probeOffer) background() bool {
	return o.id == 0 || o.priority > PriorityArr
}

// probeOffers returns, from the read pool, the probes the leases may take,
// in the order LeaseProbe takes them: as many as there are leases and
// probeMargin more, the releases not queued only when a background probe is
// admitted and the queue holds too few.
func (s *Store) probeOffers(ctx context.Context, live []*probeLease) ([]probeOffer, error) {
	now := latest(live)
	b, err := newProbeBudget(ctx, s.r, now)
	if err != nil {
		return nil, err
	}
	start, background, err := b.anyAdmitted(ctx, live)
	if err != nil || !start {
		return nil, err
	}
	want := len(live) + probeMargin
	offers, err := scanProbes(ctx, s.r, nil, queuedProbeQuery, append(append([]any{now.UnixMilli(), now.UnixMilli()}, probeArgs()...), want)...)
	if err != nil || !background || len(offers) >= want {
		return offers, err
	}
	n := len(offers)
	if offers, err = scanProbes(ctx, s.r, offers, backgroundProbeQuery, append(probeArgs(), want-n)...); err != nil || len(offers) > n {
		return offers, err
	}
	return scanProbes(ctx, s.r, offers, retryProbeQuery, append(append([]any{now.Unix()}, probeArgs()...), want-n)...)
}

// queuedProbeQuery selects, given now twice, probeArgs and a limit, the
// queued probes free and due at now, a lease expired by now being free, in
// the order LeaseProbe takes them. CROSS JOIN keeps SQLite walking the
// queued probes rather than every release.
var queuedProbeQuery = `SELECT q.id, q.infohash, q.priority, 0 FROM task_queue q CROSS JOIN torrent_releases t ON t.infohash = q.infohash
	WHERE q.kind = 'probe' AND q.not_before <= ? AND (q.lease_owner IS NULL OR q.lease_until <= ?) AND ` + probeCond("1") + `
	ORDER BY q.priority, q.queued_at DESC, q.id DESC LIMIT ?`

// unqueued selects releases t not queued for a probe.
const unqueued = ` AND NOT EXISTS (SELECT 1 FROM task_queue q WHERE q.kind = 'probe' AND q.infohash = t.infohash)`

// backgroundProbeQuery selects, given probeArgs and a limit, the releases
// not queued for a probe never probed, the most peers over 7 days first.
var backgroundProbeQuery = `SELECT 0, t.infohash, ` + strconv.Itoa(PrioritySample) + `, 0 FROM torrent_releases t LEFT JOIN probe_releases p ON p.torrent_id = t.id
	WHERE ` + probeCond("p.outcome IS NULL") + unqueued + ` ORDER BY t.peers_week DESC, t.id DESC LIMIT ?`

// retryProbeQuery selects, given the time in seconds, probeArgs and a limit,
// the releases not queued for a probe due a probe again then, the most peers
// over 7 days first.
var retryProbeQuery = `SELECT 0, t.infohash, ` + strconv.Itoa(PrioritySample) + `, 1 FROM probe_releases p CROSS JOIN torrent_releases t ON t.id = p.torrent_id
	WHERE ` + probeCond(probeDue) + unqueued + ` ORDER BY t.peers_week DESC, t.id DESC LIMIT ?`

// scanProbes appends to offers those the query selects on q.
func scanProbes(ctx context.Context, q querier, offers []probeOffer, query string, args ...any) ([]probeOffer, error) {
	err := eachOn(ctx, q, func(rows *sql.Rows) error {
		var o probeOffer
		if err := rows.Scan(&o.id, &o.hash, &o.priority, &o.retry); err != nil {
			return err
		}
		offers = append(offers, o)
		return nil
	}, query, args...)
	return offers, err
}

// rowQuerier runs single-row queries: a *sql.DB or a *sql.Tx.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// probeBudget is what ProbeRequest.Admit is given, as read on q: how many
// probes are leased and unexpired, and the bytes probes downloaded by day.
type probeBudget struct {
	q      rowQuerier
	leased int
	used   map[string]int64
}

func newProbeBudget(ctx context.Context, q rowQuerier, now time.Time) (*probeBudget, error) {
	b := &probeBudget{q: q, used: map[string]int64{}}
	return b, q.QueryRowContext(ctx, probeLeasesQuery, now.UnixMilli()).Scan(&b.leased)
}

// admit reports whether r's probe may start, and whether a background one
// may.
func (b *probeBudget) admit(ctx context.Context, r ProbeRequest) (start, background bool, err error) {
	if r.Admit == nil {
		return true, true, nil
	}
	used, ok := b.used[r.Day]
	if !ok {
		err := b.q.QueryRowContext(ctx, "SELECT rx FROM probe_usage WHERE day = ?", r.Day).Scan(&used)
		if err = ignoreNoRows(err); err != nil {
			return false, false, err
		}
		b.used[r.Day] = used
	}
	start, background = r.Admit(used, b.leased)
	return start, background, nil
}

// anyAdmitted reports whether any of the leases may start a probe, and
// whether any may start a background one.
func (b *probeBudget) anyAdmitted(ctx context.Context, live []*probeLease) (start, background bool, err error) {
	for _, l := range live {
		st, bg, err := b.admit(ctx, l.req)
		if err != nil {
			return false, false, err
		}
		start, background = start || st, background || bg
	}
	return start, background, nil
}

// claimProbes frees the expired probe leases, then gives each of live the
// first of offers its request admits that is still free, journaling the
// lease as LeaseProbe does.
func claimProbes(ctx context.Context, tx *sql.Tx, live []*probeLease, offers []probeOffer) error {
	if _, err := freeExpired(ctx, tx, "kind = 'probe'", latest(live)); err != nil {
		return err
	}
	b, err := newProbeBudget(ctx, tx, latest(live))
	if err != nil {
		return err
	}
	taken := make([]bool, len(offers))
	for _, l := range live {
		if err := claimFor(ctx, tx, b, l, offers, taken); err != nil {
			return err
		}
	}
	return nil
}

// claimFor leases to l the first of offers not taken that its request
// admits and that is still free, marking each it tries as taken.
func claimFor(ctx context.Context, tx *sql.Tx, b *probeBudget, l *probeLease, offers []probeOffer, taken []bool) error {
	start, background, err := b.admit(ctx, l.req)
	if err != nil || !start {
		return err
	}
	for i, o := range offers {
		if taken[i] || o.background() && !background {
			continue
		}
		taken[i] = true
		c, leases, err := claimProbe(ctx, tx, o, l.req, background)
		if err != nil {
			return err
		}
		if leases == 0 {
			continue
		}
		b.leased++
		l.c, l.ok = c, true
		if leases != 1 {
			return nil
		}
		r := l.req
		return journal(ctx, tx, c.Hash, r.Now, JournalProbeLeased, probeLeaseOutcome(c), Detail("attempt", c.Attempts+1, "max_bytes", r.MaxBytes, "lease_until", r.Now.Add(r.Lease)))
	}
	return nil
}

// probeAttempts is the failed attempts of the release of the task_queue row
// at hand.
const probeAttempts = `coalesce((SELECT p.attempts FROM torrent_releases t JOIN probe_releases p ON p.torrent_id = t.id WHERE t.infohash = task_queue.infohash), 0)`

// claimQueuedQuery leases, given the owner, the lease's end, now, the task
// ID, now, PriorityArr, whether a background probe is admitted and
// probeArgs, the queued probe of that ID when it is free, due, at
// PriorityArr or ahead unless background, and readable; it returns its
// priority, its leases and its release's failed attempts.
var claimQueuedQuery = `UPDATE task_queue SET lease_owner = ?, lease_until = ?, leased_at = ?, leases = leases + 1
	WHERE id = ? AND lease_owner IS NULL AND not_before <= ? AND (priority <= ? OR ?)
	AND EXISTS (SELECT 1 FROM torrent_releases t WHERE t.infohash = task_queue.infohash AND ` + probeCond("1") + `)
	RETURNING priority, leases, ` + probeAttempts

// claimUnqueuedQuery queues at PrioritySample, given now, the owner, the
// lease's end, now and the infohash, then the arguments of state and
// probeArgs, the release of that infohash leased when it is not queued and
// a probe reads it with its probe row passing state; it returns its failed
// attempts.
func claimUnqueuedQuery(state string) string {
	return `INSERT INTO task_queue (kind, infohash, priority, queued_at, lease_owner, lease_until, leased_at, leases)
	SELECT 'probe', t.infohash, ` + strconv.Itoa(PrioritySample) + `, ?, ?, ?, ?, 1 FROM torrent_releases t LEFT JOIN probe_releases p ON p.torrent_id = t.id
	WHERE t.infohash = ? AND ` + probeCond(state) + unqueued + `
	RETURNING ` + probeAttempts
}

var (
	claimNeverProbedQuery = claimUnqueuedQuery("p.outcome IS NULL")
	claimRetryQuery       = claimUnqueuedQuery(probeDue)
)

// claimProbe leases o for r and returns it with how many times its task has
// been leased since its attempt began or it last moved ahead, or 0 when o is
// no longer free.
func claimProbe(ctx context.Context, tx *sql.Tx, o probeOffer, r ProbeRequest, background bool) (ProbeCandidate, int, error) {
	now, until := r.Now.UnixMilli(), r.Now.Add(r.Lease).UnixMilli()
	c := ProbeCandidate{Hash: Hash(o.hash), Priority: PrioritySample}
	leases := 1
	var err error
	switch {
	case o.id != 0:
		err = tx.QueryRowContext(ctx, claimQueuedQuery, append([]any{r.Owner, until, now, o.id, now, PriorityArr, background}, probeArgs()...)...).Scan(&c.Priority, &leases, &c.Attempts)
	case o.retry:
		err = tx.QueryRowContext(ctx, claimRetryQuery, append([]any{now, r.Owner, until, now, o.hash, r.Now.Unix()}, probeArgs()...)...).Scan(&c.Attempts)
	default:
		err = tx.QueryRowContext(ctx, claimNeverProbedQuery, append([]any{now, r.Owner, until, now, o.hash}, probeArgs()...)...).Scan(&c.Attempts)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ProbeCandidate{}, 0, nil
	}
	if err != nil {
		return ProbeCandidate{}, 0, err
	}
	c.Wanted = c.Priority <= PriorityArr
	return c, leases, nil
}
