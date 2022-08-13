package tsauth

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/caddyauth"
	"tailscale.com/client/tailscale"
	"tailscale.com/tsnet"
	"tailscale.com/util/strs"
)

func init() {
	caddy.RegisterModule(TailscaleAuth{})
	httpcaddyfile.RegisterHandlerDirective("tailscale_auth", parseCaddyfile)
}

type TailscaleAuth struct {
	AuthKey  string `json:"auth_key,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	Verbose  bool   `json:"verbose,omitempty"`

	server *tsnet.Server
	client *tailscale.LocalClient
}

func (TailscaleAuth) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.authentication.providers.tailscale",
		New: func() caddy.Module { return new(TailscaleAuth) },
	}
}

func (ta *TailscaleAuth) Provision(ctx caddy.Context) error {
	logger := ctx.Logger(ta).Sugar()
	if ta.AuthKey != "" {
		ta.server = &tsnet.Server{
			Hostname: ta.Hostname,
			Logf: func(format string, args ...any) {
				if ta.Verbose {
					logger.Infof(format, args...)
				}
			},
			AuthKey: ta.AuthKey,
		}

		if err := ta.server.Start(); err != nil {
			return err
		}

		var err error
		ta.client, err = ta.server.LocalClient()
		if err != nil {
			return err
		}
	}

	return nil
}

func (ta *TailscaleAuth) Cleanup() error {
	if ta.server != nil {
		ta.server.Close()
	}
	return nil
}

func (TailscaleAuth) Authenticate(w http.ResponseWriter, r *http.Request) (caddyauth.User, bool, error) {
	user := caddyauth.User{}
	info, err := tailscale.WhoIs(r.Context(), r.RemoteAddr)
	if err != nil {
		return user, false, err
	}
	if len(info.Node.Tags) != 0 {
		return user, false, fmt.Errorf("node %s has tags", info.Node.Hostinfo.Hostname())
	}
	var tailnet string
	if !info.Node.Hostinfo.ShareeNode() {
		if s, found := strs.CutPrefix(info.Node.Name, info.Node.ComputedName+"."); found {
			if s, found := strs.CutSuffix(s, ".beta.tailscale.net."); found {
				tailnet = s
			}
		}
	}

	user.ID = info.UserProfile.LoginName
	user.Metadata = map[string]string{
		"tailscale_login":           strings.Split(info.UserProfile.LoginName, "@")[0],
		"tailscale_user":            info.UserProfile.LoginName,
		"tailscale_name":            info.UserProfile.DisplayName,
		"tailscale_profile_picture": info.UserProfile.ProfilePicURL,
		"tailscale_tailnet":         tailnet,
	}
	return user, true, nil
}

func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var ta TailscaleAuth
	repl := caddy.NewReplacer()

	for h.Next() {
		for nesting := h.Nesting(); h.NextBlock(nesting); {
			switch h.Val() {
			case "auth_key":
				if !h.NextArg() {
					return nil, h.ArgErr()
				}
				ta.AuthKey = h.Val()
				ta.AuthKey = repl.ReplaceAll(ta.AuthKey, "")
			case "hostname":
				if !h.NextArg() {
					return nil, h.ArgErr()
				}
				ta.Hostname = h.Val()
			case "verbose":
				ta.Verbose = true
			}
		}
	}

	return caddyauth.Authentication{
		ProvidersRaw: caddy.ModuleMap{
			"tailscale": caddyconfig.JSON(ta, nil),
		},
	}, nil
}
