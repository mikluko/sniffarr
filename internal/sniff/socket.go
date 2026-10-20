package sniff

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/anacrolix/dht/v2/krpc"
	peer_store "github.com/anacrolix/dht/v2/peer-store"
	"github.com/anacrolix/torrent/bencode"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/time/rate"
)

const (
	// sampleSize is the most infohashes a sample_infohashes reply carries.
	sampleSize = 20
	// sampleNodes is the most nodes a sample_infohashes reply names per
	// address family.
	sampleNodes = 8
	// sampleInterval is the most seconds BEP 51 lets a reply ask an
	// indexer to wait.
	sampleInterval = 21600
	// scrapeTTL is how long a get_peers query with scrape waits for the
	// node's reply.
	scrapeTTL = 10 * time.Second
	// scrapes is the most such queries waiting at once.
	scrapes = 4096
	// sampleSources is the most sources whose sample_infohashes rate is
	// tracked at once.
	sampleSources = 10000
)

// sampleLimit is how often one source gets a sample_infohashes reply.
var sampleLimit = rate.Every(10 * time.Second)

// sampleBurst is how many sample_infohashes replies a source gets at once.
const sampleBurst = 2

// socket is the DHT node's UDP socket between the node and the network. It
// answers sample_infohashes itself, by BEP 51; it adds the BEP 33 filters
// to the node's replies to get_peers queries with scrape set; and it tells
// the store the seed flag of announce_peer queries. Every other packet
// passes untouched. Nodes must be bound once the node exists for replies to
// name nodes.
type socket struct {
	net.PacketConn
	id      krpc.ID
	store   *peerStore
	limit   *perSource
	replies metric.Int64Counter

	mu    sync.Mutex
	nodes func() []krpc.NodeInfo
	// pending are the get_peers queries with scrape waiting for the node's
	// reply, by source address, then transaction ID.
	pending map[string]map[string]scrape
}

// scrape is a get_peers query with scrape set.
type scrape struct {
	hash peer_store.InfoHash
	at   time.Time
}

func newSocket(conn net.PacketConn, id krpc.ID, store *peerStore, replies metric.Int64Counter) *socket {
	return &socket{
		PacketConn: conn,
		id:         id,
		store:      store,
		limit:      newPerSource(sampleLimit, sampleBurst, sampleSources),
		replies:    replies,
		pending:    map[string]map[string]scrape{},
	}
}

// bind gives the socket the node's routing table.
func (s *socket) bind(nodes func() []krpc.NodeInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes = nodes
}

// ReadFrom returns the next packet for the node.
func (s *socket) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		n, addr, err := s.PacketConn.ReadFrom(b)
		if err != nil {
			return n, addr, err
		}
		if s.inbound(b[:n], addr) {
			return n, addr, nil
		}
	}
}

// inbound reports whether the packet is for the node, having answered it
// or noted it otherwise. The byte strings looked for are the bencoding of
// the field each query carries and no other does.
func (s *socket) inbound(b []byte, addr net.Addr) bool {
	switch {
	case bytes.Contains(b, []byte("17:sample_infohashes")):
		return !s.sample(b, addr)
	case bytes.Contains(b, []byte("6:scrape")):
		s.noteScrape(b, addr)
	case bytes.Contains(b, []byte("4:seed")):
		s.noteSeed(b, addr)
	}
	return true
}

// WriteTo sends the node's packet, with the BEP 33 filters added to a
// reply to a get_peers query with scrape.
func (s *socket) WriteTo(b []byte, addr net.Addr) (int, error) {
	if out, ok := s.scraped(b, addr); ok {
		b = out
	}
	return s.PacketConn.WriteTo(b, addr)
}

// decode unmarshals a KRPC message, tolerating trailing bytes.
func decode(b []byte, v any) bool {
	err := bencode.Unmarshal(b, v)
	var trailing bencode.ErrUnusedTrailingBytes
	return err == nil || errors.As(err, &trailing)
}

// sample answers a sample_infohashes query and reports whether b was one.
// A source past its rate gets no answer.
func (s *socket) sample(b []byte, addr net.Addr) bool {
	var m krpc.Msg
	if !decode(b, &m) || m.Y != "q" || m.Q != "sample_infohashes" {
		return false
	}
	ap, err := netip.ParseAddrPort(addr.String())
	if err != nil {
		return true
	}
	if !s.limit.allow(ap.Addr().Unmap()) {
		return true
	}
	var target krpc.ID
	var want []krpc.Want
	if m.A != nil {
		target, want = m.A.Target, m.A.Want
	}
	infohashes, _ := s.store.Count()
	interval := min(int64(s.store.cfg.TTL/time.Second), sampleInterval)
	num := int64(infohashes)
	samples := krpc.CompactInfohashes{}
	for _, h := range s.store.Sample(sampleSize) {
		samples = append(samples, h)
	}
	r := &krpc.Return{ID: s.id, Bep51Return: krpc.Bep51Return{Interval: &interval, Num: &num, Samples: &samples}}
	r.Nodes, r.Nodes6 = s.closest(target, want, ap.Addr())
	out := krpc.Msg{T: m.T, Y: "r", R: r}
	out.IP.FromAddrPort(ap)
	if _, err := s.PacketConn.WriteTo(bencode.MustMarshal(out), addr); err == nil {
		s.replies.Add(context.Background(), 1, queryAttr("sample_infohashes"))
	}
	return true
}

