// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: Apache-2.0

package tscaddy

// auth.go contains the TailscaleAuth module and supporting logic.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/caddyauth"
	"tailscale.com/client/local"
	"tailscale.com/tailcfg"
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
	localclient *local.Client
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
	if s.Kind() == reflect.Pointer {
		s = s.Elem()
	}
	if s.Kind() != reflect.Struct {
		return nil, false
	}

	innerLn := s.FieldByName("Listener")
	if !innerLn.IsValid() || innerLn.IsZero() {
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
func (ta *Auth) client(r *http.Request) (*local.Client, error) {
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
		ta.localclient = new(local.Client)
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
//   - tailscale_grants: JSON object of the peer's granted capabilities ("{}" if none)
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

	// Extract grants from CapMap
	grants := extractGrants(info.CapMap)

	// Register replacer mapping for {tailscale.grants}
	repl := r.Context().Value(caddy.ReplacerCtxKey).(*caddy.Replacer)
	repl.Map(grantsReplacer(grants))

	user.ID = info.UserProfile.LoginName
	user.Metadata = map[string]string{
		"tailscale_login":           strings.Split(info.UserProfile.LoginName, "@")[0],
		"tailscale_user":            info.UserProfile.LoginName,
		"tailscale_name":            info.UserProfile.DisplayName,
		"tailscale_profile_picture": info.UserProfile.ProfilePicURL,
		"tailscale_tailnet":         tailnet,
		"tailscale_grants":          grants.String(),
	}
	return user, true, nil
}

// Grants holds a peer's application capabilities, keyed by capability name,
// with each value being the list of that capability's parsed JSON values.
//
// It is a map so that CEL expressions can index into it, and implements
// fmt.Stringer so that it renders as JSON when used in a header or other
// string context.
type Grants map[string]any

// String returns the grants as a JSON object, matching the format of the
// Tailscale-App-Capabilities header sent by tailscale serve. Non-ASCII
// characters are escaped as \uXXXX so the value is safe to use as an HTTP
// header while remaining valid JSON.
func (g Grants) String() string {
	if g == nil {
		return "{}"
	}
	b, err := json.Marshal(map[string]any(g))
	if err != nil {
		return "{}"
	}
	return asciiJSON(b)
}

// asciiJSON escapes all non-ASCII characters in the JSON document b as
// \uXXXX sequences. Non-ASCII bytes can only appear inside JSON strings, so
// the result is an equivalent JSON document.
func asciiJSON(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for _, r := range string(b) {
		switch {
		case r < utf8.RuneSelf:
			sb.WriteRune(r)
		case r > 0xFFFF:
			r1, r2 := utf16.EncodeRune(r)
			fmt.Fprintf(&sb, `\u%04x\u%04x`, r1, r2)
		default:
			fmt.Fprintf(&sb, `\u%04x`, r)
		}
	}
	return sb.String()
}

// grantsReplacer returns a replacer func that resolves {tailscale.grants}.
// It always returns a non-nil Grants, so that CEL operations like "in" work
// without "no such overload" errors and headers render as "{}".
func grantsReplacer(grants Grants) caddy.ReplacerFunc {
	if grants == nil {
		grants = Grants{}
	}
	return func(key string) (any, bool) {
		if key == "tailscale.grants" {
			return grants, true
		}
		return nil, false
	}
}

// extractGrants converts a PeerCapMap into Grants, parsing each capability
// value as JSON. Values that are not valid JSON are kept as strings.
func extractGrants(capMap tailcfg.PeerCapMap) Grants {
	if len(capMap) == 0 {
		return nil
	}

	grants := make(Grants)

	for cap, vals := range capMap {
		parsed := make([]any, 0, len(vals))
		for _, v := range vals {
			var obj any
			if err := json.Unmarshal([]byte(v), &obj); err == nil {
				parsed = append(parsed, obj)
			} else {
				parsed = append(parsed, string(v))
			}
		}
		grants[string(cap)] = parsed
	}

	if len(grants) == 0 {
		return nil
	}
	return grants
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
