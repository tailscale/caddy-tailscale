// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: Apache-2.0

package tscaddy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// Caddy provisions new modules on every config load, and Authenticate runs on a
// copy of Auth. Either way a new local.Client would strand its connections.
func TestAuthsShareSystemLocalClient(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	r = r.WithContext(context.WithValue(r.Context(), caddyhttp.ServerCtxKey, &caddyhttp.Server{}))

	first, err := new(Auth).client(r)
	if err != nil {
		t.Fatal(err)
	}
	second, err := new(Auth).client(r)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("each Auth built its own local.Client, leaking its connections")
	}
}
