// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: Apache-2.0

// Package tscaddy provides a set of Caddy modules to integrate Tailscale into Caddy.
package tscaddy

// module.go contains the Tailscale network listeners for caddy
// as well as some shared logic for registered Tailscale nodes.

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/certmagic"
	"github.com/tailscale/tscert"
	"go.uber.org/zap"
	"tailscale.com/client/local"
	"tailscale.com/hostinfo"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"
)

func init() {
	caddy.RegisterNetwork("tailscale", getTCPListener)
	caddy.RegisterNetwork("tailscale+tls", getTLSListener)
	caddy.RegisterNetwork("tailscale/udp", getUDPListener)
	caddyhttp.RegisterNetworkHTTP3("tailscale/udp", "tailscale/udp")
	caddyhttp.RegisterNetworkHTTP3("tailscale", "tailscale/udp")

	// Caddy uses tscert to get certificates for Tailscale hostnames.
	// Update the tscert transport to send requests to the correct tsnet server,
	// rather than just always connecting to the local machine's tailscaled.
	tscert.TailscaledTransport = &tsnetMuxTransport{}
	hostinfo.SetApp("caddy")
}

func getTCPListener(c context.Context, network string, host string, portRange string, portOffset uint, _ net.ListenConfig) (any, error) {
	ctx, ok := c.(caddy.Context)
	if !ok {
		return nil, fmt.Errorf("context is not a caddy.Context: %T", c)
	}

	na, err := caddy.ParseNetworkAddress(caddy.JoinNetworkAddress(network, host, portRange))
	if err != nil {
		return nil, err
	}

	addr := na.JoinHostPort(portOffset)
	network, host, port, err := caddy.SplitNetworkAddress(addr)
	if err != nil {
		return nil, err
	}

	if network == "" {
		network = "tcp"
	}

	// Get node reference for this listener (increments node reference count)
	node, err := getNode(ctx, host)
	if err != nil {
		return nil, err
	}

	// Follow Caddy's standard listener pooling mechanism
	lnKey := fmt.Sprintf("tailscale/%s:%s:%s", host, network, port)

	sharedLn, _, err := tailscaleListeners.LoadOrNew(lnKey, func() (caddy.Destructor, error) {
		ln, err := node.Listen(network, ":"+port)
		if err != nil {
			return nil, err
		}

		return &tailscaleSharedListener{
			Listener: ln,
			key:      lnKey,
		}, nil
	})
	if err != nil {
		return nil, err
	}

	return &tailscaleFakeCloseListener{
		tailscaleSharedListener: sharedLn.(*tailscaleSharedListener),
		node:                    &fakeCloseNode{nodeName: host, node: node},
	}, nil
}

func getTLSListener(c context.Context, network string, host string, portRange string, portOffset uint, _ net.ListenConfig) (any, error) {
	ctx, ok := c.(caddy.Context)
	if !ok {
		return nil, fmt.Errorf("context is not a caddy.Context: %T", c)
	}

	na, err := caddy.ParseNetworkAddress(caddy.JoinNetworkAddress(network, host, portRange))
	if err != nil {
		return nil, err
	}

	addr := na.JoinHostPort(portOffset)
	network, host, port, err := caddy.SplitNetworkAddress(addr)
	if err != nil {
		return nil, err
	}

	if network == "" {
		network = "tcp"
	}

	// Get node reference for this listener (increments node reference count)
	node, err := getNode(ctx, host)
	if err != nil {
		return nil, err
	}

	// Follow Caddy's standard listener pooling mechanism
	lnKey := fmt.Sprintf("tailscale+tls/%s:%s:%s", host, network, port)

	sharedLn, _, err := tailscaleListeners.LoadOrNew(lnKey, func() (caddy.Destructor, error) {
		ln, err := node.Listen(network, ":"+port)
		if err != nil {
			return nil, err
		}

		localClient, _ := node.LocalClient()
		tlsLn := tls.NewListener(ln, &tls.Config{
			GetCertificate: localClient.GetCertificate,
		})

		return &tailscaleSharedListener{
			Listener: tlsLn,
			key:      lnKey,
		}, nil
	})
	if err != nil {
		return nil, err
	}

	return &tailscaleFakeCloseListener{
		tailscaleSharedListener: sharedLn.(*tailscaleSharedListener),
		node:                    &fakeCloseNode{nodeName: host, node: node},
	}, nil
}

