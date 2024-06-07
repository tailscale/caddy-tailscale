// Copyright 2015 Matthew Holt and The Caddy Authors
// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: Apache-2.0

package tscaddy

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	caddycmd "github.com/caddyserver/caddy/v2/cmd"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/caddyauth"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/headers"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
	"github.com/caddyserver/caddy/v2/modules/caddytls"
)

func init() {
	caddycmd.RegisterCommand(caddycmd.Command{
		Name:  "tailscale-proxy",
		Func:  cmdTailscaleProxy,
		Usage: "[--from <addr>] [--to <addr>] [--change-host-header]",
		Short: "A quick reverse proxy with Tailscale authentication",
		Long: `
A copy of caddy's standard production-ready reverse proxy with Tailscale
authentication. Useful for quick deployments, demos, and development.

Simply shuttles HTTP(S) traffic from the --from address to the --to address.

Requests must be received over the Tailscale network interface.  Information
about the authenticated Tailscale client are provided on the proxied request in
X-Webauth-* headers.

Unless otherwise specified in the addresses, the --from address will be
assumed to be HTTPS if a hostname is given, and the --to address will be
assumed to be HTTP.

If the --from address has a host or IP, Caddy will attempt to serve the
proxy over HTTPS with a certificate (unless overridden by the HTTP scheme
or port).

If --change-host-header is set, the Host header on the request will be modified
from its original incoming value to the address of the upstream. (Otherwise, by
default, all incoming headers are passed through unmodified.)
`,
		Flags: func() *flag.FlagSet {
			fs := flag.NewFlagSet("tailscale-proxy", flag.ExitOnError)
			fs.String("from", "localhost", "Address on which to receive traffic")
			fs.String("to", "", "Upstream address to which traffic should be sent")
			fs.Bool("change-host-header", false, "Set upstream Host header to address of upstream")
			fs.Bool("insecure", false, "Disable TLS verification (WARNING: DISABLES SECURITY BY NOT VERIFYING SSL CERTIFICATES!)")
			fs.Bool("internal-certs", false, "Use internal CA for issuing certs")
			fs.Bool("debug", false, "Enable debug logging")
			return fs
		}(),
	})
}

