// Package stun asks a STUN server for the public address of this host.
package stun

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/pion/stun/v3"
)

// DefaultServer is the STUN server asked when none is given.
const DefaultServer = "stun.l.google.com:19302"

// DefaultTimeout bounds one Addr call, dialling and retransmissions included.
const DefaultTimeout = 5 * time.Second

// Addr returns the IPv4 address server sees a binding request from, by
// RFC 5389. It gives up at DefaultTimeout or when ctx is done. The address
// is the one of the NAT or host facing server, which is the one other DHT
// nodes see too only when both paths leave through the same address.
func Addr(ctx context.Context, server string) (netip.Addr, error) {
	if server == "" {
		server = DefaultServer
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp4", server)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("stun: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return netip.Addr{}, fmt.Errorf("stun: %w", err)
		}
	}
	client, err := stun.NewClient(conn)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("stun: %w", err)
	}
	defer client.Close()
	var (
		mapped stun.XORMappedAddress
		got    error
	)
	err = client.Do(stun.MustBuild(stun.TransactionID, stun.BindingRequest), func(e stun.Event) {
		if e.Error != nil {
			got = e.Error
			return
		}
		got = mapped.GetFrom(e.Message)
	})
	if err = errors.Join(err, got); err != nil {
		return netip.Addr{}, fmt.Errorf("stun: %s: %w", server, err)
	}
	addr, ok := netip.AddrFromSlice(mapped.IP)
	if !ok {
		return netip.Addr{}, fmt.Errorf("stun: %s: no address in the reply", server)
	}
	return addr.Unmap(), nil
}