func getUDPListener(c context.Context, network string, host string, portRange string, portOffset uint, _ net.ListenConfig) (any, error) {
	ctx, ok := c.(caddy.Context)
	if !ok {
		return nil, fmt.Errorf("context is not a caddy.Context: %T", c)
	}

	na, err := caddy.ParseNetworkAddress(caddy.JoinNetworkAddress(network, host, portRange))
	if err != nil {
		return nil, err
	}

	addr := na.JoinHostPort(portOffset)
	network, host, port, err := caddy.SplitNetworkAddress(addr)
	if err != nil {
		return nil, err
	}

	if network == "" {
		network = "udp"
	}

	// Get node reference for this listener (increments node reference count)
	node, err := getNode(ctx, host)
	if err != nil {
		return nil, err
	}

	// Follow Caddy's standard listener pooling mechanism
	lnKey := fmt.Sprintf("tailscale/udp/%s:%s:%s", host, network, port)

	sharedPc, _, err := tailscaleListeners.LoadOrNew(lnKey, func() (caddy.Destructor, error) {
		st, err := node.Up(context.Background())
		if err != nil {
			return nil, err
		}

		// We can only return one listener and MagicDNS returns IPv4 addresses unless IPv4 is disabled
		// Prefer IPv4 if available unless IPv6 was explicitly requested
		// TODO(will): watch for Tailscale IP changes and update listener
		var ap netip.AddrPort

		// First pass: look for IPv4 tsnet address if IPv4 was implicitly ("udp") or explicitly ("udp4") requested
		if network == "udp" || network == "udp4" {
			for _, ip := range st.TailscaleIPs {
				if ip.Is4() {
					p, _ := strconv.Atoi(port)
					ap = netip.AddrPortFrom(ip, uint16(p))
					network = "udp4" // Update network for tsnet
					break
				}
			}
		}

		// Second pass: look for IPv6 tsnet address if IPv6 was implicitly ("udp") or explicitly ("udp6") requested
		if !ap.IsValid() && (network == "udp" || network == "udp6") {
			for _, ip := range st.TailscaleIPs {
				if ip.Is6() {
					p, _ := strconv.Atoi(port)
					ap = netip.AddrPortFrom(ip, uint16(p))
					network = "udp6" // Update network for tsnet
					break
				}
			}
		}

		if !ap.IsValid() {
			return nil, fmt.Errorf("no suitable Tailscale IP address found for UDP listener")
		}

		pc, err := node.ListenPacket(network, ap.String())
		if err != nil {
			return nil, err
		}

		return &tailscaleSharedPacketConn{
			PacketConn: pc,
			key:        lnKey,
		}, nil
	})
	if err != nil {
		return nil, err
	}

	return &tailscaleFakeClosePacketConn{
		tailscaleSharedPacketConn: sharedPc.(*tailscaleSharedPacketConn),
		node:                      &fakeCloseNode{nodeName: host, node: node},
	}, nil
}

// nodes are the Tailscale nodes that have been configured and started.
// Node configuration comes from the global Tailscale Caddy app.
// When nodes are no longer in used (e.g. all listeners have been closed), they are shutdown.
//
// Callers should use getNode() to get a node by name, rather than accessing this pool directly.
var nodes = caddy.NewUsagePool()

// tailscaleListeners tracks individual tailscale listeners to enable proper cleanup during config reloads.
// This ensures listeners are properly closed when removed from configuration.
var tailscaleListeners = caddy.NewUsagePool()

