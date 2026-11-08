package main

import (
	"context"
	"errors"
	"maps"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/dht/v2/int160"
	"github.com/anacrolix/dht/v2/krpc"
)

// queryTimeout bounds one DHT query.
const queryTimeout = 5 * time.Second

// Lookup shape: how many nodes each round queries and how many rounds.
const (
	lookupWidth  = 8
	lookupRounds = 10
)

// node is a passive DHT node on an IPv4 socket that queries others.
type node struct{ s *dht.Server }

// newNode starts a passive node on udp4 and bootstraps it. IPv4 only: on
// macOS a dual-stack socket fails reading after sending to IPv4 nodes.
func newNode() (*node, error) {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, err
	}
	cfg := dht.NewDefaultServerConfig()
	cfg.Conn = conn
	cfg.Passive = true
	s, err := dht.NewServer(cfg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if _, err := s.Bootstrap(); err != nil {
		s.Close()
		return nil, err
	}
	return &node{s: s}, nil
}

func (n *node) close() { n.s.Close() }

// query sends q with args to addr and returns the reply.
func (n *node) query(ctx context.Context, addr netip.AddrPort, q string, args krpc.MsgArgs) (*krpc.Return, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	res := n.s.Query(ctx, dht.NewAddr(net.UDPAddrFromAddrPort(addr)), q, dht.QueryInput{MsgArgs: args})
	if err := res.ToError(); err != nil {
		return nil, err
	}
	if res.Reply.R == nil {
		return nil, errors.New("reply without a return")
	}
	return res.Reply.R, nil
}

// ping returns the node ID addr answers a ping with.
func (n *node) ping(ctx context.Context, addr netip.AddrPort) ([20]byte, error) {
	r, err := n.query(ctx, addr, "ping", krpc.MsgArgs{})
	if err != nil {
		return [20]byte{}, err
	}
	return r.ID, nil
}

func (n *node) findNode(ctx context.Context, addr netip.AddrPort, target [20]byte) ([]krpc.NodeInfo, error) {
	r, err := n.query(ctx, addr, "find_node", krpc.MsgArgs{Target: target})
	if err != nil {
		return nil, err
	}
	var out []krpc.NodeInfo
	r.ForAllNodes(func(ni krpc.NodeInfo) { out = append(out, ni) })
	return out, nil
}

func (n *node) routingTable() []krpc.NodeInfo { return n.s.Nodes() }

// peers returns the peers the DHT holds for h, sorted, traversing it for
// at most d.
func (n *node) peers(ctx context.Context, h [20]byte, d time.Duration) ([]netip.AddrPort, error) {
	a, err := n.s.AnnounceTraversal(h)
	if err != nil {
		return nil, err
	}
	defer a.Close()
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	var out []netip.AddrPort
	for {
		select {
		case pv, ok := <-a.Peers:
			if !ok {
				return unique(out), nil
			}
			for _, p := range pv.Peers {
				if ap, ok := addrPort(p); ok {
					out = append(out, ap)
				}
			}
		case <-ctx.Done():
			return unique(out), nil
		}
	}
}

func unique(aps []netip.AddrPort) []netip.AddrPort {
	slices.SortFunc(aps, netip.AddrPort.Compare)
	return slices.Compact(aps)
}

func addrPort(a krpc.NodeAddr) (netip.AddrPort, bool) {
	ip, ok := netip.AddrFromSlice(a.IP)
	if !ok || a.Port <= 0 || a.Port > 65535 {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(ip.Unmap(), uint16(a.Port)), true
}

// finder asks a DHT node at addr for the nodes it knows closest to target.
type finder func(ctx context.Context, addr netip.AddrPort, target [20]byte) ([]krpc.NodeInfo, error)

// contacts walks the DHT from start toward target, lookupWidth closest
// unasked nodes a round, and returns the addresses other nodes list for
// the node of ID target, each with the addresses of the nodes that listed
// it, sorted; start's own entries count as listed by the zero address.
func contacts(ctx context.Context, find finder, start []krpc.NodeInfo, target [20]byte) map[netip.AddrPort][]netip.AddrPort {
	t := int160.FromByteArray(target)
	known := map[netip.AddrPort][20]byte{}
	asked := map[netip.AddrPort]bool{}
	found := map[netip.AddrPort]map[netip.AddrPort]bool{}
	add := func(from netip.AddrPort, ni krpc.NodeInfo) {
		ap, ok := addrPort(ni.Addr)
		if !ok {
			return
		}
		if ni.ID == target {
			if found[ap] == nil {
				found[ap] = map[netip.AddrPort]bool{}
			}
			found[ap][from] = true
			return
		}
		known[ap] = ni.ID
	}
	for _, ni := range start {
		add(netip.AddrPort{}, ni)
	}
	for range lookupRounds {
		next := closest(known, asked, t, lookupWidth)
		if len(next) == 0 || ctx.Err() != nil {
			break
		}
		var (
			mu sync.Mutex
			wg sync.WaitGroup
		)
		for _, ap := range next {
			asked[ap] = true
			wg.Go(func() {
				nis, err := find(ctx, ap, target)
				if err != nil {
					return
				}
				mu.Lock()
				defer mu.Unlock()
				for _, ni := range nis {
					add(ap, ni)
				}
			})
		}
		wg.Wait()
	}
	out := make(map[netip.AddrPort][]netip.AddrPort, len(found))
	for ap, from := range found {
		out[ap] = slices.SortedFunc(maps.Keys(from), netip.AddrPort.Compare)
	}
	return out
}

// closest returns up to n addresses of known not yet asked, nearest to t
// first.
func closest(known map[netip.AddrPort][20]byte, asked map[netip.AddrPort]bool, t int160.T, n int) []netip.AddrPort {
	var out []netip.AddrPort
	for ap := range known {
		if !asked[ap] {
			out = append(out, ap)
		}
	}
	slices.SortFunc(out, func(a, b netip.AddrPort) int {
		da, db := int160.FromByteArray(known[a]).Distance(t), int160.FromByteArray(known[b]).Distance(t)
		return da.Cmp(db)
	})
	return out[:min(n, len(out))]
}
