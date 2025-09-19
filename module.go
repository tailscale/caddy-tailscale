// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: Apache-2.0

// Package tscaddy provides a set of Caddy modules to integrate Tailscale into Caddy.
package tscaddy

// module.go contains the Tailscale network listeners for caddy
// as well as some shared logic for registered Tailscale nodes.

import (
	"cmp"
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

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/certmagic"
	"github.com/tailscale/tscert"
	"go.uber.org/zap"
	"golang.org/x/oauth2/clientcredentials"
	"tailscale.com/client/tailscale"
	"tailscale.com/hostinfo"
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

	s, err := getNode(ctx, host)
	if err != nil {
		return nil, err
	}

	if network == "" {
		network = "tcp"
	}
	return s.Listen(network, ":"+port)
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

	s, err := getNode(ctx, host)
	if err != nil {
		return nil, err
	}

	st, err := s.Up(context.Background())
	if err != nil {
		return nil, err
	}

	if network == "" || network == "udp" {
		network = "udp4"

	}
	var ap netip.AddrPort
	for _, ip := range st.TailscaleIPs {
		// TODO(will): watch for Tailscale IP changes and update listener
		if (network == "udp4" && ip.Is4()) || (network == "udp6" && ip.Is6()) {
			p, _ := strconv.Atoi(port)
			ap = netip.AddrPortFrom(ip, uint16(p))
			break
		}
	}
	return s.Server.ListenPacket(network, ap.String())
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
	app := appIface.(*App)

	s, _, err := nodes.LoadOrNew(name, func() (caddy.Destructor, error) {
		s := &tsnet.Server{
			Logf: func(format string, args ...any) {
				app.logger.Sugar().Debugf(format, args...)
			},
			UserLogf: func(format string, args ...any) {
				app.logger.Sugar().Infof(format, args...)
			},
			Ephemeral:    getEphemeral(name, app),
			RunWebClient: getWebUI(name, app),
			Port:         getPort(name, app),
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

func getAuthKey(name string, app *App) (string, error) {
	var authKey string
	var err error

	if node, ok := app.Nodes[name]; ok {
		if node.AuthKey != "" {
			authKey, err = repl.ReplaceOrErr(node.AuthKey, true, true)
			if err != nil {
				return "", err
			}
		}
	}

	if authKey == "" && app.DefaultAuthKey != "" {
		authKey, err = repl.ReplaceOrErr(app.DefaultAuthKey, true, true)
		if err != nil {
			return "", err
		}
	}

	if authKey == "" {
		// Set authkey to "TS_AUTHKEY_<HOST>".
		// If empty, fall back to "TS_AUTHKEY".
		authKey = os.Getenv("TS_AUTHKEY_" + strings.ToUpper(name))
		if authKey != "" {
			app.logger.Warn("Relying on TS_AUTHKEY_{HOST} env var is deprecated. Set caddy config instead.", zap.Any("host", name))
		} else {
			authKey = os.Getenv("TS_AUTHKEY")
		}
	}

	if authKey == "" {
		return "", nil
	}

	return resolveAuthKey(authKey, name, app)
}

func getTags(name string, app *App) []string {
	var tags []string

	// Start with app-level tags
	tags = append(tags, app.Tags...)

	// Add node-specific tags
	if node, ok := app.Nodes[name]; ok {
		tags = append(tags, node.Tags...)
	}

	// Remove duplicates
	seen := make(map[string]bool)
	result := []string{}
	for _, tag := range tags {
		if !seen[tag] {
			seen[tag] = true
			result = append(result, tag)
		}
	}

	return result
}

func resolveAuthKey(authKey, name string, app *App) (string, error) {
	if !strings.HasPrefix(authKey, "tskey-client-") {
		return authKey, nil
	}
	clientSecret := authKey

	app.logger.Debug("OAuth client token detected, performing token exchange", zap.String("node", name))

	clientID := os.Getenv("TS_API_CLIENT_ID")
	if clientID == "" || clientSecret == "" {
		app.logger.Error("TS_API_CLIENT_ID and TS_AUTHKEY must be set to use OAuth client tokens")
	}

	tags := getTags(name, app)
	if len(tags) == 0 {
		app.logger.Error("at least one tag must be specified")
	}

	baseURL := cmp.Or(os.Getenv("TS_BASE_URL"), "https://api.tailscale.com")

	credentials := clientcredentials.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     baseURL + "/api/v2/oauth/token",
	}

	ctx := context.Background()
	tailscale.I_Acknowledge_This_API_Is_Unstable = true
	tsClient := tailscale.NewClient("-", nil)
	tsClient.UserAgent = "caddy-tailscale"
	tsClient.HTTPClient = credentials.Client(ctx)
	tsClient.BaseURL = baseURL

	ephemeral := getEphemeral(name, app)

	caps := tailscale.KeyCapabilities{
		Devices: tailscale.KeyDeviceCapabilities{
			Create: tailscale.KeyDeviceCreateCapabilities{
				Reusable:      false,
				Ephemeral:     ephemeral,
				Preauthorized: true,
				Tags:          tags,
			},
		},
	}

	authkey, _, err := tsClient.CreateKey(ctx, caps)
	if err != nil {
		return "", fmt.Errorf("failed to create auth key: %w", err)
	}

	app.logger.Info("Successfully generated auth key from OAuth token", zap.String("node", name))
	return authkey, nil

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

func (t *tsnetServerListener) Unwrap() net.Listener {
	if t == nil {
		return nil
	}
	return t.Listener
}

func (t *tsnetServerListener) Close() error {
	if err := t.Listener.Close(); err != nil {
		return err
	}

	// Decrement usage count of this node.
	// If usage reaches zero, then the node is actually shutdown.
	_, err := nodes.Delete(t.name)
	return err
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

// localAPITransport is an [http.RoundTripper] that sends requests to a [tailscale.LocalClient]'s LocalAPI.
type localAPITransport struct {
	*tailscale.LocalClient
}

func (t *localAPITransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.DoLocalRequest(req)
}