// getNode returns a tailscale node for Caddy apps to interface with.
//
// The specified name will be used to lookup the node configuration from the tailscale caddy app,
// used to register the node the first time it is used.
// Only one tailscale node is created per name, even if multiple listeners are created for the node.
func getNode(ctx caddy.Context, name string) (*tailscaleNode, error) {
	appIface, err := ctx.App("tailscale")
	if err != nil {
		return nil, err
	}
	app := appIface.(*App)

	s, _, err := nodes.LoadOrNew(name, func() (caddy.Destructor, error) {
		s := &tsnet.Server{
			Logf: func(format string, args ...any) {
				app.logger.Sugar().Debugf(format, args...)
			},
			UserLogf: func(format string, args ...any) {
				app.logger.Sugar().Infof(format, args...)
			},
			Ephemeral:     getEphemeral(name, app),
			RunWebClient:  getWebUI(name, app),
			Port:          getPort(name, app),
			AdvertiseTags: getTags(name, app),
		}

		if s.AuthKey, err = getAuthKey(name, app); err != nil {
			return nil, err
		}
		if s.ControlURL, err = getControlURL(name, app); err != nil {
			return nil, err
		}
		if s.Hostname, err = getHostname(name, app); err != nil {
			return nil, err
		}

		if s.Dir, err = getStateDir(name, app); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(s.Dir, 0700); err != nil {
			return nil, err
		}

		return &tailscaleNode{Server: s}, nil
	})
	if err != nil {
		return nil, err
	}

	return s.(*tailscaleNode), nil
}

var repl = caddy.NewReplacer()

func getAuthKey(name string, app *App) (string, error) {
	if node, ok := app.Nodes[name]; ok {
		if node.AuthKey != "" {
			return repl.ReplaceOrErr(node.AuthKey, true, true)
		}
	}

	if app.DefaultAuthKey != "" {
		return repl.ReplaceOrErr(app.DefaultAuthKey, true, true)
	}

	// Set authkey to "TS_AUTHKEY_<HOST>".
	// If empty, fall back to "TS_AUTHKEY".
	authKey := os.Getenv("TS_AUTHKEY_" + strings.ToUpper(name))
	if authKey != "" {
		app.logger.Warn("Relying on TS_AUTHKEY_{HOST} env var is deprecated. Set caddy config instead.", zap.Any("host", name))
		return authKey, nil
	}

	return os.Getenv("TS_AUTHKEY"), nil
}

func getControlURL(name string, app *App) (string, error) {
	if node, ok := app.Nodes[name]; ok {
		if node.ControlURL != "" {
			return repl.ReplaceOrErr(node.ControlURL, true, true)
		}
	}
	return repl.ReplaceOrErr(app.ControlURL, true, true)
}

func getEphemeral(name string, app *App) bool {
	if node, ok := app.Nodes[name]; ok {
		if v, ok := node.Ephemeral.Get(); ok {
			return v
		}
	}
	return app.Ephemeral
}

func getHostname(name string, app *App) (string, error) {
	if app == nil {
		return name, nil
	}
	if node, ok := app.Nodes[name]; ok {
		if node.Hostname != "" {
			return repl.ReplaceOrErr(node.Hostname, true, true)
		}
	}

	return name, nil
}

func getPort(name string, app *App) uint16 {
	if node, ok := app.Nodes[name]; ok {
		return node.Port
	}

	return 0
}

func getStateDir(name string, app *App) (string, error) {
	if node, ok := app.Nodes[name]; ok {
		if node.StateDir != "" {
			return repl.ReplaceOrErr(node.StateDir, true, true)
		}
	}

	if app.StateDir != "" {
		s, err := repl.ReplaceOrErr(app.StateDir, true, true)
		if err != nil {
			return "", err
		}
		return filepath.Join(s, name), nil
	}

	// By default, tsnet will use the name of the running program in the state directory,
	// but we also include the hostname so that a single caddy instance can have multiple nodes.
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "tsnet-caddy-"+name), nil
}

func getWebUI(name string, app *App) bool {
	if node, ok := app.Nodes[name]; ok {
		if v, ok := node.WebUI.Get(); ok {
			return v
		}
	}
	return app.WebUI
}

func getTags(name string, app *App) []string {
	if node, ok := app.Nodes[name]; ok {
		if node.Tags != nil {
			return node.Tags
		}
	}
	return app.Tags
}

