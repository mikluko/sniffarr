package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/pprof/profile"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.uber.org/fx"
	"golang.org/x/sync/errgroup"

	"sarak.sh/mikluko/sniffarr/internal/app/broker"
	"sarak.sh/mikluko/sniffarr/internal/bus"
)

// password is the worker user's password on every server natsload runs.
const password = "natsload"

// startLimit bounds a server's start, recovery included.
const startLimit = time.Hour

// node is a running broker with the worker listener on a loopback port.
type node struct {
	app *fx.App
	b   *broker.Broker
	url string
}

// open starts the broker on dir and returns it with the time its start took.
func open(ctx context.Context, dir string, log *slog.Logger) (*node, time.Duration, error) {
	port, err := freePort()
	if err != nil {
		return nil, 0, err
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	n := &node{url: "nats://" + addr}
	n.app = fx.New(fx.NopLogger,
		fx.Supply(broker.Config{Dir: dir, Addr: addr, Password: password}, log),
		broker.Module,
		fx.Populate(&n.b),
	)
	if err := n.app.Err(); err != nil {
		return nil, 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, startLimit)
	defer cancel()
	t := time.Now()
	if err := n.app.Start(ctx); err != nil {
		return nil, 0, err
	}
	return n, time.Since(t), nil
}

// close stops the broker cleanly and returns the time it took.
func (n *node) close(ctx context.Context) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()
	t := time.Now()
	err := n.app.Stop(ctx)
	return time.Since(t), err
}

// msgs returns the number of messages in the fetch order stream.
func (n *node) msgs(ctx context.Context) (uint64, error) {
	s, err := n.b.JetStream.Stream(ctx, bus.StreamFetchOrders)
	if err != nil {
		return 0, err
	}
	info, err := s.Info(ctx)
	if err != nil {
		return 0, err
	}
	return info.State.Msgs, nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	return port, l.Close()
}

// fill publishes orders fetch orders of size bytes, each to a priority drawn
// by weights with a fixed seed.
func fill(ctx context.Context, n *node, orders, size int, weights []int) error {
	var failed atomic.Int64
	js, err := jetstream.New(n.b.Conn,
		jetstream.WithPublishAsyncMaxPending(4096),
		jetstream.WithPublishAsyncErrHandler(func(jetstream.JetStream, *nats.Msg, error) { failed.Add(1) }))
	if err != nil {
		return err
	}
	total := 0
	for _, w := range weights {
		total += w
	}
	r := rand.New(rand.NewPCG(1, 2))
	for i := range orders {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		p, x := 0, r.IntN(total)
		for x >= weights[p] {
			x -= weights[p]
			p++
		}
		if _, err := js.PublishAsync(bus.FetchOrders[p], order(i, size)); err != nil {
			return err
		}
	}
	select {
	case <-js.PublishAsyncComplete():
	case <-ctx.Done():
		return ctx.Err()
	}
	if f := failed.Load(); f > 0 {
		return fmt.Errorf("%d publishes failed", f)
	}
	return nil
}

// order returns the payload of order i: a hex infohash padded to size bytes.
func order(i, size int) []byte {
	h := sha1.Sum(strconv.AppendInt(nil, int64(i), 10))
	b := bytes.Repeat([]byte{'x'}, max(size, 2*len(h)))
	hex.Encode(b, h[:])
	return b
}

// drainSpec is how a drain takes orders.
type drainSpec struct {
	// workers pull at once, each on its own connection as bus.WorkerUser.
	workers int
	// batch is the size of each fetch.
	batch int
	// prios is how many priorities, from P0, are taken.
	prios int
	// maxAckPending is set on the consumers taken; 0 leaves theirs.
	maxAckPending int
	// nak is the percentage of first deliveries naked with nakDelay.
	nak      int
	nakDelay time.Duration
	// stall ends the drain when no order is delivered for that long, and
	// limit when not all are delivered by then; 0 is no limit.
	stall, limit time.Duration
}

// drainStats is what a drain did.
type drainStats struct {
	Total      uint64
	Delivered  int64
	Acked      int64
	Naked      int64
	Errors     int64
	AckPending int
	Stalled    bool
	// Elapsed runs from the first fetch to the last first delivery, or to
	// the stream's settling when nothing stalled.
	Elapsed time.Duration
}

type counters struct {
	delivered, acked, naked, errors atomic.Int64
	last                            atomic.Int64
}

