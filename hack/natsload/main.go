// Command natsload measures the fetch order stream on the NATS server the API
// embeds, started by the broker with the bus topology: publishing a backlog
// over the priority subjects, draining it at each batch size, nak rate and
// MaxAckPending, and starting the server on a store left by a clean stop and
// by a kill. Each step runs in a child process of its own.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"sarak.sh/mikluko/sniffarr/internal/bus"
)

// errUsage is returned once the flag package has reported a usage error.
var errUsage = errors.New("usage")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
	case errors.Is(err, errUsage):
		os.Exit(2)
	default:
		fmt.Fprintln(os.Stderr, "natsload:", err)
		os.Exit(1)
	}
}

// options are the command-line flags.
type options struct {
	dir      string
	orders   int
	size     int
	weights  []int
	workers  int
	batches  []int
	naks     []int
	nakDelay time.Duration
	stall    time.Duration
	limit    time.Duration
	profiles string
	restart  bool
	drain    bool
	verbose  bool

	step          string
	store         string
	batch         int
	nak           int
	maxAckPending int
	upper         bool
	hold          bool
}

func parse(args []string, stderr io.Writer) (options, error) {
	var o options
	fs := flag.NewFlagSet("natsload", flag.ContinueOnError)
	fs.SetOutput(stderr)
	weights, batches, naks := "1,4,15,80", "1,16,64", "0,5,20"
	fs.StringVar(&o.dir, "dir", "", "work directory for the stores; empty is a temporary one, removed at exit")
	fs.IntVar(&o.orders, "orders", 1_000_000, "orders in the backlog")
	fs.IntVar(&o.size, "size", 128, "bytes of each order")
	fs.StringVar(&weights, "weights", weights, "relative share of each priority, P0 to P3, in the backlog")
	fs.IntVar(&o.workers, "workers", 8, "pullers, each on its own connection")
	fs.StringVar(&batches, "batches", batches, "fetch batch sizes to drain with")
	fs.StringVar(&naks, "naks", naks, "percentages of orders naked with -nak-delay on their first delivery")
	fs.DurationVar(&o.nakDelay, "nak-delay", 10*time.Minute, "delay of each nak")
	fs.DurationVar(&o.stall, "stall", 10*time.Second, "a drain that delivers nothing for this long ends as stalled")
	fs.DurationVar(&o.limit, "limit", 3*time.Minute, "a drain that has not delivered every order by then ends as stalled")
	fs.StringVar(&o.profiles, "profiles", "", "directory to write each drain's CPU profile to; empty writes none")
	fs.BoolVar(&o.restart, "restart", true, "measure the server's start on stores left by a clean stop and by a kill")
	fs.BoolVar(&o.drain, "drain", true, "measure the drains")
	fs.BoolVar(&o.verbose, "v", false, "log the server's notices to stderr")
	fs.StringVar(&o.step, "step", "", "internal: run one step, fill, start or drain, in this process")
	fs.StringVar(&o.store, "store", "", "internal: the step's store directory")
	fs.IntVar(&o.batch, "batch", 1, "internal: the drain's batch size")
	fs.IntVar(&o.nak, "nak", 0, "internal: the drain's nak percentage")
	fs.IntVar(&o.maxAckPending, "max-ack-pending", 0, "internal: the drain's MaxAckPending")
	fs.BoolVar(&o.upper, "upper", false, "internal: the fill drains P0 to P2 after publishing")
	fs.BoolVar(&o.hold, "hold", false, "internal: the fill reports and waits to be killed instead of stopping")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, err
		}
		return o, errUsage
	}
	var err error
	if o.weights, err = ints("-weights", weights); err != nil {
		return o, err
	}
	if len(o.weights) != bus.Priorities {
		return o, fmt.Errorf("-weights: want %d", bus.Priorities)
	}
	if o.batches, err = ints("-batches", batches); err != nil {
		return o, err
	}
	if o.naks, err = ints("-naks", naks); err != nil {
		return o, err
	}
	if o.orders < 1 || o.workers < 1 {
		return o, errors.New("-orders and -workers: want at least 1")
	}
	return o, nil
}

