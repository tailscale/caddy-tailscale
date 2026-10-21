// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: Apache-2.0

package tscaddy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/headers"
	"tailscale.com/tailcfg"
)

var testCapMap = tailcfg.PeerCapMap{
	"example.com/app": []tailcfg.RawMessage{
		`{"admin":true,"role":"admin"}`,
	},
	"example.com/cap/sql": []tailcfg.RawMessage{
		`{"access":"read"}`,
	},
}

// newGrantsRequest returns a request whose replacer resolves
// {tailscale.grants} as Auth.Authenticate does for the given CapMap.
func newGrantsRequest(capMap tailcfg.PeerCapMap) (*http.Request, *caddy.Replacer) {
	req := httptest.NewRequest("GET", "http://example.com", nil)
	repl := caddy.NewReplacer()
	repl.Map(grantsReplacer(extractGrants(capMap)))
	ctx := context.WithValue(req.Context(), caddy.ReplacerCtxKey, repl)
	return req.WithContext(ctx), repl
}

// TestTailscaleGrantsCEL verifies that the tailscale.grants placeholder
// can be used in CEL expressions via the expression matcher.
func TestTailscaleGrantsCEL(t *testing.T) {
	caddyCtx, cancel := caddy.NewContext(caddy.Context{Context: context.Background()})
	defer cancel()

	tests := []struct {
		name   string
		capMap tailcfg.PeerCapMap
		expr   string
		want   bool
	}{
		{"in_operator_key_exists", testCapMap, `"example.com/app" in {tailscale.grants}`, true},
		{"in_operator_missing_key", testCapMap, `"example.com/nonexistent" in {tailscale.grants}`, false},
		{"map_index_with_list_size", testCapMap, `{tailscale.grants}["example.com/app"].size() == 1`, true},
		{"deep_field_access", testCapMap, `{tailscale.grants}["example.com/app"][0].admin == true`, true},
		{"string_field_access", testCapMap, `{tailscale.grants}["example.com/cap/sql"][0].access == "read"`, true},
		// With no grants, the placeholder is an empty map rather than nil, so
		// "in" returns false instead of failing with "no such overload".
		{"in_operator_empty_grants", nil, `"example.com/app" in {tailscale.grants}`, false},
		{"null_check_empty_grants", nil, `{tailscale.grants} == null`, false},
		{"size_empty_grants", nil, `{tailscale.grants}.size() == 0`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := newGrantsRequest(tt.capMap)
			expr := &caddyhttp.MatchExpression{Expr: tt.expr}
			if err := expr.Provision(caddyCtx); err != nil {
				t.Fatalf("Provision failed: %v", err)
			}
			matches, err := expr.MatchWithError(req)
			if err != nil {
				t.Fatalf("CEL evaluation failed: %v", err)
			}
			if matches != tt.want {
				t.Errorf("%s = %v, want %v", tt.expr, matches, tt.want)
			}
		})
	}
}

// TestTailscaleGrantsHeader verifies that the tailscale.grants placeholder
// renders as JSON when used in a header, matching the
// Tailscale-App-Capabilities header sent by tailscale serve.
func TestTailscaleGrantsHeader(t *testing.T) {
	tests := []struct {
		name   string
		capMap tailcfg.PeerCapMap
		want   string
	}{
		{
			name:   "grants",
			capMap: testCapMap,
			want:   `{"example.com/app":[{"admin":true,"role":"admin"}],"example.com/cap/sql":[{"access":"read"}]}`,
		},
		{
			name:   "no_grants",
			capMap: nil,
			want:   `{}`,
		},
		{
			name:   "empty_capability",
			capMap: tailcfg.PeerCapMap{"example.com/app": nil},
			want:   `{"example.com/app":[]}`,
		},
		{
			name:   "non_json_value",
			capMap: tailcfg.PeerCapMap{"example.com/app": {`not json`}},
			want:   `{"example.com/app":["not json"]}`,
		},
		{
			name:   "non_ascii",
			capMap: tailcfg.PeerCapMap{"example.com/app": {`{"name":"Zoë 🏠"}`}},
			want:   `{"example.com/app":[{"name":"Zo\u00eb \ud83c\udfe0"}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, repl := newGrantsRequest(tt.capMap)

			// Apply the header the same way header_up does.
			ops := &headers.HeaderOps{
				Set: http.Header{"Tailscale-App-Capabilities": []string{"{tailscale.grants}"}},
			}
			if err := ops.Provision(caddy.Context{}); err != nil {
				t.Fatalf("Provision failed: %v", err)
			}
			ops.ApplyToRequest(req)

			got := req.Header.Get("Tailscale-App-Capabilities")
			if got != tt.want {
				t.Errorf("header = %s, want %s", got, tt.want)
			}
			if !json.Valid([]byte(got)) {
				t.Errorf("header is not valid JSON: %s", got)
			}
			for i := 0; i < len(got); i++ {
				if got[i] < 0x20 || got[i] > 0x7e {
					t.Errorf("header contains non-printable-ASCII byte %#x: %s", got[i], got)
					break
				}
			}

			// The user metadata uses the same encoding.
			if s := repl.ReplaceAll("{tailscale.grants}", ""); s != tt.want {
				t.Errorf("ReplaceAll = %s, want %s", s, tt.want)
			}
		})
	}
}

// TestTailscaleGrantsNonASCIIRoundTrip verifies that escaping non-ASCII
// characters does not change the decoded value.
func TestTailscaleGrantsNonASCIIRoundTrip(t *testing.T) {
	grants := extractGrants(tailcfg.PeerCapMap{"example.com/app": {`{"name":"Zoë 🏠 日本"}`}})

	var got map[string][]map[string]string
	if err := json.Unmarshal([]byte(grants.String()), &got); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if name := got["example.com/app"][0]["name"]; name != "Zoë 🏠 日本" {
		t.Errorf("name = %q, want %q", name, "Zoë 🏠 日本")
	}
}
