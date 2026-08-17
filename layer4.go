// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: Apache-2.0

package tscaddy

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
	"github.com/caddyserver/caddy/v2/modules/caddytls"
	"github.com/mholt/caddy-l4/layer4"
	"github.com/mholt/caddy-l4/modules/l4proxy"
)

// layer4.go contains the Layer4 module.

func init() {
	caddy.RegisterModule(&Handler{})
}

// Handler is a layer4 handler that proxies connections through a Tailscale
// network tunnel to the configured upstream.
type Handler struct {
	Name string `json:"name,omitempty"`

	node   *tailscaleNode
	tlsCfg *tls.Config // built during Provision from Upstream.TLS

	// Upstreams is the list of backends to proxy to.
	Upstream l4proxy.Upstream `json:"upstream"`
}

func (*Handler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "layer4.handlers.tailscale",
		New: func() caddy.Module { return new(Handler) },
	}
}

// UnmarshalCaddyfile populates a Layer4 config from a caddyfile.
//
// We only support a single token identifying the name of a node in the App config.
// For example:
//
//	layer4 {
//	    :8080 {
//	        route {
//	            tailscale <upstream_host:port> [<my-node>]
//	        }
//	    }
//	}
//
// If a node name is not specified, a default name is used.
func (h *Handler) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	const defaultNodeName = "caddy-layer4"

	d.Next() // skip layer4 name
	if !d.NextArg() {
		return d.ArgErr()
	}
	h.Upstream = l4proxy.Upstream{Dial: []string{d.Val()}}

	if d.NextArg() {
		h.Name = d.Val()
	} else {
		h.Name = defaultNodeName
	}

	// Parse optional block for TLS configuration
	for nesting := d.Nesting(); d.NextBlock(nesting); {
		switch d.Val() {
		case "tls":
			if d.NextArg() {
				return d.ArgErr()
			}
			if h.Upstream.TLS == nil {
				h.Upstream.TLS = new(reverseproxy.TLSConfig)
			}

		case "tls_client_auth":
			if h.Upstream.TLS == nil {
				h.Upstream.TLS = new(reverseproxy.TLSConfig)
			}
			if d.CountRemainingArgs() == 1 {
				_, h.Upstream.TLS.ClientCertificateAutomate = d.NextArg(), d.Val()
			} else if d.CountRemainingArgs() == 2 {
				_, h.Upstream.TLS.ClientCertificateFile = d.NextArg(), d.Val()
				_, h.Upstream.TLS.ClientCertificateKeyFile = d.NextArg(), d.Val()
			} else {
				return d.ArgErr()
			}

		case "tls_insecure_skip_verify":
			if d.NextArg() {
				return d.ArgErr()
			}
			if h.Upstream.TLS == nil {
				h.Upstream.TLS = new(reverseproxy.TLSConfig)
			}
			h.Upstream.TLS.InsecureSkipVerify = true

		case "tls_server_name":
			if !d.NextArg() {
				return d.ArgErr()
			}
			if h.Upstream.TLS == nil {
				h.Upstream.TLS = new(reverseproxy.TLSConfig)
			}
			h.Upstream.TLS.ServerName = d.Val()

		case "tls_trust_pool":
			if !d.NextArg() {
				return d.ArgErr()
			}
			modStem := d.Val()
			modID := "tls.ca_pool.source." + modStem
			unm, err := caddyfile.UnmarshalModule(d, modID)
			if err != nil {
				return err
			}
			ca, ok := unm.(caddytls.CA)
			if !ok {
				return d.Errf("module %s is not a caddytls.CA", modID)
			}
			if h.Upstream.TLS == nil {
				h.Upstream.TLS = new(reverseproxy.TLSConfig)
			}
			if h.Upstream.TLS.CARaw != nil {
				return d.Err("cannot specify \"tls_trust_pool\" twice in caddyfile")
			}
			h.Upstream.TLS.CARaw = caddyconfig.JSONModuleObject(ca, "provider", modStem, nil)

		default:
			return d.Errf("unrecognized subdirective %s", d.Val())
		}
	}

	return nil
}

func (h *Handler) Handle(ctx *layer4.Connection, _ layer4.Handler) error {
	up, err := h.node.Dial(ctx.Context, ctx.LocalAddr().Network(), h.Upstream.String())
	if err != nil {
		return err
	}
	defer up.Close()

	if h.tlsCfg != nil {
		up = tls.Client(up, h.tlsCfg)
	}

	var wg sync.WaitGroup

	wg.Go(func() {
		if _, err := io.Copy(ctx, up); err != nil {
			h.node.Server.Logf("l4 proxy: upstream->client copy error: %v", err)
		}
		if cw, ok := ctx.Conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	})

	if _, err := io.Copy(up, ctx); err != nil {
		h.node.Server.Logf("l4 proxy: client->upstream copy error: %v", err)
	}
	if cw, ok := up.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	} else {
		_ = up.Close()
	}

	wg.Wait()
	return nil
}

func (h *Handler) Provision(ctx caddy.Context) error {
	if len(h.Upstream.Dial) == 0 {
		return fmt.Errorf("l4 handler: upstream address is required")
	}

	node, err := getNode(ctx, h.Name)
	if err != nil {
		return err
	}
	h.node = node

	if h.Upstream.TLS != nil {
		tlsCfg, err := h.Upstream.TLS.MakeTLSClientConfig(ctx)
		if err != nil {
			return err
		}
		if tlsCfg == nil {
			tlsCfg = new(tls.Config)
		}
		if tlsCfg.ServerName == "" {
			if host, _, err := net.SplitHostPort(h.Upstream.String()); err == nil {
				tlsCfg.ServerName = host
			}
		}
		h.tlsCfg = tlsCfg
	}

	return nil
}

func (h *Handler) Cleanup() error {
	// Decrement usage count of this node.
	_, err := nodes.Delete(h.Name)
	return err
}

var (
	_ layer4.NextHandler    = (*Handler)(nil)
	_ caddy.Provisioner     = (*Handler)(nil)
	_ caddy.CleanerUpper    = (*Handler)(nil)
	_ caddyfile.Unmarshaler = (*Handler)(nil)
)
