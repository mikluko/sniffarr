package sniff

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/time/rate"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
)

const (
	DefaultPort         = 42069
	DefaultRate         = 2000
	DefaultPeerWait     = 8 * time.Second
	DefaultMinPeers     = 1
	DefaultFetchTimeout = 15 * time.Second
	DefaultWalkers      = 16
)

// NetworkConfig tunes the DHT node and the metadata fetches. Zero fields
// take the default named in their doc.
type NetworkConfig struct {
	// Port is the TCP and UDP port for peers and the DHT; 0 has the system
	// pick a free one.
	Port int
	// UPnP asks the gateways found on the local network to forward the port
	// the client listens on, TCP and UDP, to it.
	UPnP bool
	// Passive makes the DHT node answer no query and ask other nodes to keep
	// it out of their routing tables, by BEP 43. A passive node receives no
	// announce_peer.
	Passive bool
	// Rate caps both DHT queries and new peer connections per second;
	// DefaultRate. The libraries' own limits, 250 queries and 10 connections
	// a second, starve thousands of concurrent fetches.
	Rate int
	// Fetchers is how many fetches run at once; it sizes the half-open
	// connection limit. DefaultFetchers.
	Fetchers int
	// PeerWait is how long a fetch looks for peers on the DHT;
	// DefaultPeerWait.
	PeerWait time.Duration
	// MinPeers is how many distinct peers a fetch must find within PeerWait
	// to ask them for metadata, or it fails with ErrNoPeers; DefaultMinPeers.
	MinPeers int
	// FetchTimeout is how long a fetch waits for metadata once it has peers;
	// DefaultFetchTimeout.
	FetchTimeout time.Duration
	// Meter records metrics; the global MeterProvider when nil.
	Meter metric.MeterProvider
}

func (c *NetworkConfig) defaults() {
	orDefault(&c.Rate, DefaultRate)
	orDefault(&c.Fetchers, DefaultFetchers)
	orDefault(&c.PeerWait, DefaultPeerWait)
	orDefault(&c.MinPeers, DefaultMinPeers)
	orDefault(&c.FetchTimeout, DefaultFetchTimeout)
}

// Network is a BitTorrent client with its own DHT node that fetches torrent
// metadata only. It does not upload, seed, announce its torrents, exchange
// peers, or store announced peers.
type Network struct {
	cfg     NetworkConfig
	client  *torrent.Client
	dht     *dht.Server
	swarm   swarm
	answers metric.Int64Counter
}

// swarm reaches the peers of a torrent.
type swarm interface {
	// lookup starts looking for the peers of h, which arrive on peers until
	// stop is called.
	lookup(h Hash) (peers <-chan dht.PeersValues, stop func(), err error)
	// fetch gets the metadata of h from peers, or fails with ErrNoMetadata.
	// It forgets the peers before it returns.
	fetch(ctx context.Context, h Hash, peers []torrent.PeerInfo) (Metadata, error)
}

// clientSwarm is the swarm of a torrent client and its DHT node.
type clientSwarm struct {
	client  *torrent.Client
	dht     *dht.Server
	timeout time.Duration
}

// lookup sends get_peers only, never announce_peer.
func (s clientSwarm) lookup(h Hash) (<-chan dht.PeersValues, func(), error) {
	a, err := s.dht.AnnounceTraversal(h)
	if err != nil {
		return nil, nil, err
	}
	return a.Peers, a.Close, nil
}

// fetch waits for the metadata for timeout at most.
func (s clientSwarm) fetch(ctx context.Context, h Hash, peers []torrent.PeerInfo) (Metadata, error) {
	t, _ := s.client.AddTorrentInfoHash(metainfo.Hash(h))
	defer t.Drop()
	t.AddPeers(peers)
	timer := time.NewTimer(s.timeout)
	defer timer.Stop()
	select {
	case <-t.GotInfo():
	case <-timer.C:
		return Metadata{}, ErrNoMetadata
	case <-ctx.Done():
		return Metadata{}, ctx.Err()
	}
	info := t.Info()
	m := Metadata{Name: info.BestName(), Size: info.TotalLength()}
	for _, f := range info.UpvertedFiles() {
		m.Files = append(m.Files, File{Path: f.DisplayPath(info), Size: f.Length})
	}
	for _, c := range t.PeerConns() {
		if c.RemoteAddr == nil {
			continue
		}
		if addr, err := netip.ParseAddrPort(c.RemoteAddr.String()); err == nil {
			m.Peers = append(m.Peers, addr)
		}
	}
	return m, nil
}

