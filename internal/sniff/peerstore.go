package sniff

import (
	"container/list"
	"math/rand/v2"
	"net/netip"
	"sync"
	"time"

	"github.com/anacrolix/dht/v2/krpc"
	peer_store "github.com/anacrolix/dht/v2/peer-store"
)

const (
	DefaultPeerTTL         = 30 * time.Minute
	DefaultStoreInfohashes = 200000
	DefaultStorePeers      = 100
	DefaultStoreReply      = 50
	// DefaultSeedMarkTTL is how long an announce_peer's seed flag waits for
	// the node to accept the announce.
	DefaultSeedMarkTTL = 30 * time.Second
)

// seedMarks is the most seed flags waiting at once; past it the expired
// ones go, and failing that all of them.
const seedMarks = 4096

// peerStoreConfig tunes a peerStore. Zero fields take the default named in
// their doc.
type peerStoreConfig struct {
	// TTL is how long an announced peer is served; DefaultPeerTTL.
	TTL time.Duration
	// Infohashes is the most infohashes held; DefaultStoreInfohashes. A
	// new one past it evicts the infohash announced least recently.
	Infohashes int
	// Peers is the most peers held per infohash; DefaultStorePeers. A new
	// one past it evicts the peer announced least recently.
	Peers int
	// Reply is the most peers GetPeers returns; DefaultStoreReply.
	Reply int
	// Now is time.Now when nil.
	Now func() time.Time
}

// peerStore is the DHT node's peer store: the peers announced to it per
// infohash, one per IP by BEP 33, served for TTL and bounded in memory.
// Nothing is persisted. It is safe for concurrent use.
type peerStore struct {
	cfg peerStoreConfig

	mu sync.Mutex
	// swarms indexes the infohashes held; order lists them by last
	// announce, oldest first; keys lists them for random sampling.
	swarms map[peer_store.InfoHash]*entry
	order  *list.List
	keys   []peer_store.InfoHash
	peers  int
	// seeds are the seed flags of announce_peer queries seen on the socket,
	// waiting for the node to accept the announce.
	seeds map[seedKey]time.Time
}

// entry is the peers of one infohash.
type entry struct {
	hash  peer_store.InfoHash
	peers map[netip.Addr]peer
	last  time.Time
	elem  *list.Element
	idx   int
}

// peer is one announced peer.
type peer struct {
	port uint16
	seed bool
	at   time.Time
}

type seedKey struct {
	hash peer_store.InfoHash
	ip   netip.Addr
}

func newPeerStore(cfg peerStoreConfig) *peerStore {
	orDefault(&cfg.TTL, DefaultPeerTTL)
	orDefault(&cfg.Infohashes, DefaultStoreInfohashes)
	orDefault(&cfg.Peers, DefaultStorePeers)
	orDefault(&cfg.Reply, DefaultStoreReply)
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &peerStore{cfg: cfg, swarms: map[peer_store.InfoHash]*entry{}, order: list.New(), seeds: map[seedKey]time.Time{}}
}

// AddPeer records na as announced for ih now, a seed when MarkSeed said so
// since DefaultSeedMarkTTL ago. An address that is not an IP is dropped.
func (s *peerStore) AddPeer(ih peer_store.InfoHash, na krpc.NodeAddr) {
	ip, ok := netip.AddrFromSlice(na.IP)
	if !ok {
		return
	}
	ip = ip.Unmap()
	now := s.cfg.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	seed := s.takeSeed(seedKey{ih, ip}, now)
	w, ok := s.swarms[ih]
	if !ok {
		if len(s.swarms) >= s.cfg.Infohashes {
			s.drop(s.order.Front().Value.(*entry))
		}
		w = &entry{hash: ih, peers: map[netip.Addr]peer{}, idx: len(s.keys)}
		w.elem = s.order.PushBack(w)
		s.swarms[ih] = w
		s.keys = append(s.keys, ih)
	}
	if _, ok := w.peers[ip]; !ok {
		if len(w.peers) >= s.cfg.Peers {
			s.evictOldest(w)
		}
		s.peers++
	}
	w.peers[ip] = peer{port: uint16(na.Port), seed: seed, at: now}
	w.last = now
	s.order.MoveToBack(w.elem)
}

