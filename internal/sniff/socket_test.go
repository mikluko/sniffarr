package sniff

import (
	"net"
	"net/netip"
	"testing"

	"github.com/anacrolix/dht/v2/krpc"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

func TestCloserByXORDistance(t *testing.T) {
	target := krpc.ID{0x10}
	require.True(t, closer(krpc.ID{0x11}, krpc.ID{0x20}, target))
	require.False(t, closer(krpc.ID{0x20}, krpc.ID{0x11}, target))
	require.False(t, closer(krpc.ID{0x11}, krpc.ID{0x11}, target), "equal distance is not closer")
	require.True(t, closer(krpc.ID{0x10, 1}, krpc.ID{0x10, 2}, target), "later bytes decide ties")
}

func TestClosestNodes(t *testing.T) {
	var nodes []krpc.NodeInfo
	for i := range 12 {
		nodes = append(nodes, krpc.NodeInfo{ID: krpc.ID{byte(i)}, Addr: krpc.NodeAddr{IP: net.IPv4(10, 0, 0, byte(i)), Port: 6881}})
	}
	nodes = append(nodes, krpc.NodeInfo{ID: krpc.ID{0xf0}, Addr: krpc.NodeAddr{IP: net.ParseIP("2001:db8::1"), Port: 6881}})
	s := newSocket(nil, krpc.ID{}, newPeerStore(peerStoreConfig{}), counter(meter(nil), "test", "{x}", ""))
	v4, v6 := s.closest(krpc.ID{3}, nil, netip.MustParseAddr("192.0.2.1"))
	require.Empty(t, v4, "nothing before the routing table is bound")
	require.Empty(t, v6)

	s.bind(func() []krpc.NodeInfo { return append([]krpc.NodeInfo(nil), nodes...) })
	v4, v6 = s.closest(krpc.ID{3}, nil, netip.MustParseAddr("192.0.2.1"))
	require.Len(t, v4, sampleNodes)
	require.Equal(t, krpc.ID{3}, v4[0].ID, "nearest first")
	require.Equal(t, krpc.ID{2}, v4[1].ID)
	require.Len(t, v4[0].Addr.IP, net.IPv4len)
	require.Empty(t, v6, "an IPv4 querier gets IPv4 nodes unless it asks")

	v4, v6 = s.closest(krpc.ID{3}, []krpc.Want{krpc.WantNodes6}, netip.MustParseAddr("192.0.2.1"))
	require.Empty(t, v4)
	require.Len(t, v6, 1)
	require.Equal(t, krpc.ID{0xf0}, v6[0].ID)

	_, v6 = s.closest(krpc.ID{3}, nil, netip.MustParseAddr("2001:db8::2"))
	require.Len(t, v6, 1, "an IPv6 querier gets IPv6 nodes")
}

func TestPerSource(t *testing.T) {
	p := newPerSource(rate.Every(1<<20), 2, 2)
	a, b, c := netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("192.0.2.2"), netip.MustParseAddr("192.0.2.3")
	require.True(t, p.allow(a))
	require.True(t, p.allow(a))
	require.False(t, p.allow(a), "the burst is spent")
	require.True(t, p.allow(b), "another source has its own")
	require.True(t, p.allow(c), "past size the sources are forgotten")
	require.True(t, p.allow(a))
}

func TestInboundPassesThrough(t *testing.T) {
	s := newSocket(nil, krpc.ID{}, newPeerStore(peerStoreConfig{}), counter(meter(nil), "test", "{x}", ""))
	src := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 6881}
	require.True(t, s.inbound([]byte("d1:ad2:id20:aaaaaaaaaaaaaaaaaaaae1:q4:ping1:t2:aa1:y1:qe"), src))
	require.True(t, s.inbound([]byte("not bencode 17:sample_infohashes"), src), "a packet that is not a query passes to the node")
	require.True(t, s.inbound([]byte("d1:ad2:id20:aaaaaaaaaaaaaaaaaaaa9:info_hash20:bbbbbbbbbbbbbbbbbbbb6:scrapei1ee1:q9:get_peers1:t2:aa1:y1:qe"), src), "a get_peers with scrape passes, noted")
	require.Len(t, s.pending[src.String()], 1)
	require.True(t, s.inbound([]byte("d1:ad2:id20:aaaaaaaaaaaaaaaaaaaa9:info_hash20:bbbbbbbbbbbbbbbbbbbb4:porti1e4:seedi1e5:token2:xxe1:q13:announce_peer1:t2:aa1:y1:qe"), src), "an announce_peer with seed passes, marked")
	require.Len(t, s.store.seeds, 1)
	require.Len(t, s.pending[src.String()], 1)

	out, ok := s.scraped([]byte("d1:rd2:id20:aaaaaaaaaaaaaaaaaaaa5:token2:xxe1:t2:bb1:y1:re"), src)
	require.False(t, ok, "a reply to another transaction is untouched")
	require.Nil(t, out)
	out, ok = s.scraped([]byte("d1:rd2:id20:aaaaaaaaaaaaaaaaaaaa5:token2:xxe1:t2:aa1:y1:re"), src)
	require.True(t, ok)
	var m krpc.Msg
	require.True(t, decode(out, &m))
	require.NotNil(t, m.R.BFsd)
	require.NotNil(t, m.R.BFpe)
	require.Equal(t, "xx", *m.R.Token, "the node's token stays")
	require.Empty(t, s.pending, "the query is done")
}