// Start starts the client and its DHT node. offer, unless nil, is called
// with every infohash announced to the node, and peers, unless nil, takes
// the announcing peer through Announced. The client creates, reads and
// writes no torrent files.
func Start(cfg NetworkConfig, offer func(Hash, sniffarrv1.Source), peers PeerSink) (*Network, error) {
	cfg.defaults()
	client, err := torrent.NewClient(clientConfig(cfg, offer, peers))
	if err != nil {
		return nil, err
	}
	n := &Network{cfg: cfg, client: client}
	for _, d := range client.DhtServers() {
		if w, ok := d.(torrent.AnacrolixDhtServerWrapper); ok {
			n.dht = w.Server
			break
		}
	}
	if n.dht == nil {
		n.Close()
		return nil, errors.New("the torrent client has no DHT node")
	}
	n.swarm = clientSwarm{client: client, dht: n.dht, timeout: cfg.FetchTimeout}
	if err := n.observe(meter(cfg.Meter)); err != nil {
		n.Close()
		return nil, err
	}
	return n, nil
}

// clientConfig is the configuration of the torrent client and its DHT node
// that Start starts for cfg, offer and peers.
func clientConfig(cfg NetworkConfig, offer func(Hash, sniffarrv1.Source), peers PeerSink) *torrent.ClientConfig {
	c := torrent.NewDefaultClientConfig()
	c.DefaultStorage = noStorage{}
	c.NoUpload = true
	c.Seed = false
	c.DisablePEX = true
	c.DisableTrackers = true
	c.DisableWebseeds = true
	c.DisableWebtorrent = true
	c.ListenPort = cfg.Port
	c.NoDefaultPortForwarding = !cfg.UPnP
	c.PeriodicallyAnnounceTorrentsToDht = false
	c.TotalHalfOpenConns = 4 * cfg.Fetchers
	c.DialRateLimiter = rate.NewLimiter(rate.Limit(cfg.Rate), max(cfg.Rate/10, 1))
	c.ConfigureAnacrolixDhtServer = func(sc *dht.ServerConfig) {
		sc.SendLimiter = rate.NewLimiter(rate.Limit(cfg.Rate), max(cfg.Rate/10, 1))
		sc.PeerStore = nil
		sc.Passive = cfg.Passive
		if offer != nil {
			sc.OnAnnouncePeer = func(h metainfo.Hash, ip net.IP, port int, portOk bool) {
				offer(Hash(h), sniffarrv1.Source_SOURCE_ANNOUNCE)
				if addr, ok := netip.AddrFromSlice(ip); ok && portOk && peers != nil {
					peers.Announced(Hash(h), netip.AddrPortFrom(addr.Unmap(), uint16(port)))
				}
			}
		}
	}
	c.Slogger = slog.New(slog.DiscardHandler)
	return c
}

func (n *Network) observe(m metric.Meter) error {
	n.answers = counter(m, "sniffarr.dht.sample_answers", "{answer}", "sample_infohashes queries answered.")
	_, err := m.Int64ObservableGauge("sniffarr.dht.nodes", metric.WithUnit("{node}"),
		metric.WithDescription("Nodes in the DHT routing table."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(n.dht.NumNodes()))
			return nil
		}))
	return err
}

// Bootstrap populates the DHT routing table from the bootstrap nodes.
func (n *Network) Bootstrap(ctx context.Context) error {
	_, err := n.dht.BootstrapContext(ctx)
	return err
}

// Port returns the port the client listens on.
func (n *Network) Port() int {
	return n.client.LocalPort()
}

// Nodes returns how many DHT nodes the node knows.
func (n *Network) Nodes() int {
	return n.dht.NumNodes()
}