// tailscaleNode is a wrapper around a tsnet.Server that provides a fully self-contained Tailscale node.
// This node can listen on the tailscale network interface, or be used to connect to other nodes in the tailnet.
type tailscaleNode struct {
	*tsnet.Server

	// running reports whether tsnet.Server.Start has returned successfully for this node.
	//
	// Not every node is started when it is created. A node created for the reverse proxy
	// transport is only started when the first request is sent through it, so a node that was
	// configured but never used has no tsnet state at all. Several tsnet.Server methods are
	// documented as safe in that state but are not, so the wrappers below record when the
	// server has actually come up and the accessors that would crash consult it.
	running atomic.Bool
}

// start brings tsnet up and records whether it succeeded.
//
// tsnet.Server.Start is idempotent (it is guarded by a sync.Once), so this is safe to call on
// every use. Only a nil error sets running: when Start fails there is no way to tell from the
// outside how far initialization got, and treating a half-built server as running would put us
// back where we started.
func (t *tailscaleNode) start() error {
	err := t.Server.Start()
	if err == nil {
		t.running.Store(true)
	}
	return err
}

// Listen starts the node, then listens on the tailnet.
//
// tsnet.Server.Listen starts the server itself, so this only exists to record that it happened.
func (t *tailscaleNode) Listen(network, addr string) (net.Listener, error) {
	if err := t.start(); err != nil {
		return nil, err
	}
	return t.Server.Listen(network, addr)
}

// ListenPacket starts the node, then listens on the tailnet. See Listen.
func (t *tailscaleNode) ListenPacket(network, addr string) (net.PacketConn, error) {
	if err := t.start(); err != nil {
		return nil, err
	}
	return t.Server.ListenPacket(network, addr)
}

// Up starts the node and waits for it to come up. See Listen.
func (t *tailscaleNode) Up(ctx context.Context) (*ipnstate.Status, error) {
	if err := t.start(); err != nil {
		return nil, err
	}
	return t.Server.Up(ctx)
}

// LocalClient starts the node, then returns its LocalAPI client. See Listen.
func (t *tailscaleNode) LocalClient() (*local.Client, error) {
	if err := t.start(); err != nil {
		return nil, err
	}
	return t.Server.LocalClient()
}

// HTTPClient starts the node, then returns a client that dials over the tailnet. See Listen.
//
// The error is dropped to keep tsnet.Server's signature: a failure here surfaces on the first
// request the returned client makes, which is where it did before.
func (t *tailscaleNode) HTTPClient() *http.Client {
	_ = t.start()
	return t.Server.HTTPClient()
}

// CertDomains returns the domains this node can serve certificates for, or nil if it is not
// running.
//
// tsnet.Server.CertDomains documents exactly that, but reads s.lb unconditionally, and s.lb is
// only set once the server starts — so on a node that never started it panics instead of
// returning nil. tsnetMuxTransport.RoundTrip calls this on every node in the pool for each
// handshake, which means one unused node would take down TLS for all of them.
func (t *tailscaleNode) CertDomains() []string {
	if !t.running.Load() {
		return nil
	}
	return t.Server.CertDomains()
}

func (t *tailscaleNode) Destruct() error {
	// tsnet.Server.Close "must not be called before or concurrently with Start", and a node that
	// was never started has nothing to close: Server.close ends in s.sys.Bus.Get().Close(), and
	// s.sys is only populated by start(), so closing one panics with a nil dereference.
	//
	// That is reachable in normal operation. Caddy releases the previous configuration's modules
	// after every reload and again on shutdown, so a node held only by a reverse proxy transport
	// that never carried a request crashes the admin API on the reload that stops using it, and
	// turns every shutdown into a panic instead of a clean exit.
	if !t.running.Load() {
		return nil
	}
	return t.Server.Close()
}

// fakeCloseNode is similar to fakeCloseListener but for node references.
// It allows listeners to hold references to nodes without affecting the
// actual node reference count until the listener is truly destroyed.
type fakeCloseNode struct {
	nodeName string
	node     *tailscaleNode
}

func (fcn *fakeCloseNode) Close() error {
	_, _ = nodes.Delete(fcn.nodeName)
	return nil
}

// tailscaleSharedListener is similar to Caddy's sharedListener but for tailscale listeners
type tailscaleSharedListener struct {
	net.Listener
	key string
}

