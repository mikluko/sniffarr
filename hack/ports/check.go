package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/dht/v2/krpc"
)

// status is a check's verdict; skip is a check without evidence either way.
type status int

const (
	statusOK status = iota
	statusSkip
	statusFail
)

func (s status) String() string {
	return [...]string{"ok", "skip", "FAIL"}[s]
}

// result is one check's verdict about subject.
type result struct {
	status  status
	subject string
	detail  string
}

func (r result) String() string { return fmt.Sprintf("%-4s  %s: %s", r.status, r.subject, r.detail) }

// checks are the inputs of every check.
type checks struct {
	targets []target
	// nodeID is the sniff's node ID as the ingest API stores it, before
	// the sniff gives it the BEP 42 prefix of its public address.
	nodeID [20]byte
	seeds  [][20]byte
	// lookup bounds the DHT traversal for one seed's peers.
	lookup time.Duration
}

// run runs every check through n and returns the results: the sniff's,
// then each probe's, then each seed's.
func (c checks) run(ctx context.Context, n *node) []result {
	var sniffs, probes []target
	for _, t := range c.targets {
		if t.component == componentSniff {
			sniffs = append(sniffs, t)
		} else {
			probes = append(probes, t)
		}
	}
	var (
		wg       sync.WaitGroup
		sniffRs  []result
		probeRs  []result
		served   [][]target
		announce [][]netip.AddrPort
	)
	wg.Go(func() { sniffRs = c.sniffs(ctx, n, sniffs) })
	wg.Go(func() { probeRs, served = c.probes(ctx, probes) })
	wg.Go(func() { announce = c.announced(ctx, n) })
	wg.Wait()
	return append(append(sniffRs, probeRs...), judgeSeeds(c.seeds, announce, served, probes)...)
}

// sniffs checks every sniff: its node ID over UDP, TCP, and the address
// other DHT nodes list it at.
func (c checks) sniffs(ctx context.Context, n *node, sniffs []target) []result {
	var wg sync.WaitGroup
	rs := make([][3]result, len(sniffs))
	for i, t := range sniffs {
		wg.Go(func() {
			id, err := n.ping(ctx, t.udpAddr())
			rs[i][0] = judgeSniffID(t, id, err, c.nodeID)
		})
		wg.Go(func() { rs[i][1] = judgeReach(t, reach(ctx, t.tcpAddr())) })
		wg.Go(func() {
			rs[i][2] = judgeContacts(t, contacts(ctx, n.findNode, n.routingTable(), secured(c.nodeID, t.addr)))
		})
	}
	wg.Wait()
	var out []result
	for _, r := range rs {
		out = append(out, r[:]...)
	}
	return out
}

// probes checks every probe over TCP and returns the results with, for
// each seed, the probes that serve it.
func (c checks) probes(ctx context.Context, probes []target) ([]result, [][]target) {
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		out     = make([]result, len(probes))
		served  = make([][]target, len(c.seeds))
		counts  = make([]int, len(probes))
		peerID  = newPeerID()
		serving = func(i, j int) {
			mu.Lock()
			defer mu.Unlock()
			served[j] = append(served[j], probes[i])
			counts[i]++
		}
	)
	for i, t := range probes {
		wg.Go(func() {
			err := reach(ctx, t.tcpAddr())
			promiscuous := false
			if err == nil {
				promiscuous, _ = handshake(ctx, t.tcpAddr(), randomHash(), peerID)
			}
			if err == nil && !promiscuous {
				var seeds sync.WaitGroup
				for j, h := range c.seeds {
					seeds.Go(func() {
						if ok, _ := handshake(ctx, t.tcpAddr(), h, peerID); ok {
							serving(i, j)
						}
					})
				}
				seeds.Wait()
			}
			mu.Lock()
			defer mu.Unlock()
			out[i] = judgeProbe(t, err, promiscuous, counts[i], len(c.seeds))
		})
	}
	wg.Wait()
	return out, served
}

// announced returns, for each seed, the peers the DHT holds for it.
func (c checks) announced(ctx context.Context, n *node) [][]netip.AddrPort {
	var wg sync.WaitGroup
	out := make([][]netip.AddrPort, len(c.seeds))
	for j, h := range c.seeds {
		wg.Go(func() { out[j], _ = n.peers(ctx, h, c.lookup) })
	}
	wg.Wait()
	return out
}

// secured is id with the BEP 42 prefix of addr, as the sniff at addr
// prefixes the stored ID.
func secured(id [20]byte, addr netip.Addr) [20]byte {
	dht.SecureNodeId((*krpc.ID)(&id), addr.AsSlice())
	return id
}

// reach connects to addr over TCP and hangs up.
func reach(ctx context.Context, addr netip.AddrPort) error {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", addr.String())
	if err != nil {
		return err
	}
	return c.Close()
}

// randomHash is an infohash no torrent has.
func randomHash() [20]byte {
	var h [20]byte
	_, _ = rand.Read(h[:])
	return h
}

