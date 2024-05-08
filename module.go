// Package tscaddy provides a set of Caddy modules to integrate Tailscale into Caddy.
package tscaddy

// module.go contains the Tailscale network listeners for caddy
// as well as some shared logic for registered Tailscale nodes.

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"path"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"go.uber.org/zap"
	"tailscale.com/tsnet"
)

func init() {
	caddy.RegisterNetwork("tailscale", getPlainListener)
	caddy.RegisterNetwork("tailscale+tls", getTLSListener)
	caddy.RegisterModule(&TailscaleCaddyTransport{})
}

func getPlainListener(c context.Context, _ string, addr string, _ net.ListenConfig) (any, error) {
	ctx, ok := c.(caddy.Context)
	if !ok {
		return nil, fmt.Errorf("context is not a caddy.Context: %T", c)
	}

	network, host, port, err := caddy.SplitNetworkAddress(addr)
	if err != nil {
		return nil, err
	}

	s, err := getNode(ctx, host)
	if err != nil {
		return nil, err
	}

	if network == "" {
		network = "tcp"
	}

	ln := &tailscaleNode{
		Server: s.Server,
	}
	return ln.Listen(network, ":"+port)
}

func getTLSListener(c context.Context, _ string, addr string, _ net.ListenConfig) (any, error) {
	ctx, ok := c.(caddy.Context)
	if !ok {
		return nil, fmt.Errorf("context is not a caddy.Context: %T", c)
	}

	network, host, port, err := caddy.SplitNetworkAddress(addr)
	if err != nil {
		return nil, err
	}

	s, err := getNode(ctx, host)
	if err != nil {
		return nil, err
	}

	if network == "" {
		network = "tcp"
	}
	ln, err := s.Listen(network, ":"+port)
	if err != nil {
		return nil, err
	}

	localClient, _ := s.LocalClient()

	ln = tls.NewListener(ln, &tls.Config{
		GetCertificate: localClient.GetCertificate,
	})

	return ln, nil
}

// nodes are the Tailscale nodes that have been configured and started.
// Node configuration comes from the global Tailscale Caddy app.
// When nodes are no longer in used (e.g. all listeners have been closed), they are shutdown.
//
// Callers should use getNode() to get a node by name, rather than accessing this pool directly.
var nodes = caddy.NewUsagePool()

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
	app := appIface.(*TSApp)

	s, _, err := nodes.LoadOrNew(name, func() (caddy.Destructor, error) {
		s := &tsnet.Server{
			Hostname: name,
			Logf: func(format string, args ...any) {
				app.logger.Sugar().Debugf(format, args...)
			},
			Ephemeral: getEphemeral(name, app),
		}

		if s.AuthKey, err = getAuthKey(name, app); err != nil {
			app.logger.Warn("error parsing auth key", zap.Error(err))
		}

		if name != "" {
			// Set config directory for tsnet.  By default, tsnet will use the name of the
			// running program, but we include the hostname as well so that a single
			// caddy instance can have multiple tsnet servers.
			configDir, err := os.UserConfigDir()
			if err != nil {
				return nil, err
			}
			s.Dir = path.Join(configDir, "tsnet-caddy-"+name)
			if err := os.MkdirAll(s.Dir, 0700); err != nil {
				return nil, err
			}
		}

		return &tailscaleNode{
			s,
		}, nil
	})
	if err != nil {
		return nil, err
	}

	return s.(*tailscaleNode), nil
}

var repl = caddy.NewReplacer()

func getAuthKey(name string, app *TSApp) (string, error) {
	if app == nil {
		return "", nil
	}

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

func getEphemeral(name string, app *TSApp) bool {
	if app == nil {
		return false
	}
	if node, ok := app.Nodes[name]; ok {
		return node.Ephemeral
	}

	return app.Ephemeral
}

// tailscaleNode is a wrapper around a tsnet.Server that provides a fully self-contained Tailscale node.
// This node can listen on the tailscale network interface, or be used to connect to other nodes in the tailnet.
type tailscaleNode struct {
	*tsnet.Server
}

func (t tailscaleNode) Destruct() error {
	return t.Close()
}

func (t *tailscaleNode) Listen(network string, addr string) (net.Listener, error) {
	ln, err := t.Server.Listen(network, addr)
	if err != nil {
		return nil, err
	}
	serverListener := &tsnetServerListener{
		name:     t.Hostname,
		Listener: ln,
	}
	return serverListener, nil
}

type tsnetServerListener struct {
	name string
	net.Listener
}

func (t *tsnetServerListener) Close() error {
	if err := t.Listener.Close(); err != nil {
		return err
	}

	// Decrement usage count of server for this hostname.
	// If usage reaches zero, then the server is actually shutdown.
	_, err := nodes.Delete(t.name)
	return err
}
