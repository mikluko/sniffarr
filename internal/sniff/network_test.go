package sniff

import (
	"context"
	"net"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/dht/v2/krpc"
	peer_store "github.com/anacrolix/dht/v2/peer-store"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/stretchr/testify/require"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
)

// testID is the node ID the tests start active nodes under.
var testID = [20]byte{0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42}

// seed starts a client without DHT that serves the info's metadata on
// localhost, and returns the infohash and the client's address. The client
// wants the torrent's data, since a client wanting nothing refuses peers.
func seed(t *testing.T, info metainfo.Info) (Hash, torrent.PeerInfo) {
	t.Helper()
	b, err := bencode.Marshal(info)
	require.NoError(t, err)
	mi := &metainfo.MetaInfo{InfoBytes: b}
	c := torrent.NewDefaultClientConfig()
	c.DefaultStorage = noStorage{}
	c.DisableIPv6 = true
	c.ListenHost = func(string) string { return "127.0.0.1" }
	c.ListenPort = 0
	c.NoDHT = true
	c.NoDefaultPortForwarding = true
	cl, err := torrent.NewClient(c)
	require.NoError(t, err)
	t.Cleanup(func() { cl.Close() })
	st, err := cl.AddTorrent(mi)
	require.NoError(t, err)
	st.DownloadAll()
	return Hash(mi.HashInfoBytes()), torrent.PeerInfo{Addr: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(cl.LocalPort()))}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// started returns a Network started under id and closed with the test.
func started(t *testing.T, cfg NetworkConfig, id [20]byte) *Network {
	t.Helper()
	n := New(cfg, nil, nil)
	require.NoError(t, n.Start(id))
	t.Cleanup(n.Close)
	return n
}

// querier is a DHT node on loopback that queries the one under test.
func querier(t *testing.T) *dht.Server {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	s, err := dht.NewServer(&dht.ServerConfig{
		Conn:             conn,
		NoSecurity:       true,
		StartingNodes:    func() ([]dht.Addr, error) { return nil, nil },
		QueryResendDelay: func() time.Duration { return 500 * time.Millisecond },
	})
	require.NoError(t, err)
	t.Cleanup(s.Close)
	return s
}

// nodeAddr is the address of n's node on loopback.
func nodeAddr(n *Network) dht.Addr {
	return dht.NewAddr(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: n.Port()})
}

func TestMetadataTouchesNoFiles(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	long := strings.Repeat("Очень длинное имя каталога ", 20)
	require.Greater(t, len(long), 300)
	h, peer := seed(t, metainfo.Info{
		Name:        "Collection",
		PieceLength: 1 << 14,
		Pieces:      make([]byte, 20),
		Files: []metainfo.FileInfo{
			{Path: []string{long, "video.mkv"}, Length: 1 << 13},
			{Path: []string{long, "_____padding_file_1_____"}, Length: 1 << 13},
		},
	})
	n := started(t, NetworkConfig{Port: freePort(t), FetchTimeout: 10 * time.Second}, testID)

	m, err := n.swarm.fetch(context.Background(), h, []torrent.PeerInfo{peer})
	require.NoError(t, err)
	require.Equal(t, "Collection", m.Name)
	require.Len(t, m.Files, 2)
	require.Equal(t, long+"/video.mkv", m.Files[0].Path)

	entries, err := os.ReadDir(tmp)
	require.NoError(t, err)
	require.Empty(t, entries, "nothing is created under the temporary directory")
	wd, err := os.ReadDir(".")
	require.NoError(t, err)
	for _, e := range wd {
		require.NotEqual(t, "Collection", e.Name(), "nor under the working directory")
	}
}

