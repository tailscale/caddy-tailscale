// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: Apache-2.0

package tscaddy

import "net"

// addressCachingListener snapshots connection addresses before handing
// connections to Caddy. Some userspace network connections can return nil
// addresses after their underlying endpoint has been closed.
type addressCachingListener struct {
	net.Listener
}

func (ln *addressCachingListener) Accept() (net.Conn, error) {
	for {
		conn, err := ln.Listener.Accept()
		if err != nil {
			return nil, err
		}

		localAddr := conn.LocalAddr()
		remoteAddr := conn.RemoteAddr()

		// The peer may have reset the connection before we could snapshot
		// its addresses. Do not hand Caddy an unusable connection or
		// fabricate an address used for logging, matching, or authentication.
		if localAddr == nil || remoteAddr == nil {
			_ = conn.Close()
			continue
		}

		return &addressCachingConn{
			Conn:       conn,
			localAddr:  localAddr,
			remoteAddr: remoteAddr,
		}, nil
	}
}

// addressCachingConn keeps addresses available independently of the
// underlying endpoint's lifecycle. Other net.Conn methods are delegated.
type addressCachingConn struct {
	net.Conn
	localAddr  net.Addr
	remoteAddr net.Addr
}

func (conn *addressCachingConn) LocalAddr() net.Addr {
	return conn.localAddr
}

func (conn *addressCachingConn) RemoteAddr() net.Addr {
	return conn.remoteAddr
}

var (
	_ net.Listener = (*addressCachingListener)(nil)
	_ net.Conn     = (*addressCachingConn)(nil)
)