// drain takes every order pending on the first s.prios priorities, each
// worker fetching from the highest priority with any, acking each order or,
// on its first delivery, naking s.nak percent of them; it returns once all
// were delivered and the stream holds only the naked ones, or on a stall or
// the limit.
func drain(ctx context.Context, n *node, s drainSpec) (drainStats, error) {
	var st drainStats
	var err error
	if st.Total, err = setConsumers(ctx, n, s); err != nil {
		return st, err
	}
	before, err := n.msgs(ctx)
	if err != nil {
		return st, err
	}
	var c counters
	t0 := time.Now()
	c.last.Store(t0.UnixNano())
	pullCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	g, gctx := errgroup.WithContext(pullCtx)
	for range s.workers {
		g.Go(func() error { return pull(gctx, n.url, s, &c) })
	}
	end, stalled, werr := watch(gctx, n, s, &c, t0, st.Total, before-st.Total)
	cancel()
	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return st, err
	}
	if werr != nil {
		return st, werr
	}
	if end.IsZero() {
		return st, ctx.Err()
	}
	st.Stalled, st.Elapsed = stalled, end.Sub(t0)
	st.Delivered, st.Acked, st.Naked, st.Errors = c.delivered.Load(), c.acked.Load(), c.naked.Load(), c.errors.Load()
	st.AckPending, err = ackPending(ctx, n, s.prios)
	return st, err
}

// setConsumers sets s.maxAckPending on the consumers a drain takes and
// returns the orders pending on them.
func setConsumers(ctx context.Context, n *node, s drainSpec) (uint64, error) {
	var total uint64
	cfgs := bus.Consumers()[bus.StreamFetchOrders]
	for p := range s.prios {
		cfg := cfgs[p]
		if s.maxAckPending > 0 {
			cfg.MaxAckPending = s.maxAckPending
		}
		c, err := n.b.JetStream.CreateOrUpdateConsumer(ctx, bus.StreamFetchOrders, cfg)
		if err != nil {
			return 0, err
		}
		total += c.CachedInfo().NumPending
	}
	return total, nil
}

// watch returns the time by which all total orders were delivered and the
// stream held only the rest left untaken and the naked ones; on a stall or
// the limit, the time of the last first delivery and true; when ctx ends,
// the zero time.
func watch(ctx context.Context, n *node, s drainSpec, c *counters, t0 time.Time, total, rest uint64) (time.Time, bool, error) {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return time.Time{}, false, nil
		case <-tick.C:
		}
		last := time.Unix(0, c.last.Load())
		if uint64(c.delivered.Load()) >= total {
			left, err := n.msgs(ctx)
			if err != nil {
				return time.Time{}, false, err
			}
			if left == rest+uint64(c.naked.Load()) {
				return time.Now(), false, nil
			}
			continue
		}
		if time.Since(last) > s.stall || (s.limit > 0 && time.Since(t0) > s.limit) {
			return last, true, nil
		}
	}
}

// ackPending sums the pending acks of the first prios consumers.
func ackPending(ctx context.Context, n *node, prios int) (int, error) {
	sum := 0
	for p := range prios {
		con, err := n.b.JetStream.Consumer(ctx, bus.StreamFetchOrders, bus.OrderConsumers[p])
		if err != nil {
			return 0, err
		}
		sum += con.CachedInfo().NumAckPending
	}
	return sum, nil
}

// pull is one worker of drain.
func pull(ctx context.Context, url string, s drainSpec, c *counters) error {
	nc, err := nats.Connect(url, nats.UserInfo(bus.WorkerUser, password), nats.CustomInboxPrefix(bus.WorkerInbox), nats.Name("natsload"))
	if err != nil {
		return err
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}
	cons := make([]jetstream.Consumer, s.prios)
	for p := range cons {
		if cons[p], err = js.Consumer(ctx, bus.StreamFetchOrders, bus.OrderConsumers[p]); err != nil {
			return err
		}
	}
	for ctx.Err() == nil {
		if !take(cons, s, c) {
			select {
			case <-ctx.Done():
			case <-time.After(time.Millisecond):
			}
		}
	}
	return nil
}

