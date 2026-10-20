package sniff

import (
	"encoding/hex"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/dht/v2/krpc"
	peer_store "github.com/anacrolix/dht/v2/peer-store"
	"github.com/stretchr/testify/require"
)

func ih(b byte) peer_store.InfoHash { return peer_store.InfoHash{b} }

func addr(i int) krpc.NodeAddr {
	return krpc.NodeAddr{IP: net.IPv4(10, byte(i>>16), byte(i>>8), byte(i)).To4(), Port: 6881}
}

func TestPeerStoreServesWithinTTL(t *testing.T) {
	c := &clock{at: hour}
	s := newPeerStore(peerStoreConfig{TTL: 30 * time.Minute, Now: c.now})
	s.AddPeer(ih(1), addr(1))
	c.set(hour.Add(20 * time.Minute))
	s.AddPeer(ih(1), addr(2))
	require.Len(t, s.GetPeers(ih(1)), 2)
	require.Nil(t, s.GetPeers(ih(2)), "an unknown infohash has no peers")

	c.set(hour.Add(45 * time.Minute))
	got := s.GetPeers(ih(1))
	require.Len(t, got, 1, "the peer announced 45 minutes ago is past the TTL")
	require.Equal(t, addr(2), got[0])
	infohashes, peers := s.Count()
	require.Equal(t, 1, infohashes)
	require.Equal(t, 2, peers, "expired peers are held until a sweep")

	s.Sweep()
	infohashes, peers = s.Count()
	require.Equal(t, 1, infohashes)
	require.Equal(t, 1, peers)
	c.set(hour.Add(time.Hour))
	s.Sweep()
	infohashes, peers = s.Count()
	require.Zero(t, infohashes, "an infohash with no live peer goes")
	require.Zero(t, peers)
	require.Empty(t, s.Sample(20))
}

func TestPeerStoreOnePeerPerIP(t *testing.T) {
	s := newPeerStore(peerStoreConfig{})
	s.AddPeer(ih(1), krpc.NodeAddr{IP: net.IPv4(10, 0, 0, 1), Port: 6881})
	s.AddPeer(ih(1), krpc.NodeAddr{IP: net.IPv4(10, 0, 0, 1), Port: 6882})
	got := s.GetPeers(ih(1))
	require.Len(t, got, 1, "BEP 33: one entry per IP, the port updated")
	require.Equal(t, 6882, got[0].Port)
	require.Equal(t, net.IP{10, 0, 0, 1}, got[0].IP, "IPv4 goes out as 4 bytes")
}

func TestPeerStoreBounds(t *testing.T) {
	c := &clock{at: hour}
	s := newPeerStore(peerStoreConfig{Infohashes: 3, Peers: 2, Reply: 1, Now: c.now})
	for i := 1; i <= 3; i++ {
		c.set(hour.Add(time.Duration(i) * time.Minute))
		s.AddPeer(ih(byte(i)), addr(1))
	}
	c.set(hour.Add(10 * time.Minute))
	s.AddPeer(ih(1), addr(2))
	s.AddPeer(ih(4), addr(1))
	require.Nil(t, s.GetPeers(ih(2)), "the infohash announced least recently is evicted")
	require.Len(t, s.GetPeers(ih(1)), 1, "Reply caps a reply")
	infohashes, peers := s.Count()
	require.Equal(t, 3, infohashes)
	require.Equal(t, 4, peers)

	c.set(hour.Add(11 * time.Minute))
	s.AddPeer(ih(1), addr(3))
	got := s.GetPeers(ih(1))
	require.Len(t, got, 1)
	require.NotEqual(t, addr(1), got[0], "the peer announced least recently is evicted")
	_, peers = s.Count()
	require.Equal(t, 4, peers)
	require.Len(t, s.Sample(2), 2)
	require.ElementsMatch(t, [][20]byte{ih(1), ih(3), ih(4)}, s.Sample(10), "a sample past the count is every infohash")
}

func TestPeerStoreSeeds(t *testing.T) {
	c := &clock{at: hour}
	s := newPeerStore(peerStoreConfig{Now: c.now})
	s.MarkSeed(ih(1), netip.MustParseAddr("10.0.0.1"))
	s.MarkSeed(ih(1), netip.MustParseAddr("10.0.0.3"))
	s.AddPeer(ih(1), krpc.NodeAddr{IP: net.IPv4(10, 0, 0, 1), Port: 1})
	s.AddPeer(ih(1), krpc.NodeAddr{IP: net.IPv4(10, 0, 0, 2), Port: 1})
	c.set(hour.Add(DefaultSeedMarkTTL + time.Second))
	s.AddPeer(ih(1), krpc.NodeAddr{IP: net.IPv4(10, 0, 0, 3), Port: 1})
	seeds, downloaders := s.Filters(ih(1))
	var wantSeeds, wantDownloaders krpc.ScrapeBloomFilter
	wantSeeds.AddIp(net.IP{10, 0, 0, 1})
	wantDownloaders.AddIp(net.IP{10, 0, 0, 2})
	wantDownloaders.AddIp(net.IP{10, 0, 0, 3})
	require.Equal(t, wantSeeds, seeds, "a mark taken within its TTL makes a seed")
	require.Equal(t, wantDownloaders, downloaders, "a mark past it does not")
	sd, pe := s.Estimate(ih(1))
	require.InDelta(t, 1, sd, 0.01)
	require.InDelta(t, 2, pe, 0.01)
	require.Zero(t, len(s.seeds), "marks are consumed")
}

// TestBloomFilterVector checks the library's filter against the BEP 33 test
// vector.
func TestBloomFilterVector(t *testing.T) {
	var f krpc.ScrapeBloomFilter
	for i := range 256 {
		f.AddIp(net.IP{192, 0, 2, byte(i)})
	}
	for i := range 1000 {
		ip := net.ParseIP("2001:db8::")
		ip[14], ip[15] = byte(i>>8), byte(i)
		f.AddIp(ip)
	}
	want := strings.NewReplacer(" ", "", "\n", "").Replace(`
F6C3F5EA A07FFD91 BDE89F77 7F26FB2B FF37BDB8 FB2BBAA2 FD3DDDE7 BACFFF75 EE7CCBAE
FE5EEDB1 FBFAFF67 F6ABFF5E 43DDBCA3 FD9B9FFD F4FFD3E9 DFF12D1B DF59DB53 DBE9FA5B
7FF3B8FD FCDE1AFB 8BEDD7BE 2F3EE71E BBBFE93B CDEEFE14 8246C2BC 5DBFF7E7 EFDCF24F
D8DC7ADF FD8FFFDF DDFFF7A4 BBEEDF5C B95CE81F C7FCFF1F F4FFFFDF E5F7FDCB B7FD79B3
FA1FC77B FE07FFF9 05B7B7FF C7FEFEFF E0B8370B B0CD3F5B 7F2BD93F EB4386CF DD6F7FD5
BFAF2E9E BFFFFEEC D67ADBF7 C67F17EF D5D75EBA 6FFEBA7F FF47A91E B1BFBB53 E8ABFB57
62ABE8FF 237279BF EFBFEEF5 FFC5FEBF DFE5ADFF ADFEE1FB 737FFFFB FD9F6AEF FEEE76B6
FD8F72EF`)
	require.Equal(t, strings.ToLower(want), hex.EncodeToString(f[:]))
	require.InDelta(t, 1224.9308, f.EstimateCount(), 0.001)
}
