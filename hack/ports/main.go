// Command ports checks from outside the cluster that the internet reaches
// a sniffarr release's sniff and probes at their NodePorts or hostPorts, and
// that the DHT knows them there. It reads the node ID and the seeded torrents
// from the API, the ports and addresses from Kubernetes, and prints one line
// per check, exiting 1 when any fails.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"connectrpc.com/connect"

	"sarak.sh/mikluko/sniffarr/hack/internal/cluster"
	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
)

var (
	// errUsage is returned once the flag package has reported a usage error.
	errUsage = errors.New("usage")
	// errFailed is returned when a check failed; the results say which.
	errFailed = errors.New("failed")
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
	case errors.Is(err, errUsage):
		os.Exit(2)
	case errors.Is(err, errFailed):
		os.Exit(1)
	default:
		fmt.Fprintln(os.Stderr, "ports:", err)
		os.Exit(1)
	}
}

// options are the command-line flags.
type options struct {
	url       string
	context   string
	namespace string
	release   string
	addr      netip.Addr
	seeds     int
	minAge    time.Duration
	lookup    time.Duration
	timeout   time.Duration
}

func parse(args []string, stderr io.Writer) (options, error) {
	var o options
	fs := flag.NewFlagSet("ports", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(fs.Output(), "usage: ports [flags]")
		fs.PrintDefaults()
	}
	fs.StringVar(&o.url, "url", "", "base URL of the API, such as http://127.0.0.1:8080; empty port-forwards to the API in Kubernetes")
	fs.StringVar(&o.context, "context", "", "kube context; empty is the current one")
	fs.StringVar(&o.namespace, "namespace", "sniffarr", "namespace of the release")
	fs.StringVar(&o.namespace, "n", "sniffarr", "shorthand for --namespace")
	fs.StringVar(&o.release, "release", "sniffarr", "Helm release name")
	fs.Func("addr", "public IPv4 address of the nodes; empty takes each pod's node's ExternalIP, or else its public InternalIP", func(s string) error {
		a, err := netip.ParseAddr(s)
		if err != nil || !a.Is4() {
			return errors.New("want an IPv4 address")
		}
		o.addr = a
		return nil
	})
	fs.IntVar(&o.seeds, "seeds", 8, "how many seeded torrents to check, the newest old enough")
	fs.DurationVar(&o.minAge, "min-age", 35*time.Minute, "how long ago a torrent was probed at the latest, so that its probe announced it since")
	fs.DurationVar(&o.lookup, "lookup", 30*time.Second, "how long to search the DHT for one torrent's peers")
	fs.DurationVar(&o.timeout, "timeout", 3*time.Minute, "how long all the checks take at most")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, err
		}
		return o, errUsage
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintln(stderr, "ports takes no arguments")
		return o, errUsage
	}
	if o.seeds < 0 {
		_, _ = fmt.Fprintln(stderr, "-seeds: want 0 or more")
		return o, errUsage
	}
	return o, nil
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	o, err := parse(args, stderr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	k := cluster.Kube{Context: o.context, Namespace: o.namespace}
	ts, err := discover(ctx, k, o.release, o.addr)
	if err != nil {
		return err
	}
	c := checks{targets: ts, lookup: o.lookup}
	if err := c.fromAPI(ctx, o, k); err != nil {
		return err
	}
	n, err := newNode()
	if err != nil {
		return fmt.Errorf("dht: %w", err)
	}
	defer n.close()
	failed := 0
	for _, r := range c.run(ctx, n) {
		if _, err := fmt.Fprintln(stdout, r); err != nil {
			return err
		}
		if r.status == statusFail {
			failed++
		}
	}
	if failed > 0 {
		return errFailed
	}
	return nil
}

// fromAPI reads the sniff's stored node ID and the seeded torrents from the
// API at o.url, or through kubectl port-forward when it is empty.
func (c *checks) fromAPI(ctx context.Context, o options, k cluster.Kube) error {
	base := o.url
	if base == "" {
		f, err := cluster.PortForward(ctx, k, o.release+"-api")
		if err != nil {
			return err
		}
		defer f.Close()
		base = f.URL
	}
	u, err := url.Parse(base)
	if err != nil {
		return fmt.Errorf("--url: %w", err)
	}
	hc := cluster.HTTPClient(u)
	hk, err := sniffarrv1connect.NewIngestServiceClient(hc, base).HashKey(ctx, connect.NewRequest(&sniffarrv1.HashKeyRequest{}))
	if err != nil {
		return fmt.Errorf("hash key: %w", err)
	}
	if len(hk.Msg.GetNodeId()) != len(c.nodeID) {
		return fmt.Errorf("hash key: node ID of %d bytes, want %d", len(hk.Msg.GetNodeId()), len(c.nodeID))
	}
	copy(c.nodeID[:], hk.Msg.GetNodeId())
	c.seeds, err = seeds(ctx, sniffarrv1connect.NewBrowseServiceClient(hc, base), time.Now().Add(-o.minAge), o.seeds)
	return err
}
