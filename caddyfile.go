package tscaddy

import (
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
)

func init() {
	httpcaddyfile.RegisterGlobalOption("tailscale", parseApp)
}

func parseApp(d *caddyfile.Dispenser, _ any) (any, error) {
	app := new(TSApp)
	if !d.Next() {
		return app, d.ArgErr()

	}

	for nesting := d.Nesting(); d.NextBlock(nesting); {
		switch d.Val() {
		case "auth_key":
			if !d.NextArg() {
				return nil, d.ArgErr()
			}
			app.DefaultAuthKey = d.Val()
		case "ephemeral":
			app.Ephemeral = true
		default:
			return nil, d.Errf("unrecognized subdirective: %s", d.Val())
		}
	}

	return httpcaddyfile.App{
		Name:  "tailscale",
		Value: caddyconfig.JSON(app, nil),
	}, nil
}
