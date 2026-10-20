package sketch

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHashIsFixed(t *testing.T) {
	var k Key
	for i := range k {
		k[i] = byte(i)
	}
	v4 := k.Hash(netip.MustParseAddrPort("203.0.113.7:6881"))
	require.Equal(t, v4, k.Hash(netip.MustParseAddrPort("[::ffff:203.0.113.7]:6881")), "a mapped IPv4 hashes as IPv4")
	require.NotEqual(t, v4, k.Hash(netip.MustParseAddrPort("203.0.113.7:6882")))
	require.NotEqual(t, v4, k.Hash(netip.MustParseAddrPort("[2001:db8::7]:6881")))
	require.Equal(t, uint64(0x42d6514d2c437e9a), v4, "the hash of a peer never changes")
	other, err := NewKey()
	require.NoError(t, err)
	require.NotEqual(t, v4, other.Hash(netip.MustParseAddrPort("203.0.113.7:6881")))
}

func TestRoundTripAndUnion(t *testing.T) {
	k, err := NewKey()
	require.NoError(t, err)
	a, b := New(), New()
	for i := range 300 {
		a.Add(k.Hash(netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 0, byte(i >> 8), byte(i)}), 6881)))
	}
	for i := 200; i < 500; i++ {
		b.Add(k.Hash(netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 0, byte(i >> 8), byte(i)}), 6881)))
	}
	parsed, err := Parse(a.Bytes())
	require.NoError(t, err)
	require.Equal(t, a.Cardinality(), parsed.Cardinality())
	require.InDelta(t, 300, a.Cardinality(), 30)
	require.NoError(t, parsed.Union(b))
	require.InDelta(t, 500, parsed.Cardinality(), 50)
	empty := New()
	require.Equal(t, uint64(0), empty.Cardinality())

	_, err = Parse([]byte{1, 2})
	require.Error(t, err)
	_, err = ParseKey([]byte{1})
	require.Error(t, err)
}