// closest returns the nodes nearest target from the routing table, up to
// sampleNodes per family, the families chosen by want or, without it, by
// the source's, as BEP 32 has it.
func (s *socket) closest(target krpc.ID, want []krpc.Want, source netip.Addr) (v4 krpc.CompactIPv4NodeInfo, v6 krpc.CompactIPv6NodeInfo) {
	s.mu.Lock()
	nodes := s.nodes
	s.mu.Unlock()
	if nodes == nil {
		return nil, nil
	}
	want4, want6 := source.Unmap().Is4(), !source.Unmap().Is4()
	if len(want) > 0 {
		want4, want6 = false, false
		for _, w := range want {
			want4 = want4 || w == krpc.WantNodes
			want6 = want6 || w == krpc.WantNodes6
		}
	}
	all := nodes()
	sort.Slice(all, func(i, j int) bool { return closer(all[i].ID, all[j].ID, target) })
	for _, n := range all {
		ip, ok := netip.AddrFromSlice(n.Addr.IP)
		if !ok {
			continue
		}
		switch {
		case ip.Unmap().Is4() && want4 && len(v4) < sampleNodes:
			n.Addr.IP = ip.Unmap().AsSlice()
			v4 = append(v4, n)
		case !ip.Unmap().Is4() && want6 && len(v6) < sampleNodes:
			v6 = append(v6, n)
		}
	}
	return v4, v6
}

// closer reports whether a is nearer target than b by XOR distance.
func closer(a, b, target krpc.ID) bool {
	for i := range target {
		da, db := a[i]^target[i], b[i]^target[i]
		if da != db {
			return da < db
		}
	}
	return false
}

// noteScrape remembers a get_peers query with scrape set, for scraped to
// find the node's reply to it.
func (s *socket) noteScrape(b []byte, addr net.Addr) {
	var m krpc.Msg
	if !decode(b, &m) || m.Y != "q" || m.Q != "get_peers" || m.A == nil || m.A.Scrape == 0 {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.count() >= scrapes {
		s.expire(now)
		if s.count() >= scrapes {
			clear(s.pending)
		}
	}
	key := addr.String()
	if s.pending[key] == nil {
		s.pending[key] = map[string]scrape{}
	}
	s.pending[key][m.T] = scrape{hash: peer_store.InfoHash(m.A.InfoHash), at: now}
}

// count is how many queries are pending; the caller holds mu.
func (s *socket) count() int {
	n := 0
	for _, ts := range s.pending {
		n += len(ts)
	}
	return n
}

// expire forgets the queries pending for longer than scrapeTTL; the caller
// holds mu.
func (s *socket) expire(now time.Time) {
	for key, ts := range s.pending {
		for t, q := range ts {
			if now.Sub(q.at) > scrapeTTL {
				delete(ts, t)
			}
		}
		if len(ts) == 0 {
			delete(s.pending, key)
		}
	}
}

// scraped returns b, a packet for addr, with the BEP 33 filters added when
// it is the node's reply to a pending get_peers query with scrape, and
// reports whether it was.
func (s *socket) scraped(b []byte, addr net.Addr) ([]byte, bool) {
	key := addr.String()
	s.mu.Lock()
	ts := s.pending[key]
	s.mu.Unlock()
	if len(ts) == 0 {
		return nil, false
	}
	var m krpc.Msg
	if !decode(b, &m) || m.Y != "r" || m.R == nil {
		return nil, false
	}
	s.mu.Lock()
	q, ok := ts[m.T]
	if ok {
		delete(ts, m.T)
		if len(ts) == 0 {
			delete(s.pending, key)
		}
	}
	s.mu.Unlock()
	if !ok {
		return nil, false
	}
	sd, pe := s.store.Filters(q.hash)
	m.R.BFsd, m.R.BFpe = &sd, &pe
	s.replies.Add(context.Background(), 1, queryAttr("scrape"))
	return bencode.MustMarshal(m), true
}

// announcement is the part of an announce_peer query the store needs.
type announcement struct {
	Y string `bencode:"y"`
	Q string `bencode:"q"`
	A *struct {
		InfoHash krpc.ID `bencode:"info_hash"`
		Seed     int     `bencode:"seed"`
	} `bencode:"a"`
}

// noteSeed marks the source a seed of the infohash it announces with seed
// set, by BEP 33.
func (s *socket) noteSeed(b []byte, addr net.Addr) {
	var m announcement
	if !decode(b, &m) || m.Y != "q" || m.Q != "announce_peer" || m.A == nil || m.A.Seed != 1 {
		return
	}
	ap, err := netip.ParseAddrPort(addr.String())
	if err != nil {
		return
	}
	s.store.MarkSeed(peer_store.InfoHash(m.A.InfoHash), ap.Addr())
}

// perSource rate-limits per IP, tracking at most size IPs: past that, it
// starts over.
type perSource struct {
	limit rate.Limit
	burst int
	size  int

	mu sync.Mutex
	m  map[netip.Addr]*rate.Limiter
}

func newPerSource(limit rate.Limit, burst, size int) *perSource {
	return &perSource{limit: limit, burst: burst, size: size, m: map[netip.Addr]*rate.Limiter{}}
}

// allow reports whether ip may be answered now.
func (p *perSource) allow(ip netip.Addr) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	l, ok := p.m[ip]
	if !ok {
		if len(p.m) >= p.size {
			clear(p.m)
		}
		l = rate.NewLimiter(p.limit, p.burst)
		p.m[ip] = l
	}
	return l.Allow()
}

func queryAttr(q string) metric.AddOption {
	return metric.WithAttributes(attribute.String("sniffarr.dht.query", q))
}