func TestClientConfig(t *testing.T) {
	offer := func(Hash, sniffarrv1.Source) {}
	public := netip.MustParseAddr("203.0.113.7")
	for _, tt := range []struct {
		name     string
		cfg      NetworkConfig
		offer    func(Hash, sniffarrv1.Source)
		announce bool
	}{
		{name: "discovering", cfg: NetworkConfig{Port: 6881}, offer: offer, announce: true},
		{name: "discovering with UPnP", cfg: NetworkConfig{Port: 6881, UPnP: true}, offer: offer, announce: true},
		{name: "discovering with a public IP", cfg: NetworkConfig{Port: 6881, PublicIP: public}, offer: offer, announce: true},
		{name: "passive", cfg: NetworkConfig{Port: 6881, Passive: true}},
		{name: "passive with UPnP", cfg: NetworkConfig{Port: 6881, Passive: true, UPnP: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			n := New(tt.cfg, tt.offer, nil)
			id := testID
			if tt.cfg.Passive {
				id = [20]byte{}
			}
			c := n.clientConfig(id)
			require.Equal(t, 6881, c.ListenPort)
			require.Equal(t, !tt.cfg.UPnP, c.NoDefaultPortForwarding, "UPnP alone decides port forwarding")
			var sc dht.ServerConfig
			c.ConfigureAnacrolixDhtServer(&sc)
			require.Equal(t, tt.cfg.Passive, sc.Passive)
			require.Equal(t, tt.announce, sc.OnAnnouncePeer != nil)
			if tt.cfg.Passive {
				require.Nil(t, sc.PeerStore, "a passive node answers nothing and stores nothing")
				require.Nil(t, sc.Conn, "its socket is the library's own")
				require.True(t, sc.NodeId.IsZero(), "and its ID is random")
				return
			}
			require.Same(t, n.store, sc.PeerStore)
			require.IsType(t, &socket{}, sc.Conn)
			require.Len(t, n.sockets, 1)
			if !tt.cfg.PublicIP.IsValid() {
				require.Equal(t, krpc.ID(testID), sc.NodeId, "the ID is used as given")
				return
			}
			require.NotEqual(t, krpc.ID(testID), sc.NodeId)
			require.True(t, dht.NodeIdSecure(sc.NodeId, public.AsSlice()), "the ID takes the BEP 42 prefix of the public IP")
			require.Equal(t, testID[3:], sc.NodeId[3:], "and keeps the rest")
			var again dht.ServerConfig
			c.ConfigureAnacrolixDhtServer(&again)
			require.Equal(t, sc.NodeId, again.NodeId, "the same ID and IP give the same prefix")
		})
	}
}

func TestStartPassiveOnAnyPort(t *testing.T) {
	n := New(NetworkConfig{Passive: true}, nil, nil)
	require.Zero(t, n.Port(), "before Start there is no port")
	require.Zero(t, n.Nodes())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := n.Metadata(ctx, Hash{1})
	require.ErrorIs(t, err, context.DeadlineExceeded, "Metadata waits for Start")
	require.NoError(t, n.Start([20]byte{}))
	t.Cleanup(n.Close)
	require.Positive(t, n.Port(), "port 0 has the system pick one")
	seeds, downloaders := n.Swarm(Hash{1})
	require.Zero(t, seeds+downloaders)
}