// MarkSeed notes that ip announced ih as a seed; the next AddPeer of the
// pair within DefaultSeedMarkTTL records a seed.
func (s *peerStore) MarkSeed(ih peer_store.InfoHash, ip netip.Addr) {
	now := s.cfg.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.seeds) >= seedMarks {
		for k, at := range s.seeds {
			if now.Sub(at) > DefaultSeedMarkTTL {
				delete(s.seeds, k)
			}
		}
		if len(s.seeds) >= seedMarks {
			clear(s.seeds)
		}
	}
	s.seeds[seedKey{ih, ip.Unmap()}] = now
}

// takeSeed consumes the seed mark of k, reporting whether one stood.
func (s *peerStore) takeSeed(k seedKey, now time.Time) bool {
	at, ok := s.seeds[k]
	if !ok {
		return false
	}
	delete(s.seeds, k)
	return now.Sub(at) <= DefaultSeedMarkTTL
}

// GetPeers returns up to Reply peers announced for ih within TTL, in no
// particular order.
func (s *peerStore) GetPeers(ih peer_store.InfoHash) []krpc.NodeAddr {
	now := s.cfg.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.swarms[ih]
	if !ok {
		return nil
	}
	var out []krpc.NodeAddr
	for ip, p := range w.peers {
		if now.Sub(p.at) > s.cfg.TTL {
			continue
		}
		out = append(out, krpc.NodeAddr{IP: ip.AsSlice(), Port: int(p.port)})
		if len(out) == s.cfg.Reply {
			break
		}
	}
	return out
}

// Filters returns the BEP 33 bloom filters of the seeds and of the other
// peers announced for ih within TTL.
func (s *peerStore) Filters(ih peer_store.InfoHash) (seeds, downloaders krpc.ScrapeBloomFilter) {
	now := s.cfg.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.swarms[ih]
	if !ok {
		return
	}
	for ip, p := range w.peers {
		if now.Sub(p.at) > s.cfg.TTL {
			continue
		}
		if p.seed {
			seeds.AddIp(ip.AsSlice())
		} else {
			downloaders.AddIp(ip.AsSlice())
		}
	}
	return
}

// Estimate returns the seeds and the other peers of ih the filters of
// Filters count; an empty filter counts 0, where BEP 33's formula gives
// 0.5.
func (s *peerStore) Estimate(ih peer_store.InfoHash) (seeds, downloaders float64) {
	sd, pe := s.Filters(ih)
	return estimate(sd), estimate(pe)
}

func estimate(f krpc.ScrapeBloomFilter) float64 {
	if f == (krpc.ScrapeBloomFilter{}) {
		return 0
	}
	return f.EstimateCount()
}

// Sample returns up to n distinct infohashes held, chosen at random.
func (s *peerStore) Sample(n int) [][20]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	n = min(n, len(s.keys))
	out := make([][20]byte, 0, n)
	if n == len(s.keys) {
		for _, k := range s.keys {
			out = append(out, k)
		}
		return out
	}
	picked := make(map[int]bool, n)
	for len(out) < n {
		i := rand.IntN(len(s.keys))
		if picked[i] {
			continue
		}
		picked[i] = true
		out = append(out, s.keys[i])
	}
	return out
}

// Count returns how many infohashes and how many peers are held.
func (s *peerStore) Count() (infohashes, peers int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.swarms), s.peers
}

// Sweep drops the peers announced longer than TTL ago and the infohashes
// left with none.
func (s *peerStore) Sweep() {
	now := s.cfg.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for e := s.order.Front(); e != nil; {
		w := e.Value.(*entry)
		e = e.Next()
		if now.Sub(w.last) > s.cfg.TTL {
			s.drop(w)
			continue
		}
		for ip, p := range w.peers {
			if now.Sub(p.at) > s.cfg.TTL {
				delete(w.peers, ip)
				s.peers--
			}
		}
	}
	for k, at := range s.seeds {
		if now.Sub(at) > DefaultSeedMarkTTL {
			delete(s.seeds, k)
		}
	}
}

// drop forgets w and its peers.
func (s *peerStore) drop(w *entry) {
	s.peers -= len(w.peers)
	s.order.Remove(w.elem)
	last := len(s.keys) - 1
	moved := s.keys[last]
	s.keys[w.idx] = moved
	s.swarms[moved].idx = w.idx
	s.keys = s.keys[:last]
	delete(s.swarms, w.hash)
}

// evictOldest forgets the peer of w announced least recently.
func (s *peerStore) evictOldest(w *entry) {
	var oldest netip.Addr
	var at time.Time
	first := true
	for ip, p := range w.peers {
		if first || p.at.Before(at) {
			oldest, at, first = ip, p.at, false
		}
	}
	delete(w.peers, oldest)
	s.peers--
}
