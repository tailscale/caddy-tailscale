package tscaddy

// app.go contains App and Node, which provide global configuration for registering Tailscale nodes.

import (
	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"go.uber.org/zap"
)

func init() {
	caddy.RegisterModule(App{})
	httpcaddyfile.RegisterGlobalOption("tailscale", parseAppConfig)
}

// App is the Tailscale Caddy app used to configure Tailscale nodes.
// Nodes can be used to serve sites privately on a Tailscale network,
// or to connect to other Tailnet nodes as upstream proxy backend.
type App struct {
	// DefaultAuthKey is the default auth key to use for Tailscale if no other auth key is specified.
	DefaultAuthKey string `json:"auth_key,omitempty" caddy:"namespace=tailscale.auth_key"`

	// ControlURL specifies the default control URL to use for nodes.
	ControlURL string `json:"control_url,omitempty" caddy:"namespace=tailscale.control_url"`

	// Ephemeral specifies whether Tailscale nodes should be registered as ephemeral.
	Ephemeral bool `json:"ephemeral,omitempty" caddy:"namespace=tailscale.ephemeral"`

	// Nodes is a map of per-node configuration which overrides global options.
	Nodes map[string]Node `json:"nodes,omitempty" caddy:"namespace=tailscale"`

	logger *zap.Logger
}

// Node is a Tailscale node configuration.
// A single node can be used to serve multiple sites on different domains or ports,
// and/or to connect to other Tailscale nodes.
type Node struct {
	// AuthKey is the Tailscale auth key used to register the node.
	AuthKey string `json:"auth_key,omitempty" caddy:"namespace=auth_key"`

	// ControlURL specifies the control URL to use for the node.
	ControlURL string `json:"control_url,omitempty" caddy:"namespace=tailscale.control_url"`

	// Ephemeral specifies whether the node should be registered as ephemeral.
	Ephemeral bool `json:"ephemeral,omitempty" caddy:"namespace=tailscale.ephemeral"`

	name string
}

func (App) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "tailscale",
		New: func() caddy.Module { return new(App) },
	}
}

func (t *App) Provision(ctx caddy.Context) error {
	t.logger = ctx.Logger(t)
	return nil
}

func (t *App) Start() error {
	return nil
}

func (t *App) Stop() error {
	return nil
}

func parseAppConfig(d *caddyfile.Dispenser, _ any) (any, error) {
	app := &App{
		Nodes: make(map[string]Node),
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
		case "control_url":
			if !d.NextArg() {
				return nil, d.ArgErr()
			}
			app.ControlURL = d.Val()
		case "ephemeral":
			app.Ephemeral = true
		default:
			node, err := parseNodeConfig(d)
			if app.Nodes == nil {
				app.Nodes = map[string]Node{}
			}
			if err != nil {
				return nil, err
			}
			app.Nodes[node.name] = node
		}
	}

	return httpcaddyfile.App{
		Name:  "tailscale",
		Value: caddyconfig.JSON(app, nil),
	}, nil
}

func parseNodeConfig(d *caddyfile.Dispenser) (Node, error) {
	name := d.Val()
	segment := d.NewFromNextSegment()

	if !segment.Next() {
		return Node{}, d.ArgErr()
	}

	node := Node{name: name}
	for nesting := segment.Nesting(); segment.NextBlock(nesting); {
		val := segment.Val()
		switch val {
		case "auth_key":
			if !segment.NextArg() {
				return node, segment.ArgErr()
			}
			node.AuthKey = segment.Val()
		case "control_url":
			if !segment.NextArg() {
				return node, segment.ArgErr()
			}
			node.ControlURL = segment.Val()
		case "ephemeral":
			node.Ephemeral = true
		default:
			return node, segment.Errf("unrecognized subdirective: %s", segment.Val())
		}
	}

	return node, nil
}

var (
	_ caddy.App         = (*App)(nil)
	_ caddy.Provisioner = (*App)(nil)
)
