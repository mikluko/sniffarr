package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/netip"
	"time"
)

// protocol opens a BitTorrent handshake: the length of the protocol name,
// then the name.
const protocol = "\x13BitTorrent protocol"

// extensionBit, in the sixth reserved byte, announces the extension
// protocol of BEP 10, which a peer holding the torrent answers at once.
const extensionBit = 0x10

// dialTimeout bounds one TCP connection and its handshake.
const dialTimeout = 10 * time.Second

// handshake connects to addr and offers the torrent h, announcing the
// extension protocol; served reports whether the peer answered with a
// handshake for h and then a message, and err whether addr could not be
// connected to at all. The message is the evidence: a peer may echo the
// infohash before it looks the torrent up, then hang up when it holds none.
func handshake(ctx context.Context, addr netip.AddrPort, h, peerID [20]byte) (served bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", addr.String())
	if err != nil {
		return false, err
	}
	defer func() { _ = c.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	msg := make([]byte, 0, 68)
	msg = append(msg, protocol...)
	msg = append(msg, 0, 0, 0, 0, 0, extensionBit, 0, 0)
	msg = append(msg, h[:]...)
	msg = append(msg, peerID[:]...)
	if _, err := c.Write(msg); err != nil {
		return false, nil
	}
	reply := make([]byte, len(msg))
	if _, err := io.ReadFull(c, reply); err != nil {
		return false, nil
	}
	if !bytes.HasPrefix(reply, []byte(protocol)) || !bytes.Equal(reply[len(protocol)+8:len(protocol)+28], h[:]) {
		return false, nil
	}
	_, err = io.ReadFull(c, make([]byte, 4))
	return err == nil, nil
}
