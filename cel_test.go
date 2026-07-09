// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: Apache-2.0

package tscaddy

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// TestTailscaleGrantsCEL verifies that the tailscale.grants placeholder
// can be used in CEL expressions via the expression matcher.
func TestTailscaleGrantsCEL(t *testing.T) {
	testGrants := map[string]any{
		"example.com/app": []any{
			map[string]any{
				"admin": true,
				"role":  "admin",
			},
		},
		"example.com/cap/sql": []any{
			map[string]any{
				"access": "read",
			},
		},
	}

	req := httptest.NewRequest("GET", "http://example.com", nil)

	// Create a replacer with the tailscale.grants placeholder handler,
	// as Auth.Authenticate does during request processing.
	repl := caddy.NewReplacer()
	repl.Map(func(key string) (any, bool) {
		if key == "tailscale.grants" {
			return testGrants, true
		}
		return nil, false
	})
	ctx := context.WithValue(req.Context(), caddy.ReplacerCtxKey, repl)
	req = req.WithContext(ctx)

	caddyCtx, cancel := caddy.NewContext(caddy.Context{Context: context.Background()})
	defer cancel()

	t.Run("in_operator_key_exists", func(t *testing.T) {
		expr := &caddyhttp.MatchExpression{
			Expr: `"example.com/app" in {tailscale.grants}`,
		}
		if err := expr.Provision(caddyCtx); err != nil {
			t.Fatalf("Provision failed: %v", err)
		}
		matches, err := expr.MatchWithError(req)
		if err != nil {
			t.Fatalf("CEL evaluation failed: %v", err)
		}
		if !matches {
			t.Error("expected 'example.com/app' in grants")
		}
	})

	t.Run("map_index_with_list_size", func(t *testing.T) {
		expr := &caddyhttp.MatchExpression{
			Expr: `{tailscale.grants}["example.com/app"].size() == 1`,
		}
		if err := expr.Provision(caddyCtx); err != nil {
			t.Fatalf("Provision failed: %v", err)
		}
		matches, err := expr.MatchWithError(req)
		if err != nil {
			t.Fatalf("CEL evaluation failed: %v", err)
		}
		if !matches {
			t.Error("expected grants map indexing to return list with 1 element")
		}
	})

	t.Run("deep_field_access", func(t *testing.T) {
		// Access a nested field: grants["example.com/app"][0].admin == true
		expr := &caddyhttp.MatchExpression{
			Expr: `{tailscale.grants}["example.com/app"][0].admin == true`,
		}
		if err := expr.Provision(caddyCtx); err != nil {
			t.Fatalf("Provision failed: %v", err)
		}
		matches, err := expr.MatchWithError(req)
		if err != nil {
			t.Fatalf("CEL evaluation failed: %v", err)
		}
		if !matches {
			t.Error("expected admin to be true")
		}
	})

	t.Run("in_operator_missing_key", func(t *testing.T) {
		expr := &caddyhttp.MatchExpression{
			Expr: `"example.com/nonexistent" in {tailscale.grants}`,
		}
		if err := expr.Provision(caddyCtx); err != nil {
			t.Fatalf("Provision failed: %v", err)
		}
		matches, err := expr.MatchWithError(req)
		if err != nil {
			t.Fatalf("CEL evaluation failed: %v", err)
		}
		if matches {
			t.Error("expected nonexistent key not to be in grants")
		}
	})

	t.Run("grants_empty_when_not_set", func(t *testing.T) {
		// Request without grants in context should get nil from replacer
		emptyReq := httptest.NewRequest("GET", "http://example.com", nil)
		emptyRepl := caddy.NewReplacer()
		emptyCtx := context.WithValue(emptyReq.Context(), caddy.ReplacerCtxKey, emptyRepl)
		emptyReq = emptyReq.WithContext(emptyCtx)

		emptyRepl.Map(func(key string) (any, bool) {
			if key == "tailscale.grants" {
				// Return empty map, as Auth.Authenticate now does
				return map[string]any{}, true
			}
			return nil, false
		})

		// Check that in operator returns false (not an error) when grants are nil
		expr := &caddyhttp.MatchExpression{
			Expr: `"example.com/app" in {tailscale.grants}`,
		}
		if err := expr.Provision(caddyCtx); err != nil {
			t.Fatalf("Provision failed: %v", err)
		}
		matches, err := expr.MatchWithError(emptyReq)
		if err != nil {
			t.Fatalf("CEL evaluation failed with 'no such overload': %v", err)
		}
		if matches {
			t.Error("expected in operator to return false when grants are empty")
		}
	})

	t.Run("null_check_empty_grants", func(t *testing.T) {
		// Same setup: empty grants
		emptyReq := httptest.NewRequest("GET", "http://example.com", nil)
		emptyRepl := caddy.NewReplacer()
		emptyCtx := context.WithValue(emptyReq.Context(), caddy.ReplacerCtxKey, emptyRepl)
		emptyReq = emptyReq.WithContext(emptyCtx)

		emptyRepl.Map(func(key string) (any, bool) {
			if key == "tailscale.grants" {
				return map[string]any{}, true
			}
			return nil, false
		})

		// Empty map is not null, so == null should be false
		expr := &caddyhttp.MatchExpression{
			Expr: `{tailscale.grants} == null`,
		}
		if err := expr.Provision(caddyCtx); err != nil {
			t.Fatalf("Provision failed: %v", err)
		}
		matches, err := expr.MatchWithError(emptyReq)
		if err != nil {
			t.Fatalf("CEL evaluation failed: %v", err)
		}
		if matches {
			t.Error("expected tailscale.grants to not be null when grants are empty")
		}
	})
}