// take fetches a batch from the first of cons with orders and handles it,
// reporting whether any had orders.
func take(cons []jetstream.Consumer, s drainSpec, c *counters) bool {
	for _, con := range cons {
		batch, err := con.FetchNoWait(s.batch)
		if err != nil {
			c.errors.Add(1)
			continue
		}
		k := 0
		for m := range batch.Messages() {
			k++
			handle(m, s, c)
		}
		if batch.Error() != nil {
			c.errors.Add(1)
		}
		if k > 0 {
			return true
		}
	}
	return false
}

func handle(m jetstream.Msg, s drainSpec, c *counters) {
	md, err := m.Metadata()
	if err != nil {
		c.errors.Add(1)
		return
	}
	first := md.NumDelivered == 1
	if first {
		c.delivered.Add(1)
		c.last.Store(time.Now().UnixNano())
	}
	if first && naked(md.Sequence.Stream, s.nak) {
		err = m.NakWithDelay(s.nakDelay)
		c.naked.Add(1)
	} else {
		err = m.Ack()
		c.acked.Add(1)
	}
	if err != nil {
		c.errors.Add(1)
	}
}

// naked reports whether the order at stream sequence seq is among the pct
// percent naked.
func naked(seq uint64, pct int) bool {
	seq += 0x9e3779b97f4a7c15
	seq = (seq ^ seq>>30) * 0xbf58476d1ce4e5b9
	seq = (seq ^ seq>>27) * 0x94d049bb133111eb
	return int((seq^seq>>31)%100) < pct
}

// meter measures a phase of this process: wall time, CPU time and the peak
// in-use heap.
type meter struct {
	t0   time.Time
	cpu0 time.Duration
	peak atomic.Uint64
	stop chan struct{}
	done chan struct{}
}

func startMeter() *meter {
	m := &meter{t0: time.Now(), cpu0: cpuTime(), stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			if ms.HeapInuse > m.peak.Load() {
				m.peak.Store(ms.HeapInuse)
			}
			select {
			case <-m.stop:
				return
			case <-t.C:
			}
		}
	}()
	return m
}

func (m *meter) end() (elapsed, cpu time.Duration, peak uint64) {
	close(m.stop)
	<-m.done
	return time.Since(m.t0), cpuTime() - m.cpu0, m.peak.Load()
}

func cpuTime() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// maxRSS returns this process's peak resident set in bytes.
func maxRSS() uint64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return uint64(ru.Maxrss)
	}
	return uint64(ru.Maxrss) * 1024
}

// liveHeap returns the heap in use after a collection.
func liveHeap() uint64 {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

// watched are the server functions whose share of CPU a drain reports, by
// the suffix of their symbol.
var watched = []struct{ key, suffix string }{
	{"checkPending", ".(*consumer).checkPending"},
	{"removeMsg", ".(*fileStore).removeMsg"},
	{"removeMsgBlock", ".(*fileStore).removeMsgBlock"},
	{"loadMsgs", ".(*msgBlock).loadMsgsWithLock"},
}

// profiler records a CPU profile of this process.
type profiler struct{ buf bytes.Buffer }

func startProfile() (*profiler, error) {
	p := &profiler{}
	return p, pprof.StartCPUProfile(&p.buf)
}

// end stops the profile and returns it with each watched function's share
// of the CPU samples whose stacks hold it.
func (p *profiler) end() ([]byte, map[string]float64, error) {
	pprof.StopCPUProfile()
	data := p.buf.Bytes()
	prof, err := profile.ParseData(data)
	if err != nil {
		return nil, nil, err
	}
	return data, shares(prof), nil
}

func shares(prof *profile.Profile) map[string]float64 {
	v := len(prof.SampleType) - 1
	var total int64
	hits := make([]int64, len(watched))
	for _, s := range prof.Sample {
		total += s.Value[v]
		for i, in := range holds(s) {
			if in {
				hits[i] += s.Value[v]
			}
		}
	}
	out := make(map[string]float64, len(watched))
	for i, w := range watched {
		if total > 0 {
			out[w.key] = 100 * float64(hits[i]) / float64(total)
		}
	}
	return out
}

// holds reports, for each of watched, whether s's stack holds it.
func holds(s *profile.Sample) []bool {
	in := make([]bool, len(watched))
	for _, loc := range s.Location {
		for _, line := range loc.Line {
			if line.Function == nil {
				continue
			}
			for i, w := range watched {
				in[i] = in[i] || strings.HasSuffix(line.Function.Name, w.suffix)
			}
		}
	}
	return in
}
