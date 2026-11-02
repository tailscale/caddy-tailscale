// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: Apache-2.0

package tscaddy

import (
	"crypto/tls"
	"errors"
	"net"
	"testing"
)

// disappearingAddrConn simulates a userspace connection whose addresses
// become unavailable after its endpoint closes.
//
// The embedded net.Conn is intentionally unused: these tests exercise
// only address access and Close, and do not perform network I/O.
type disappearingAddrConn struct {
	net.Conn
	localAddr  net.Addr
	remoteAddr net.Addr
	closed     bool
	closeCalls int
}

func (c *disappearingAddrConn) LocalAddr() net.Addr {
	if c.closed {
		return nil
	}
	return c.localAddr
}

func (c *disappearingAddrConn) RemoteAddr() net.Addr {
	if c.closed {
		return nil
	}
	return c.remoteAddr
}

func (c *disappearingAddrConn) Close() error {
	c.closed = true
	c.closeCalls++
	return nil
}

// queuedAddrListener returns queued connections followed by an error.
// It is a sequential test double, not a concurrent listener.
type queuedAddrListener struct {
	net.Listener
	conns      []net.Conn
	acceptErr  error
	closeCalls int
}

func (ln *queuedAddrListener) Accept() (net.Conn, error) {
	if len(ln.conns) == 0 {
		if ln.acceptErr != nil {
			return nil, ln.acceptErr
		}
		return nil, net.ErrClosed
	}

	conn := ln.conns[0]
	ln.conns = ln.conns[1:]
	return conn, nil
}

func (ln *queuedAddrListener) Close() error {
	ln.closeCalls++
	return nil
}

func newAddrTestConn() *disappearingAddrConn {
	return &disappearingAddrConn{
		localAddr: &net.TCPAddr{
			IP:   net.ParseIP("100.64.0.1"),
			Port: 993,
		},
		remoteAddr: &net.TCPAddr{
			IP:   net.ParseIP("100.64.0.2"),
			Port: 54321,
		},
	}
}

func TestAddressCachingListenerPreservesAddressesAfterClose(t *testing.T) {
	raw := newAddrTestConn()
	wantLocal := raw.LocalAddr()
	wantRemote := raw.RemoteAddr()

	ln := &addressCachingListener{
		Listener: &queuedAddrListener{
			conns: []net.Conn{raw},
		},
	}

	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}

	if got := conn.LocalAddr(); got != wantLocal {
		t.Fatalf("LocalAddr() = %v; want %v", got, wantLocal)
	}
	if got := conn.RemoteAddr(); got != wantRemote {
		t.Fatalf("RemoteAddr() = %v; want %v", got, wantRemote)
	}

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if raw.closeCalls != 1 {
		t.Fatalf("underlying Close calls = %d; want 1", raw.closeCalls)
	}

	// Confirm the fake actually reproduces address disappearance.
	if raw.LocalAddr() != nil || raw.RemoteAddr() != nil {
		t.Fatal("underlying addresses should be nil after Close")
	}

	// These checks fail if the wrapper delegates address access instead
	// of returning cached values.
	if got := conn.LocalAddr(); got != wantLocal {
		t.Fatalf("LocalAddr() after Close = %v; want %v", got, wantLocal)
	}
	if got := conn.RemoteAddr(); got != wantRemote {
		t.Fatalf("RemoteAddr() after Close = %v; want %v", got, wantRemote)
	}

	// Exercise the address methods used by downstream logging.
	if got := conn.LocalAddr().Network(); got != "tcp" {
		t.Fatalf("local network = %q; want tcp", got)
	}
	if got := conn.RemoteAddr().String(); got != wantRemote.String() {
		t.Fatalf("remote address = %q; want %q", got, wantRemote.String())
	}
}

func TestAddressCachingListenerSkipsMissingAddresses(t *testing.T) {
	for _, name := range []string{"local", "remote", "both"} {
		t.Run(name, func(t *testing.T) {
			bad := newAddrTestConn()
			if name == "local" || name == "both" {
				bad.localAddr = nil
			}
			if name == "remote" || name == "both" {
				bad.remoteAddr = nil
			}

			good := newAddrTestConn()
			ln := &addressCachingListener{
				Listener: &queuedAddrListener{
					conns: []net.Conn{bad, good},
				},
			}

			conn, err := ln.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()

			if bad.closeCalls != 1 {
				t.Fatalf("rejected connection Close calls = %d; want 1",
					bad.closeCalls)
			}
			if good.closed {
				t.Fatal("valid connection was closed during acceptance")
			}
			if conn.LocalAddr() != good.localAddr ||
				conn.RemoteAddr() != good.remoteAddr {
				t.Fatal("Accept did not return the valid connection's addresses")
			}
		})
	}
}

func TestAddressCachingListenerPropagatesAcceptError(t *testing.T) {
	wantErr := errors.New("accept failed")

	for _, skipBad := range []bool{false, true} {
		name := "immediate"
		if skipBad {
			name = "after-rejected-connection"
		}

		t.Run(name, func(t *testing.T) {
			rawLn := &queuedAddrListener{acceptErr: wantErr}
			var bad *disappearingAddrConn

			if skipBad {
				bad = newAddrTestConn()
				bad.remoteAddr = nil
				rawLn.conns = []net.Conn{bad}
			}

			ln := &addressCachingListener{Listener: rawLn}
			conn, err := ln.Accept()

			if conn != nil {
				t.Fatal("Accept returned a connection on error")
			}
			if err != wantErr {
				t.Fatalf("Accept error = %v; want original error %v",
					err, wantErr)
			}
			if bad != nil && bad.closeCalls != 1 {
				t.Fatal("rejected connection was not closed exactly once")
			}
		})
	}
}

func TestAddressCachingListenerTLSPlacement(t *testing.T) {
	raw := newAddrTestConn()
	wantLocal := raw.LocalAddr()
	wantRemote := raw.RemoteAddr()

	cachedLn := &addressCachingListener{
		Listener: &queuedAddrListener{
			conns: []net.Conn{raw},
		},
	}

	// Match the ordering used by getTLSListener.
	tlsLn := tls.NewListener(cachedLn, &tls.Config{})

	conn, err := tlsLn.Accept()
	if err != nil {
		t.Fatal(err)
	}

	// Accept does not perform the TLS handshake, so no certificate or
	// network I/O is needed to test the concrete type and address methods.
	if _, ok := conn.(*tls.Conn); !ok {
		t.Fatalf("accepted connection type = %T; want *tls.Conn", conn)
	}

	// Simulate the underlying endpoint closing independently.
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	if got := conn.LocalAddr(); got != wantLocal {
		t.Fatalf("TLS LocalAddr() = %v; want %v", got, wantLocal)
	}
	if got := conn.RemoteAddr(); got != wantRemote {
		t.Fatalf("TLS RemoteAddr() = %v; want %v", got, wantRemote)
	}
}

func TestAddressCachingListenerDelegatesClose(t *testing.T) {
	rawLn := &queuedAddrListener{}
	ln := &addressCachingListener{Listener: rawLn}

	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if rawLn.closeCalls != 1 {
		t.Fatalf("underlying listener Close calls = %d; want 1",
			rawLn.closeCalls)
	}
}