// ints parses a comma-separated list of non-negative integers.
func ints(name, s string) ([]int, error) {
	var out []int
	for f := range strings.SplitSeq(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || n < 0 {
			return nil, fmt.Errorf("%s: %q is not a non-negative integer", name, f)
		}
		out = append(out, n)
	}
	return out, nil
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	o, err := parse(args, stderr)
	if err != nil {
		return err
	}
	if o.step != "" {
		return step(ctx, o, stdout, stderr)
	}
	return matrix(ctx, o, stdout, stderr)
}

// result is what a step reports, as one JSON line on stdout.
type result struct {
	Orders  uint64
	Elapsed time.Duration
	// Cores is the CPU time over the wall time of the measured phase.
	Cores  float64
	Heap   uint64
	MaxRSS uint64
	Stop   time.Duration
	Drain  *drainStats        `json:",omitempty"`
	Shares map[string]float64 `json:",omitempty"`
}

// step runs o.step in this process and writes its result to stdout.
func step(ctx context.Context, o options, stdout, stderr io.Writer) error {
	level := slog.LevelWarn
	if o.verbose {
		level = slog.LevelInfo
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
	var r result
	var err error
	switch o.step {
	case "fill":
		r, err = fillStep(ctx, o, log, stdout)
	case "start":
		r, err = startStep(ctx, o, log)
	case "drain":
		r, err = drainStep(ctx, o, log)
	default:
		return fmt.Errorf("-step %q", o.step)
	}
	if err != nil || o.hold {
		return err
	}
	return json.NewEncoder(stdout).Encode(r)
}

// fillStep publishes the backlog to a new store, with o.upper takes P0 to P2,
// and then stops the server or, with o.hold, reports and waits for ctx.
func fillStep(ctx context.Context, o options, log *slog.Logger, stdout io.Writer) (result, error) {
	var r result
	n, _, err := open(ctx, o.store, log)
	if err != nil {
		return r, err
	}
	m := startMeter()
	if err := fill(ctx, n, o.orders, o.size, o.weights); err != nil {
		return r, err
	}
	var cpu time.Duration
	r.Elapsed, cpu, r.Heap = m.end()
	r.Cores = cpu.Seconds() / r.Elapsed.Seconds()
	if o.upper {
		s := drainSpec{workers: o.workers, batch: 64, prios: bus.Priorities - 1, maxAckPending: o.workers * 64, stall: o.stall}
		if _, err := drain(ctx, n, s); err != nil {
			return r, err
		}
	}
	if r.Orders, err = n.msgs(ctx); err != nil {
		return r, err
	}
	r.MaxRSS = maxRSS()
	if o.hold {
		if err := json.NewEncoder(stdout).Encode(r); err != nil {
			return r, err
		}
		<-ctx.Done()
		return r, nil
	}
	r.Stop, err = n.close(ctx)
	return r, err
}

// startStep starts the server on o.store, then stops it cleanly.
func startStep(ctx context.Context, o options, log *slog.Logger) (result, error) {
	var r result
	n, d, err := open(ctx, o.store, log)
	if err != nil {
		return r, err
	}
	r.Elapsed = d
	if r.Orders, err = n.msgs(ctx); err != nil {
		return r, err
	}
	r.Heap, r.MaxRSS = liveHeap(), maxRSS()
	r.Stop, err = n.close(ctx)
	return r, err
}

// drainStep drains the store at o.store with o's batch, nak percentage and
// MaxAckPending, profiling the drain.
func drainStep(ctx context.Context, o options, log *slog.Logger) (result, error) {
	var r result
	n, _, err := open(ctx, o.store, log)
	if err != nil {
		return r, err
	}
	if r.Orders, err = n.msgs(ctx); err != nil {
		return r, err
	}
	p, err := startProfile()
	if err != nil {
		return r, err
	}
	m := startMeter()
	s := drainSpec{workers: o.workers, batch: o.batch, prios: bus.Priorities, maxAckPending: o.maxAckPending,
		nak: o.nak, nakDelay: o.nakDelay, stall: o.stall, limit: o.limit}
	st, err := drain(ctx, n, s)
	wall, cpu, heap := m.end()
	r.Cores, r.Heap = cpu.Seconds()/wall.Seconds(), heap
	data, shares, perr := p.end()
	if err != nil {
		return r, err
	}
	if perr != nil {
		return r, perr
	}
	if o.profiles != "" {
		name := fmt.Sprintf("drain-b%d-n%d-m%d.pprof", o.batch, o.nak, o.maxAckPending)
		if err := os.WriteFile(filepath.Join(o.profiles, name), data, 0o644); err != nil {
			return r, err
		}
	}
	r.Elapsed, r.Drain, r.Shares, r.MaxRSS = st.Elapsed, &st, shares, maxRSS()
	r.Stop, err = n.close(ctx)
	return r, err
}

// matrix runs every step in a child process and prints the tables.
func matrix(ctx context.Context, o options, stdout, stderr io.Writer) error {
	base := o.dir
	if base == "" {
		var err error
		if base, err = os.MkdirTemp("", "natsload"); err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(base) }()
	} else if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	if o.profiles != "" {
		if err := os.MkdirAll(o.profiles, 0o755); err != nil {
			return err
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	c := &children{exe: exe, o: o, stderr: stderr}
	template := filepath.Join(base, "template")
	_ = os.RemoveAll(template)
	_, _ = fmt.Fprintf(stderr, "filling %d orders\n", o.orders)
	f, err := c.run(ctx, "fill", template)
	if err != nil {
		return fmt.Errorf("fill: %w", err)
	}
	if _, err := fmt.Fprintf(stdout, "fill: %d orders in %s, %.0f/s, %.2f cores, heap peak %s, rss %s\n\n",
		f.Orders, f.Elapsed.Round(time.Millisecond), float64(f.Orders)/f.Elapsed.Seconds(),
		f.Cores, mib(f.Heap), mib(f.MaxRSS)); err != nil {
		return err
	}
	if o.restart {
		if err := c.restarts(ctx, base, template, stdout); err != nil {
			return err
		}
	}
	if o.drain {
		return c.drains(ctx, base, template, stdout)
	}
	return nil
}

type children struct {
	exe    string
	o      options
	stderr io.Writer
}

// run runs step on store in a child process and returns its result.
func (c *children) run(ctx context.Context, step, store string, extra ...string) (result, error) {
	cmd := exec.CommandContext(ctx, c.exe, c.args(step, store, extra)...)
	cmd.Stderr = c.stderr
	out, err := cmd.Output()
	if err != nil {
		return result{}, err
	}
	var r result
	return r, json.Unmarshal(out, &r)
}

// kill runs the fill on store with extra in a child process, kills it once
// it reports, and returns its report.
func (c *children) kill(ctx context.Context, store string, extra ...string) (result, error) {
	cmd := exec.CommandContext(ctx, c.exe, c.args("fill", store, append(extra, "-hold"))...)
	cmd.Stderr = c.stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return result{}, err
	}
	if err := cmd.Start(); err != nil {
		return result{}, err
	}
	var r result
	line, err := bufio.NewReader(out).ReadBytes('\n')
	if err == nil {
		err = json.Unmarshal(line, &r)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return r, err
}

func (c *children) args(step, store string, extra []string) []string {
	o := c.o
	join := func(xs []int) string {
		s := make([]string, len(xs))
		for i, x := range xs {
			s[i] = strconv.Itoa(x)
		}
		return strings.Join(s, ",")
	}
	args := []string{"-step", step, "-store", store,
		"-orders", strconv.Itoa(o.orders), "-size", strconv.Itoa(o.size), "-weights", join(o.weights),
		"-workers", strconv.Itoa(o.workers), "-nak-delay", o.nakDelay.String(), "-stall", o.stall.String(), "-limit", o.limit.String(),
		"-profiles", o.profiles, "-v=" + strconv.FormatBool(o.verbose)}
	return append(args, extra...)
}

// restarts measures the start on stores left by a clean stop and by a kill,
// holding the whole backlog or what is left once P0 to P2 are taken.
func (c *children) restarts(ctx context.Context, base, template string, stdout io.Writer) error {
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	_, _ = fmt.Fprintln(w, "store\tleft by\torders\tstart\tstop\tlive heap\trss\t")
	for _, tc := range []struct {
		name, by string
		extra    []string
	}{
		{"backlog", "stop", nil},
		{"backlog", "kill", nil},
		{"P3 only", "stop", []string{"-upper"}},
		{"P3 only", "kill", []string{"-upper"}},
	} {
		dir := filepath.Join(base, "restart")
		_ = os.RemoveAll(dir)
		_, _ = fmt.Fprintf(c.stderr, "restart: %s left by %s\n", tc.name, tc.by)
		var err error
		switch {
		case tc.by == "kill":
			_, err = c.kill(ctx, dir, tc.extra...)
		case tc.extra == nil:
			err = os.CopyFS(dir, os.DirFS(template))
		default:
			_, err = c.run(ctx, "fill", dir, tc.extra...)
		}
		if err != nil {
			return fmt.Errorf("restart %s %s: %w", tc.name, tc.by, err)
		}
		r, err := c.run(ctx, "start", dir)
		if err != nil {
			return fmt.Errorf("restart %s %s: %w", tc.name, tc.by, err)
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\t%s\t\n", tc.name, tc.by, r.Orders,
			r.Elapsed.Round(time.Millisecond), r.Stop.Round(time.Millisecond), mib(r.Heap), mib(r.MaxRSS))
		_ = os.RemoveAll(dir)
	}
	_, _ = fmt.Fprintln(w)
	return w.Flush()
}

// drains measures a drain of the backlog for each batch size, nak
// percentage and MaxAckPending: the workers' in-flight orders, and, with
// naks, that plus every order naked over the drain.
func (c *children) drains(ctx context.Context, base, template string, stdout io.Writer) error {
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	_, _ = fmt.Fprintln(w, "batch\tnak%\tmaxAckPending\torders/s\telapsed\tdelivered\tnaked\tackPending\tstalled\tcores\theap peak\trss\tcheckPending%\tremoveMsg%\tremoveMsgBlock%\tloadMsgs%\t")
	for _, batch := range c.o.batches {
		for _, nak := range c.o.naks {
			inflight := c.o.workers * batch
			maps := []int{inflight}
			if nak > 0 {
				maps = append(maps, inflight+c.o.orders*nak/100)
			}
			for _, mp := range maps {
				dir := filepath.Join(base, "drain")
				_ = os.RemoveAll(dir)
				if err := os.CopyFS(dir, os.DirFS(template)); err != nil {
					return err
				}
				_, _ = fmt.Fprintf(c.stderr, "drain: batch %d, nak %d%%, MaxAckPending %d\n", batch, nak, mp)
				r, err := c.run(ctx, "drain", dir, "-batch", strconv.Itoa(batch), "-nak", strconv.Itoa(nak), "-max-ack-pending", strconv.Itoa(mp))
				if err != nil {
					return fmt.Errorf("drain: %w", err)
				}
				d := r.Drain
				_, _ = fmt.Fprintf(w, "%d\t%d\t%d\t%.0f\t%s\t%d\t%d\t%d\t%t\t%.2f\t%s\t%s\t%.1f\t%.1f\t%.1f\t%.1f\t\n",
					batch, nak, mp, float64(d.Delivered)/d.Elapsed.Seconds(), d.Elapsed.Round(time.Millisecond),
					d.Delivered, d.Naked, d.AckPending, d.Stalled, r.Cores,
					mib(r.Heap), mib(r.MaxRSS),
					r.Shares["checkPending"], r.Shares["removeMsg"], r.Shares["removeMsgBlock"], r.Shares["loadMsgs"])
				_ = os.RemoveAll(dir)
			}
		}
	}
	return w.Flush()
}

func mib(b uint64) string { return fmt.Sprintf("%.0fMiB", float64(b)/(1<<20)) }
