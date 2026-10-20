package stun

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/require"
)

// serve answers every binding request on a loopback socket with mapped as
// the XOR-MAPPED-ADDRESS, or stays silent when mapped is invalid, until the
// test ends. It returns the socket's address.
func serve(t *testing.T, mapped netip.Addr) string {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if !mapped.IsValid() {
				continue
			}
			var req stun.Message
			req.Raw = append([]byte(nil), buf[:n]...)
			if req.Decode() != nil || req.Type != stun.BindingRequest {
				continue
			}
			res := stun.MustBuild(&req, stun.BindingSuccess,
				&stun.XORMappedAddress{IP: mapped.AsSlice(), Port: 51413}, stun.Fingerprint)
			_, _ = pc.WriteTo(res.Raw, from)
		}
	}()
	return pc.LocalAddr().String()
}

func TestAddr(t *testing.T) {
	public := netip.MustParseAddr("203.0.113.7")
	server := serve(t, public)
	got, err := Addr(context.Background(), server)
	require.NoError(t, err)
	require.Equal(t, public, got)
}

func TestAddrSilentServer(t *testing.T) {
	server := serve(t, netip.Addr{})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := Addr(ctx, server)
	require.Error(t, err)
	require.ErrorContains(t, err, server)
}

func TestAddrUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := Addr(ctx, "127.0.0.1:1")
	require.Error(t, err)
}
