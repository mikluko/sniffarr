// Package memconn joins an HTTP server and its clients inside one process:
// a net.Listener whose connections are net.Pipe ends, and a dialer that
// hands the other end to an http.Transport. Nothing it carries leaves the
// process, so the server's interceptors see a request exactly as one from
// the network. Connections are unbuffered, the listener has no deadline, and
// its address names no network location.
package memconn

import (
	"context"
	"net"
	"net/http"
	"sync"
)

// Network is the network name of a Listener's address.
const Network = "memconn"

// URL is the base URL a client dials a Listener at; the host is a name only.
const URL = "http://" + Network

// Listener accepts the connections its DialContext makes. The zero value is
// not usable; use Listen.
type Listener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

// Listen returns an open Listener.
func Listen() *Listener {
	return &Listener{conns: make(chan net.Conn), done: make(chan struct{})}
}

// Accept waits for a dial and returns its server end, or net.ErrClosed once
// the Listener is closed.
func (l *Listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

// Close makes Accept and every waiting or later dial fail; connections
// already made stay open.
func (l *Listener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

// Addr returns an address of Network; it names no location.
func (l *Listener) Addr() net.Addr { return addr{} }

// DialContext makes a connection and waits for Accept to take its other
// end; network and address are ignored. It fails when ctx ends first or the
// Listener is closed.
func (l *Listener) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.conns <- server:
		return client, nil
	case <-l.done:
		client.Close()
		return nil, net.ErrClosed
	case <-ctx.Done():
		client.Close()
		return nil, ctx.Err()
	}
}

// Client returns an HTTP client speaking unencrypted HTTP/2 over l, for
// requests to URL; the server must accept HTTP/2 without TLS.
func (l *Listener) Client() *http.Client {
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: &http.Transport{Protocols: &p, DialContext: l.DialContext}}
}

type addr struct{}

func (addr) Network() string { return Network }
func (addr) String() string  { return Network }
