// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: Apache-2.0

package tscaddy

// auth.go contains the TailscaleAuth module and supporting logic.

import (
	"fmt"
	"net"
	"net/http"
	"reflect"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/caddyauth"
	"tailscale.com/client/tailscale"
	"tailscale.com/tsnet"
)

func init() {
	caddy.RegisterModule(Auth{})
	httpcaddyfile.RegisterHandlerDirective("tailscale_auth", parseAuthConfig)
	httpcaddyfile.RegisterDirectiveOrder("tailscale_auth", httpcaddyfile.After, "basicauth")
}

// Auth is an HTTP authentication provider that authenticates users based on their Tailscale identity.
// If configured on a caddy site that is listening on a tailscale node,
// that node will be used to identify the user information for inbound requests.
// Otherwise, it will attempt to find and use the local tailscaled daemon running on the system.
type Auth struct {
	localclient *tailscale.LocalClient
}

func (Auth) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.authentication.providers.tailscale",
		New: func() caddy.Module { return new(Auth) },
	}
}

// findTsnetListener recursively searches ln for wrapped or embedded net.Listeners
// until it finds a tsnetListener or runs out.
// ok indicates if a tsnetListener was found.
//
// In the future consider alternative approach if Caddy supports unwrapping listeners.
// See discussion in https://github.com/tailscale/caddy-tailscale/pull/70
func findTsnetListener(ln net.Listener) (_ tsnetListener, ok bool) {
	if ln == nil {
		return nil, false
	}

	// if ln is a tsnetListener, return it.
	if tsn, ok := ln.(tsnetListener); ok {
		return tsn, true
	}

	// if ln is a wrappedListener, unwrap it.
	if wl, ok := ln.(wrappedListener); ok {
		return findTsnetListener(wl.Unwrap())
	}

	// if ln has an embedded net.Listener field, unwrap it.
	s := reflect.ValueOf(ln)
	if s.Kind() == reflect.Ptr {
		s = s.Elem()
	}
	if s.Kind() != reflect.Struct {
		return nil, false
	}

	innerLn := s.FieldByName("Listener")
	if innerLn.IsZero() {
		// no more child/embedded listeners left
		return nil, false
	}

	// if the "Listener" field is a net.Listener, use it.
	if wl, ok := innerLn.Interface().(net.Listener); ok {
		return findTsnetListener(wl)
	}
	return nil, false
}

// wrappedListener is implemented by types that wrap net.Listeners.
type wrappedListener interface {
	Unwrap() net.Listener
}

// client returns the tailscale LocalClient for the TailscaleAuth module.
// If the LocalClient has not already been configured, the provided request will be used to
// lookup the tailscale node that serviced the request, and get the associated LocalClient.
func (ta *Auth) client(r *http.Request) (*tailscale.LocalClient, error) {
	if ta.localclient != nil {
		return ta.localclient, nil
	}

	// if request was made through a tsnet listener, set up the client for the associated tsnet
	// server.
	server := r.Context().Value(caddyhttp.ServerCtxKey).(*caddyhttp.Server)
	for _, listener := range server.Listeners() {
		if tsl, ok := findTsnetListener(listener); ok {
			var err error
			ta.localclient, err = tsl.Server().LocalClient()
			if err != nil {
				return nil, err
			}
		}
	}

	if ta.localclient == nil {
		// default to empty client that will talk to local tailscaled
		ta.localclient = new(tailscale.LocalClient)
	}

	return ta.localclient, nil
}

// tsnetListener is an interface that is implemented by [tsnet.Listener].
type tsnetListener interface {
	Server() *tsnet.Server
}

// Authenticate authenticates the request and sets Tailscale user data on the caddy User object.
//
// This method will set the following user metadata:
//   - tailscale_login: the user's login name without the domain
//   - tailscale_user: the user's full login name
//   - tailscale_name: the user's display name
//   - tailscale_profile_picture: the user's profile picture URL
//   - tailscale_tailnet: the user's tailnet name (if the user is not connecting to a shared node)
func (ta Auth) Authenticate(w http.ResponseWriter, r *http.Request) (caddyauth.User, bool, error) {
	user := caddyauth.User{}

	client, err := ta.client(r)
	if err != nil {
		return user, false, err
	}

	info, err := client.WhoIs(r.Context(), r.RemoteAddr)
	if err != nil {
		return user, false, err
	}

	if len(info.Node.Tags) != 0 {
		return user, false, fmt.Errorf("node %s has tags", info.Node.Hostinfo.Hostname())
	}

	var tailnet string
	if !info.Node.Hostinfo.ShareeNode() {
		if s, found := strings.CutPrefix(info.Node.Name, info.Node.ComputedName+"."); found {
			tailnet = strings.TrimSuffix(s, ".")
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

func parseAuthConfig(_ httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var ta Auth

	return caddyauth.Authentication{
		ProvidersRaw: caddy.ModuleMap{
			"tailscale": caddyconfig.JSON(ta, nil),
		},
	}, nil
}

var (
	_ caddyauth.Authenticator = (*Auth)(nil)
)
