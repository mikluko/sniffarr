package sniffer

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"golang.org/x/time/rate"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
)

const (
	DefaultPort         = 42069
	DefaultRate         = 2000
	DefaultPeerWait     = 8 * time.Second
	DefaultFetchTimeout = 15 * time.Second
	DefaultWalkers      = 16
)

// NetworkConfig tunes the DHT node and the metadata fetches. Zero fields
// take the default named in their doc.
type NetworkConfig struct {
	// Port is the TCP and UDP port for peers and the DHT, forwarded by UPnP;
	// DefaultPort.
	Port int
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
	// FetchTimeout is how long a fetch waits for metadata once it has peers;
	// DefaultFetchTimeout.
	FetchTimeout time.Duration
}

func (c *NetworkConfig) defaults() {
	orDefault(&c.Port, DefaultPort)
	orDefault(&c.Rate, DefaultRate)
	orDefault(&c.Fetchers, DefaultFetchers)
	orDefault(&c.PeerWait, DefaultPeerWait)
	orDefault(&c.FetchTimeout, DefaultFetchTimeout)
}

// Network is a BitTorrent client with its own DHT node that fetches torrent
// metadata only. It does not upload, seed, announce its torrents, exchange
// peers, or store announced peers.
type Network struct {
	cfg     NetworkConfig
	client  *torrent.Client
	dht     *dht.Server
	dataDir string
}

// Start starts the client and its DHT node. offer, unless nil, is called
// with every infohash announced to the node.
func Start(cfg NetworkConfig, offer func(Hash, sniffarrv1.Source)) (*Network, error) {
	cfg.defaults()
	dataDir, err := os.MkdirTemp("", "sniffdht")
	if err != nil {
		return nil, err
	}
	c := torrent.NewDefaultClientConfig()
	c.DataDir = dataDir
	c.NoUpload = true
	c.Seed = false
	c.DisablePEX = true
	c.DisableTrackers = true
	c.DisableWebseeds = true
	c.DisableWebtorrent = true
	c.ListenPort = cfg.Port
	c.NoDefaultPortForwarding = false
	c.PeriodicallyAnnounceTorrentsToDht = false
	c.TotalHalfOpenConns = 4 * cfg.Fetchers
	c.DialRateLimiter = rate.NewLimiter(rate.Limit(cfg.Rate), max(cfg.Rate/10, 1))
	c.ConfigureAnacrolixDhtServer = func(sc *dht.ServerConfig) {
		sc.SendLimiter = rate.NewLimiter(rate.Limit(cfg.Rate), max(cfg.Rate/10, 1))
		sc.PeerStore = nil
		if offer != nil {
			sc.OnAnnouncePeer = func(h metainfo.Hash, _ net.IP, _ int, _ bool) {
				offer(Hash(h), sniffarrv1.Source_SOURCE_ANNOUNCE)
			}
		}
	}
	c.Slogger = slog.New(slog.DiscardHandler)
	client, err := torrent.NewClient(c)
	if err != nil {
		os.RemoveAll(dataDir)
		return nil, err
	}
	n := &Network{cfg: cfg, client: client, dataDir: dataDir}
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
	return n, nil
}

// Bootstrap populates the DHT routing table from the bootstrap nodes.
func (n *Network) Bootstrap(ctx context.Context) error {
	_, err := n.dht.BootstrapContext(ctx)
	return err
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
	os.RemoveAll(n.dataDir)
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
	r := res.Reply.R
	if r.Samples != nil {
		for _, h := range *r.Samples {
			found(Hash(h))
		}
	}
	r.ForAllNodes(func(ni krpc.NodeInfo) { push(ni.Addr) })
}

// Metadata finds the torrent's peers on the DHT and gets its metadata from
// them. The peers' addresses are dropped with the torrent before Metadata
// returns.
func (n *Network) Metadata(ctx context.Context, h Hash) (Metadata, error) {
	peers := n.findPeers(ctx, h)
	if len(peers) == 0 {
		return Metadata{}, ErrNoPeers
	}
	t, _ := n.client.AddTorrentInfoHash(metainfo.Hash(h))
	defer t.Drop()
	t.AddPeers(peers)
	timer := time.NewTimer(n.cfg.FetchTimeout)
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
	return m, nil
}

// findPeers asks the DHT for peers of the torrent, for at most PeerWait and
// at most 20 peers. It sends get_peers only, never announce_peer.
func (n *Network) findPeers(ctx context.Context, h Hash) []torrent.PeerInfo {
	a, err := n.dht.AnnounceTraversal(h)
	if err != nil {
		return nil
	}
	defer a.Close()
	var peers []torrent.PeerInfo
	timer := time.NewTimer(n.cfg.PeerWait)
	defer timer.Stop()
	for {
		select {
		case v, ok := <-a.Peers:
			if !ok {
				return peers
			}
			for _, p := range v.Peers {
				ip, ok := netip.AddrFromSlice(p.IP)
				if !ok {
					continue
				}
				peers = append(peers, torrent.PeerInfo{Addr: netip.AddrPortFrom(ip.Unmap(), uint16(p.Port)), Source: torrent.PeerSourceDhtGetPeers})
			}
			if len(peers) >= 20 {
				return peers
			}
		case <-timer.C:
			return peers
		case <-ctx.Done():
			return peers
		}
	}
}
