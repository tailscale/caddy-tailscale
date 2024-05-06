package tscaddy

import (
	"github.com/caddyserver/caddy/v2"
	"go.uber.org/zap"
)

func init() {
	caddy.RegisterModule(TSApp{})
}

type TSApp struct {
	// DefaultAuthKey is the default auth key to use for Tailscale if no other auth key is specified.
	DefaultAuthKey string `json:"auth_key,omitempty" caddy:"namespace=tailscale.auth_key"`

	Ephemeral bool `json:"ephemeral,omitempty" caddy:"namespace=tailscale.ephemeral"`

	Servers map[string]TSServer `json:"servers,omitempty" caddy:"namespace=tailscale"`

	logger *zap.Logger
}

type TSServer struct {
	AuthKey string `json:"auth_key,omitempty" caddy:"namespace=auth_key"`

	Ephemeral bool `json:"ephemeral,omitempty" caddy:"namespace=tailscale.ephemeral"`

	name string
}

func (TSApp) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "tailscale",
		New: func() caddy.Module { return new(TSApp) },
	}
}

func (t *TSApp) Provision(ctx caddy.Context) error {
	t.logger = ctx.Logger(t)
	return nil
}

func (t *TSApp) Start() error {
	tsapp.Store(t)
	return nil
}

func (t *TSApp) Stop() error {
	tsapp.CompareAndSwap(t, nil)
	return nil
}

var _ caddy.App = (*TSApp)(nil)
var _ caddy.Provisioner = (*TSApp)(nil)
