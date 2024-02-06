package tscaddy

import "github.com/caddyserver/caddy/v2"

type TSApp struct {
	// DefaultAuthKey is the default auth key to use for Tailscale if no other auth key is specified.
	DefaultAuthKey string `json:"auth_key,omitempty" caddy:"namespace=tailscale.auth_key"`

	Ephemeral bool `json:"ephemeral,omitempty" caddy:"namespace=tailscale.ephemeral"`
}

func (TSApp) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "tailscale",
		New: func() caddy.Module { return new(TSApp) },
	}
}
