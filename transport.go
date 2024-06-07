// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: Apache-2.0

package tscaddy

// transport.go contains the Transport module.

import (
	"net/http"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
)

func init() {
	caddy.RegisterModule(&Transport{})
}

// Transport is a caddy transport that uses a tailscale node to make requests.
type Transport struct {
	Name string `json:"name,omitempty"`

	node *tailscaleNode

	// A non-nil TLS config enables TLS.
	// We do not currently use the config values for anything.
	TLS *reverseproxy.TLSConfig `json:"tls,omitempty"`
}

func (t *Transport) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.reverse_proxy.transport.tailscale",
		New: func() caddy.Module { return new(Transport) },
	}
}

// UnmarshalCaddyfile populates a Transport config from a caddyfile.
//
// We only support a single token identifying the name of a node in the App config.
// For example:
//
//	reverse_proxy {
//	  transport tailscale my-node
//	}
//
// If a node name is not specified, a default name is used.
func (t *Transport) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	const defaultNodeName = "caddy-proxy"

	d.Next() // skip transport name
	if d.NextArg() {
		t.Name = d.Val()
	} else {
		t.Name = defaultNodeName
	}

	return nil
}

func (t *Transport) Provision(ctx caddy.Context) error {
	var err error
	t.node, err = getNode(ctx, t.Name)
	return err
}

func (t *Transport) Cleanup() error {
	// Decrement usage count of this node.
	_, err := nodes.Delete(t.Name)
	return err
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme == "" {
		if t.TLSEnabled() {
			req.URL.Scheme = "https"
		} else {
			req.URL.Scheme = "http"
		}
	}
	return t.node.HTTPClient().Transport.RoundTrip(req)
}

// TLSEnabled returns true if TLS is enabled.
func (h Transport) TLSEnabled() bool {
	return h.TLS != nil
}

// EnableTLS enables TLS on the transport.
func (h *Transport) EnableTLS(config *reverseproxy.TLSConfig) error {
	h.TLS = config
	return nil
}

var (
	_ http.RoundTripper         = (*Transport)(nil)
	_ caddy.Provisioner         = (*Transport)(nil)
	_ caddy.CleanerUpper        = (*Transport)(nil)
	_ caddyfile.Unmarshaler     = (*Transport)(nil)
	_ reverseproxy.TLSTransport = (*Transport)(nil)
)
