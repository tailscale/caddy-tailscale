package tscaddy

// transport.go contains the TailscaleCaddyTransport module.

import (
	"fmt"
	"net/http"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"go.uber.org/zap"
)

// TailscaleCaddyTransport is a caddy transport that uses a tailscale node to make requests.
type TailscaleCaddyTransport struct {
	logger *zap.Logger
	node   *tailscaleNode
}

func (t *TailscaleCaddyTransport) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID: "http.reverse_proxy.transport.tailscale",
		New: func() caddy.Module {
			return new(TailscaleCaddyTransport)
		},
	}
}

func (t *TailscaleCaddyTransport) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	return nil
}

func (t *TailscaleCaddyTransport) Provision(ctx caddy.Context) error {
	t.logger = ctx.Logger()

	// TODO(will): allow users to specify a node name used to lookup that node's config in TSApp.
	s, err := getNode(ctx, "caddy-tsnet-client")
	if err != nil {
		return err
	}

	s.Ephemeral = true
	s.Logf = func(format string, args ...any) {
		t.logger.Debug(fmt.Sprintf(format, args))
	}

	if err := s.Start(); err != nil {
		return err
	}
	t.node = s

	return nil
}

func (t *TailscaleCaddyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme == "" {
		request.URL.Scheme = "http"
	}
	return t.node.HTTPClient().Transport.RoundTrip(request)
}

var (
	_ http.RoundTripper     = (*TailscaleCaddyTransport)(nil)
	_ caddy.Provisioner     = (*TailscaleCaddyTransport)(nil)
	_ caddyfile.Unmarshaler = (*TailscaleCaddyTransport)(nil)
)