// Close stops the client, waiting a few seconds at most.
func (n *Network) Close() {
	done := make(chan struct{})
	go func() {
		n.client.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

// Sample walks the DHT with BEP 51 sample_infohashes queries for random
// targets, following the nodes each answer names, and calls found with
// every sampled infohash until ctx is done. walkers queries run at once,
// DefaultWalkers when it is zero.
func (n *Network) Sample(ctx context.Context, walkers int, found func(Hash)) {
	orDefault(&walkers, DefaultWalkers)
	frontier := make(chan krpc.NodeAddr, 50000)
	asked := newRecent[string](1000000)
	push := func(a krpc.NodeAddr) {
		if !asked.add(a.String()) {
			return
		}
		select {
		case frontier <- a:
		default:
		}
	}
	reseed := func() {
		for _, node := range n.dht.Nodes() {
			push(node.Addr)
		}
	}
	reseed()
	var wg sync.WaitGroup
	for range walkers {
		wg.Go(func() {
			for {
				var a krpc.NodeAddr
				select {
				case <-ctx.Done():
					return
				case a = <-frontier:
				case <-time.After(5 * time.Second):
					reseed()
					continue
				}
				n.sampleNode(ctx, a, found, push)
			}
		})
	}
	wg.Wait()
}

func (n *Network) sampleNode(ctx context.Context, a krpc.NodeAddr, found func(Hash), push func(krpc.NodeAddr)) {
	var target krpc.ID
	_, _ = rand.Read(target[:])
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res := n.dht.Query(qctx, dht.NewAddr(a.UDP()), "sample_infohashes", dht.QueryInput{
		MsgArgs:  krpc.MsgArgs{Target: target},
		NumTries: 1,
	})
	if res.Err != nil || res.Reply.R == nil {
		return
	}
	n.answers.Add(ctx, 1)
	r := res.Reply.R
	if r.Samples != nil {
		for _, h := range *r.Samples {
			found(Hash(h))
		}
	}
	r.ForAllNodes(func(ni krpc.NodeInfo) { push(ni.Addr) })
}

// Metadata finds the torrent's peers on the DHT and gets its metadata from
// them, failing with ErrNoPeers when it finds fewer than MinPeers. The peers'
// addresses are dropped with the torrent before Metadata returns, save in
// Metadata.Peers.
func (n *Network) Metadata(ctx context.Context, h Hash) (Metadata, error) {
	peers := n.findPeers(ctx, h)
	if len(peers) < n.cfg.MinPeers {
		return Metadata{}, ErrNoPeers
	}
	m, err := n.swarm.fetch(ctx, h, peers)
	if err != nil {
		return m, err
	}
	seen := make(map[netip.AddrPort]bool, len(peers)+len(m.Peers))
	all := make([]netip.AddrPort, 0, len(peers)+len(m.Peers))
	for _, a := range m.Peers {
		if !seen[a] {
			seen[a] = true
			all = append(all, a)
		}
	}
	for _, p := range peers {
		if a, ok := p.Addr.(netip.AddrPort); ok && !seen[a] {
			seen[a] = true
			all = append(all, a)
		}
	}
	m.Peers = all
	return m, nil
}

// enoughPeers is how many peers findPeers looks for when MinPeers is fewer.
const enoughPeers = 20

// findPeers looks for distinct peers of the torrent for at most PeerWait,
// and until it has max(MinPeers, enoughPeers).
func (n *Network) findPeers(ctx context.Context, h Hash) []torrent.PeerInfo {
	batches, stop, err := n.swarm.lookup(h)
	if err != nil {
		return nil
	}
	defer stop()
	want := max(n.cfg.MinPeers, enoughPeers)
	var peers []torrent.PeerInfo
	seen := map[netip.AddrPort]bool{}
	timer := time.NewTimer(n.cfg.PeerWait)
	defer timer.Stop()
	for {
		select {
		case v, ok := <-batches:
			if !ok {
				return peers
			}
			for _, p := range v.Peers {
				ip, ok := netip.AddrFromSlice(p.IP)
				if !ok {
					continue
				}
				addr := netip.AddrPortFrom(ip.Unmap(), uint16(p.Port))
				if seen[addr] {
					continue
				}
				seen[addr] = true
				peers = append(peers, torrent.PeerInfo{Addr: addr, Source: torrent.PeerSourceDhtGetPeers})
			}
			if len(peers) >= want {
				return peers
			}
		case <-timer.C:
			return peers
		case <-ctx.Done():
			return peers
		}
	}
}