// newPeerID is a random BitTorrent peer ID in Azureus style.
func newPeerID() [20]byte {
	var id [20]byte
	copy(id[:], "-SFPORT-")
	_, _ = rand.Read(id[8:])
	return id
}

// judgeSniffID judges the node ID the sniff at t answered a ping with,
// against id, the stored one, which the sniff prefixes for t's address.
func judgeSniffID(t target, got [20]byte, err error, id [20]byte) result {
	r := result{subject: t.name + " " + t.udpAddr().String() + "/udp"}
	want := secured(id, t.addr)
	switch {
	case err != nil:
		r.status, r.detail = statusFail, "no answer to ping: "+err.Error()
	case got == want:
		r.status, r.detail = statusOK, fmt.Sprintf("answers as %x, the stored ID with the BEP 42 prefix of %s", got, t.addr)
	case got == id:
		r.status, r.detail = statusFail, fmt.Sprintf("answers as %x, the stored ID without the BEP 42 prefix of %s: STUN reported another address, or none", got, t.addr)
	default:
		r.status, r.detail = statusFail, fmt.Sprintf("answers as %x, not this release's sniff, %x", got, want)
	}
	return r
}

// judgeReach judges a TCP connection to t.
func judgeReach(t target, err error) result {
	r := result{subject: t.name + " " + t.tcpAddr().String() + "/tcp"}
	if err != nil {
		r.status, r.detail = statusFail, err.Error()
	} else {
		r.status, r.detail = statusOK, "accepts connections"
	}
	return r
}

// judgeContacts judges the addresses other DHT nodes list the sniff at t
// at: the NodePort, unless its queries leave from another port.
func judgeContacts(t target, seen []netip.AddrPort) result {
	r := result{subject: t.name + " as others list it"}
	var other []string
	for _, ap := range seen {
		if ap != t.udpAddr() {
			other = append(other, ap.String())
		}
	}
	switch {
	case len(seen) == 0:
		r.status, r.detail = statusSkip, "no node the lookup asked lists it"
	case len(other) > 0:
		r.status, r.detail = statusFail, fmt.Sprintf("at %s, not %s: its queries leave from another port than the NodePort", strings.Join(other, ", "), t.udpAddr())
	default:
		r.status, r.detail = statusOK, fmt.Sprintf("at %s", t.udpAddr())
	}
	return r
}

// judgeProbe judges the probe at t: reachable over TCP, on the same port
// number as over UDP, since peers dial the port the DHT announces, and
// refusing a torrent it does not hold, else serving proves nothing.
func judgeProbe(t target, err error, promiscuous bool, served, seeds int) result {
	r := judgeReach(t, err)
	switch {
	case err != nil:
	case promiscuous:
		r.status, r.detail = statusFail, "answers a handshake for a random infohash"
	case t.tcp != t.udp:
		r.status, r.detail = statusFail, fmt.Sprintf("TCP NodePort %d differs from UDP %d, the port the DHT announces", t.tcp, t.udp)
	default:
		r.detail += fmt.Sprintf(", serves %d of %d seeds", served, seeds)
	}
	return r
}

// judgeSeeds judges, for every seed, the peers the DHT holds at the
// probes' addresses: each must be at a probe's NodePort. It fails too when
// no seed is announced at all.
func judgeSeeds(seeds [][20]byte, announced [][]netip.AddrPort, served [][]target, probes []target) []result {
	ports := map[netip.AddrPort]string{}
	for _, p := range probes {
		ports[p.udpAddr()] = p.name
	}
	var out []result
	seen := false
	for i, h := range seeds {
		r := result{subject: "seed " + hex.EncodeToString(h[:])}
		var at, bad []string
		for _, ap := range announced[i] {
			if !slices.ContainsFunc(probes, func(p target) bool { return p.addr == ap.Addr() }) {
				continue
			}
			if name, ok := ports[ap]; ok {
				at = append(at, ap.String()+" ("+name+")")
			} else {
				bad = append(bad, ap.String())
			}
		}
		by := "no probe"
		if len(served[i]) > 0 {
			by = names(served[i])
		}
		switch {
		case len(bad) > 0:
			r.status, r.detail = statusFail, fmt.Sprintf("announced at %s, no probe's NodePort; served by %s", strings.Join(bad, ", "), by)
		case len(at) > 0:
			r.status, r.detail = statusOK, fmt.Sprintf("announced at %s, served by %s", strings.Join(at, ", "), by)
		default:
			r.status, r.detail = statusSkip, "not announced from the probes' addresses; served by "+by
		}
		seen = seen || len(at)+len(bad) > 0
		out = append(out, r)
	}
	switch {
	case len(seeds) == 0:
		out = append(out, result{statusSkip, "seeds", "no seeded probe old enough in the journal"})
	case !seen:
		out = append(out, result{statusFail, "seeds", fmt.Sprintf("none of %d announced from the probes' addresses", len(seeds))})
	}
	return out
}

// names joins the names of ts.
func names(ts []target) string {
	ns := make([]string, len(ts))
	for i, t := range ts {
		ns[i] = t.name
	}
	return strings.Join(ns, ", ")
}
