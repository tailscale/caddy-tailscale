// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: Apache-2.0

package tscaddy

import (
	"reflect"
	"testing"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp/caddyauth"
)

// Authenticate must have a pointer receiver: a value receiver runs against a
// copy of the module, so the localclient cache never sticks and every gated
// request leaks a tailscaled connection (see the commit that added this test).
//
// A behavioural test would need a live tailscaled, so this asserts the method
// set instead — a value receiver is exactly what puts Authenticate into Auth's
// method set, making the value type satisfy caddyauth.Authenticator.
func TestAuthenticateHasPointerReceiver(t *testing.T) {
	authenticator := reflect.TypeFor[caddyauth.Authenticator]()

	if reflect.TypeFor[Auth]().Implements(authenticator) {
		t.Error("Auth implements caddyauth.Authenticator by value, so Authenticate has a value receiver: " +
			"client()'s cache fills a per-request copy, leaking a tailscaled connection per request")
	}
	if !reflect.TypeFor[*Auth]().Implements(authenticator) {
		t.Error("*Auth does not implement caddyauth.Authenticator")
	}
}

// Caddy must instantiate the module as a pointer, or the provider it holds
// would be a value whose method set lacks the pointer-receiver Authenticate.
func TestAuthModuleInstantiatesPointer(t *testing.T) {
	instance := (*Auth)(nil).CaddyModule().New()

	if _, ok := instance.(*Auth); !ok {
		t.Fatalf("CaddyModule().New() = %T, want *Auth", instance)
	}
	if _, ok := instance.(caddyauth.Authenticator); !ok {
		t.Fatal("module instance does not implement caddyauth.Authenticator")
	}
}