func cmdTailscaleProxy(fs caddycmd.Flags) (int, error) {
	caddy.TrapSignals()

	from := fs.String("from")
	to := fs.String("to")
	changeHost := fs.Bool("change-host-header")
	insecure := fs.Bool("insecure")
	internalCerts := fs.Bool("internal-certs")
	debug := fs.Bool("debug")

	httpPort := strconv.Itoa(caddyhttp.DefaultHTTPPort)
	httpsPort := strconv.Itoa(caddyhttp.DefaultHTTPSPort)

	if to == "" {
		return caddy.ExitCodeFailedStartup, fmt.Errorf("--to is required")
	}

	// strip "tailscale/" prefix if present
	from, tsBind := strings.CutPrefix(from, "tailscale/")

	// set up the downstream address; assume missing information from given parts
	fromAddr, err := httpcaddyfile.ParseAddress(from)

	var listen string
	if tsBind {
		listen = "tailscale/" + fromAddr.Host + ":" + fromAddr.Port
		fromAddr.Host = ""
	} else {
		listen = ":" + fromAddr.Port
	}

	if err != nil {
		return caddy.ExitCodeFailedStartup, fmt.Errorf("invalid downstream address %s: %v", from, err)
	}
	if fromAddr.Path != "" {
		return caddy.ExitCodeFailedStartup, fmt.Errorf("paths are not allowed: %s", from)
	}
	if fromAddr.Scheme == "" {
		if fromAddr.Port == httpPort || fromAddr.Host == "" {
			fromAddr.Scheme = "http"
		} else {
			fromAddr.Scheme = "https"
		}
	}
	if fromAddr.Port == "" {
		if fromAddr.Scheme == "http" {
			fromAddr.Port = httpPort
		} else if fromAddr.Scheme == "https" {
			fromAddr.Port = httpsPort
		}
	}

	// set up the upstream address; assume missing information from given parts
	toAddr, toScheme, err := parseUpstreamDialAddress(to)
	if err != nil {
		return caddy.ExitCodeFailedStartup, fmt.Errorf("invalid upstream address %s: %v", to, err)
	}

	// proceed to build the handler and server
	ht := reverseproxy.HTTPTransport{}
	if toScheme == "https" {
		ht.TLS = new(reverseproxy.TLSConfig)
		if insecure {
			ht.TLS.InsecureSkipVerify = true
		}
	}

	handler := reverseproxy.Handler{
		TransportRaw: caddyconfig.JSONModuleObject(ht, "protocol", "http", nil),
		Upstreams:    reverseproxy.UpstreamPool{{Dial: toAddr}},
		Headers: &headers.Handler{
			Request: &headers.HeaderOps{
				Set: http.Header{
					"X-Webauth-Email":   []string{"{http.auth.user.tailscale_user}"},
					"X-Webauth-Name":    []string{"{http.auth.user.tailscale_name}"},
					"X-Webauth-Photo":   []string{"{http.auth.user.tailscale_profile_picture}"},
					"X-Webauth-Tailnet": []string{"{http.auth.user.tailscale_tailnet}"},
					"X-Webauth-User":    []string{"{http.auth.user.tailscale_login}"},
				},
			},
		},
	}

	if changeHost {
		handler.Headers.Request.Set["Host"] = []string{"{http.reverse_proxy.upstream.hostport}"}
	}

	route := caddyhttp.Route{
		HandlersRaw: []json.RawMessage{
			caddyconfig.JSONModuleObject(handler, "handler", "reverse_proxy", nil),
		},
	}
	if fromAddr.Host != "" {
		route.MatcherSetsRaw = []caddy.ModuleMap{
			{
				"host": caddyconfig.JSON(caddyhttp.MatchHost{fromAddr.Host}, nil),
			},
		}
	}

	authHandler := caddyauth.Authentication{
		ProvidersRaw: caddy.ModuleMap{
			"tailscale": caddyconfig.JSON(Auth{}, nil),
		},
	}
	authRoute := caddyhttp.Route{
		HandlersRaw: []json.RawMessage{
			caddyconfig.JSONModuleObject(authHandler, "handler", "authentication", nil),
		},
	}

	server := &caddyhttp.Server{
		Routes: caddyhttp.RouteList{authRoute, route},
		Listen: []string{listen},
	}

	httpApp := caddyhttp.App{
		Servers: map[string]*caddyhttp.Server{"proxy": server},
	}

	appsRaw := caddy.ModuleMap{
		"http": caddyconfig.JSON(httpApp, nil),
	}
	if internalCerts && fromAddr.Host != "" {
		tlsApp := caddytls.TLS{
			Automation: &caddytls.AutomationConfig{
				Policies: []*caddytls.AutomationPolicy{{
					SubjectsRaw: []string{fromAddr.Host},
					IssuersRaw:  []json.RawMessage{json.RawMessage(`{"module":"internal"}`)},
				}},
			},
		}
		appsRaw["tls"] = caddyconfig.JSON(tlsApp, nil)
	} else if tsBind {
		tlsApp := caddytls.TLS{
			Automation: &caddytls.AutomationConfig{
				Policies: []*caddytls.AutomationPolicy{{
					ManagersRaw: []json.RawMessage{json.RawMessage(`{"via": "tailscale"}`)},
				}},
			},
		}
		appsRaw["tls"] = caddyconfig.JSON(tlsApp, nil)
	}

	var false bool
	cfg := &caddy.Config{
		Admin: &caddy.AdminConfig{Disabled: true,
			Config: &caddy.ConfigSettings{
				Persist: &false,
			},
		},
		AppsRaw: appsRaw,
	}
	if debug {
		cfg.Logging = &caddy.Logging{
			Logs: map[string]*caddy.CustomLog{
				"default": {
					BaseLog: caddy.BaseLog{
						Level: "DEBUG",
					},
				},
			},
		}
	}

	err = caddy.Run(cfg)
	if err != nil {
		return caddy.ExitCodeFailedStartup, err
	}

	fmt.Printf("Caddy proxying %s -> %s\n", fromAddr.String(), toAddr)

	select {}
}
