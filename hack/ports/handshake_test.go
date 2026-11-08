package main

import (
	"io"
	"net"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

// peer accepts one connection, reads a handshake, echoes it with its own
// peer ID and, when holding, follows it with a keep-alive.
func peer(t *testing.T, holding bool) netip.AddrPort {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		in := make([]byte, 68)
		if _, err := io.ReadFull(c, in); err != nil {
			return
		}
		copy(in[48:], "-XX0000-000000000000")
		_, _ = c.Write(in)
		if holding {
			_, _ = c.Write([]byte{0, 0, 0, 0})
		}
	}()
	return netip.MustParseAddrPort(l.Addr().String())
}

func TestHandshake(t *testing.T) {
	id := newPeerID()
	served, err := handshake(t.Context(), peer(t, true), hash(1), id)
	require.NoError(t, err)
	require.True(t, served)

	served, err = handshake(t.Context(), peer(t, false), hash(1), id)
	require.NoError(t, err)
	require.False(t, served, "an echo without a message is no evidence")

	l, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	addr := netip.MustParseAddrPort(l.Addr().String())
	require.NoError(t, l.Close())
	_, err = handshake(t.Context(), addr, hash(1), id)
	require.Error(t, err)
	require.Error(t, reach(t.Context(), addr))
}