func TestNodeAnswersSampleInfohashes(t *testing.T) {
	n := started(t, NetworkConfig{Port: freePort(t)}, testID)
	q := querier(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res := q.Ping(nodeAddr(n).Raw().(*net.UDPAddr))
	require.NoError(t, res.Err, "ping passes through to the node")
	require.Equal(t, testID, [20]byte(res.Reply.R.ID))
	require.Eventually(t, func() bool { return n.Nodes() == 1 }, 5*time.Second, 10*time.Millisecond, "the node learns the querier")

	res = q.Query(ctx, nodeAddr(n), "sample_infohashes", dht.QueryInput{MsgArgs: krpc.MsgArgs{Target: krpc.ID{1}}, NumTries: 1})
	require.NoError(t, res.Err)
	r := res.Reply.R
	require.NotNil(t, r)
	require.Equal(t, testID, [20]byte(r.ID))
	require.NotNil(t, r.Samples, "samples is present even when empty")
	require.Empty(t, *r.Samples)
	require.NotNil(t, r.Num)
	require.Zero(t, *r.Num)
	require.NotNil(t, r.Interval)
	require.Equal(t, int64(DefaultPeerTTL/time.Second), *r.Interval)
	require.Len(t, r.Nodes, 1, "the nodes nearest the target")
	require.Equal(t, q.ID(), [20]byte(r.Nodes[0].ID))
	require.Empty(t, r.Nodes6)
	require.Equal(t, "127.0.0.1", res.Reply.IP.IP.String(), "the querier's address, as the node's own replies carry it")

	for i := range 25 {
		n.store.AddPeer(peer_store.InfoHash{byte(i + 1)}, krpc.NodeAddr{IP: net.IPv4(10, 0, 0, byte(i+1)), Port: 6881})
	}
	res = q.Query(ctx, nodeAddr(n), "sample_infohashes", dht.QueryInput{MsgArgs: krpc.MsgArgs{Target: krpc.ID{1}}, NumTries: 1})
	require.NoError(t, res.Err)
	r = res.Reply.R
	require.Len(t, *r.Samples, sampleSize, "at most 20 of the infohashes held")
	require.Equal(t, int64(25), *r.Num, "num is every infohash held")
}

func TestNodeAnswersScrape(t *testing.T) {
	n := started(t, NetworkConfig{Port: freePort(t)}, testID)
	q := querier(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h := krpc.ID{7}

	res := q.Query(ctx, nodeAddr(n), "get_peers", dht.QueryInput{MsgArgs: krpc.MsgArgs{InfoHash: h}, NumTries: 1})
	require.NoError(t, res.Err)
	require.NotNil(t, res.Reply.R.Token)
	require.Nil(t, res.Reply.R.BFsd, "get_peers without scrape passes through untouched")
	require.Nil(t, res.Reply.R.BFpe)

	res = q.Query(ctx, nodeAddr(n), "get_peers", dht.QueryInput{MsgArgs: krpc.MsgArgs{InfoHash: h, Scrape: 1}, NumTries: 1})
	require.NoError(t, res.Err)
	r := res.Reply.R
	require.NotNil(t, r.Token, "the node's own token")
	require.NotNil(t, r.BFsd, "with scrape the filters come")
	require.NotNil(t, r.BFpe)
	require.Equal(t, krpc.ScrapeBloomFilter{}, *r.BFsd, "empty for an infohash with no peers")
	require.Equal(t, krpc.ScrapeBloomFilter{}, *r.BFpe)
	require.Equal(t, testID, [20]byte(r.ID))

	announce(t, n, h, *r.Token, 6881, true)
	res = q.Query(ctx, nodeAddr(n), "get_peers", dht.QueryInput{MsgArgs: krpc.MsgArgs{InfoHash: h, Scrape: 1}, NumTries: 1})
	require.NoError(t, res.Err)
	r = res.Reply.R
	require.Len(t, r.Values, 1, "the announced peer is served")
	require.Equal(t, krpc.NodeAddr{IP: net.IP{127, 0, 0, 1}, Port: 6881}, r.Values[0])
	require.InDelta(t, 1, r.BFsd.EstimateCount(), 0.01, "an announce with seed makes a seed")
	require.Equal(t, krpc.ScrapeBloomFilter{}, *r.BFpe)
	seeds, downloaders := n.Swarm(Hash(h))
	require.InDelta(t, 1, seeds, 0.01)
	require.Zero(t, downloaders)

	announce(t, n, h, *r.Token, 6882, false)
	res = q.Query(ctx, nodeAddr(n), "get_peers", dht.QueryInput{MsgArgs: krpc.MsgArgs{InfoHash: h, Scrape: 1}, NumTries: 1})
	require.NoError(t, res.Err)
	r = res.Reply.R
	require.Len(t, r.Values, 1, "one entry per IP")
	require.Equal(t, 6882, r.Values[0].Port, "with the port updated")
	require.Equal(t, krpc.ScrapeBloomFilter{}, *r.BFsd, "an announce without seed makes a downloader")
	require.InDelta(t, 1, r.BFpe.EstimateCount(), 0.01)
}

// announce sends n's node an announce_peer for h from a socket of its own
// on loopback, with the token and port, and seed set when seed, and waits
// for the reply.
func announce(t *testing.T, n *Network, h krpc.ID, token string, port int, seed bool) {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer conn.Close()
	a := map[string]any{"id": string(make([]byte, 20)), "info_hash": string(h[:]), "token": token, "port": port}
	if seed {
		a["seed"] = 1
	}
	b, err := bencode.Marshal(map[string]any{"t": "an", "y": "q", "q": "announce_peer", "a": a})
	require.NoError(t, err)
	_, err = conn.WriteTo(b, nodeAddr(n).Raw())
	require.NoError(t, err)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	buf := make([]byte, 1500)
	k, _, err := conn.ReadFrom(buf)
	require.NoError(t, err)
	var m krpc.Msg
	require.NoError(t, bencode.Unmarshal(buf[:k], &m))
	require.Equal(t, "r", m.Y, "the announce is accepted: %v", m.E)
	require.Eventually(t, func() bool { return len(n.store.GetPeers(peer_store.InfoHash(h))) == 1 }, 5*time.Second, 5*time.Millisecond)
}
