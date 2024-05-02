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
	app := &TSApp{
		Servers: make(map[string]TSServer),
	}
	if !d.Next() {
		return app, d.ArgErr()

	}

	for d.NextBlock(0) {
		val := d.Val()

		switch val {
		case "auth_key":
			if !d.NextArg() {
				return nil, d.ArgErr()
			}
			app.DefaultAuthKey = d.Val()
		case "ephemeral":
			app.Ephemeral = true
		default:
			svr, err := parseServer(d)
			if app.Servers == nil {
				app.Servers = map[string]TSServer{}
			}
			if err != nil {
				return nil, err
			}
			app.Servers[svr.name] = svr
		}
	}

	return httpcaddyfile.App{
		Name:  "tailscale",
		Value: caddyconfig.JSON(app, nil),
	}, nil
}

func parseServer(d *caddyfile.Dispenser) (TSServer, error) {
	name := d.Val()
	segment := d.NewFromNextSegment()

	if !segment.Next() {
		return TSServer{}, d.ArgErr()
	}

	svr := TSServer{}
	svr.name = name
	for nesting := segment.Nesting(); segment.NextBlock(nesting); {
		val := segment.Val()
		switch val {
		case "auth_key":
			if !segment.NextArg() {
				return svr, segment.ArgErr()
			}
			svr.AuthKey = segment.Val()
		case "ephemeral":
			svr.Ephemeral = true
		default:
			return svr, segment.Errf("unrecognized subdirective: %s", segment.Val())
		}
	}

	return svr, nil
}