func (tsl *tailscaleSharedListener) Destruct() error {
	return tsl.Close()
}

// tailscaleFakeCloseListener is similar to Caddy's fakeCloseListener
type tailscaleFakeCloseListener struct {
	closed atomic.Bool
	*tailscaleSharedListener
	node *fakeCloseNode
}

func (tfcl *tailscaleFakeCloseListener) Accept() (net.Conn, error) {
	// if the listener is already closed, return error
	if tfcl.closed.Load() {
		return nil, &net.OpError{
			Op:   "accept",
			Net:  tfcl.Addr().Network(),
			Addr: tfcl.Addr(),
			Err:  fmt.Errorf("listener 'closed'"),
		}
	}

	return tfcl.tailscaleSharedListener.Accept()
}

func (tfcl *tailscaleFakeCloseListener) Close() error {
	if tfcl.closed.CompareAndSwap(false, true) {
		_, _ = tailscaleListeners.Delete(tfcl.key)
		return tfcl.node.Close()
	}
	return nil
}

func (tfcl *tailscaleFakeCloseListener) Unwrap() net.Listener {
	return tfcl.Listener
}

// tailscaleSharedPacketConn is similar to tailscaleSharedListener but for packet connections
type tailscaleSharedPacketConn struct {
	net.PacketConn
	key string
}

func (tspc *tailscaleSharedPacketConn) Destruct() error {
	return tspc.Close()
}

// tailscaleFakeClosePacketConn is similar to tailscaleFakeCloseListener but for packet connections
type tailscaleFakeClosePacketConn struct {
	closed atomic.Bool
	*tailscaleSharedPacketConn
	node *fakeCloseNode
}

func (tfcpc *tailscaleFakeClosePacketConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	// if the connection is already closed, return error
	if tfcpc.closed.Load() {
		return 0, nil, &net.OpError{
			Op:   "readfrom",
			Net:  tfcpc.LocalAddr().Network(),
			Addr: tfcpc.LocalAddr(),
			Err:  fmt.Errorf("connection 'closed'"),
		}
	}

	return tfcpc.tailscaleSharedPacketConn.ReadFrom(p)
}

func (tfcpc *tailscaleFakeClosePacketConn) Close() error {
	if tfcpc.closed.CompareAndSwap(false, true) {
		_, _ = tailscaleListeners.Delete(tfcpc.key)
		return tfcpc.node.Close()
	}
	return nil
}

func (tfcpc *tailscaleFakeClosePacketConn) Unwrap() net.PacketConn {
	return tfcpc.PacketConn
}

// tsnetMuxTransport is an [http.RoundTripper] that sends requests to the LocalAPI
// for the tsnet server that matches the ClientHelloInfo server name.
// If no tsnet server matches, a default Transport is used.
type tsnetMuxTransport struct {
	defaultTransport     *http.Transport
	defaultTransportOnce sync.Once
}

func (t *tsnetMuxTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	var rt http.RoundTripper

	clientHello, ok := ctx.Value(certmagic.ClientHelloInfoCtxKey).(*tls.ClientHelloInfo)
	if ok && clientHello != nil {
		nodes.Range(func(key, value any) bool {
			if n, ok := value.(*tailscaleNode); ok && n != nil {
				for _, d := range n.CertDomains() {
					// Tailscale doesn't do wildcard certs, but caddy uses MatchWildcard
					// for the built-in Tailscale cert manager, so we do so here as well.
					if certmagic.MatchWildcard(clientHello.ServerName, d) {
						if lc, err := n.LocalClient(); err == nil {
							rt = &localAPITransport{lc}
						}
						return false
					}
				}
			}
			return true
		})
	}

	if rt == nil {
		t.defaultTransportOnce.Do(func() {
			t.defaultTransport = &http.Transport{
				DialContext: tscert.TailscaledDialer,
			}
		})
		rt = t.defaultTransport
	}
	return rt.RoundTrip(req)
}

// localAPITransport is an [http.RoundTripper] that sends requests to a [local.Client]'s LocalAPI.
type localAPITransport struct {
	*local.Client
}

func (t *localAPITransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.DoLocalRequest(req)
}
