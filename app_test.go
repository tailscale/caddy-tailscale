// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: Apache-2.0

package tscaddy

import (
	"encoding/json"
	"testing"

	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/google/go-cmp/cmp"
)

func Test_ParseApp(t *testing.T) {
	tests := []struct {
		name    string
		d       *caddyfile.Dispenser
		want    string
		authKey string
		wantErr bool
	}{
		{

			name: "empty",
			d: caddyfile.NewTestDispenser(`
				tailscsale {}
			`),
			want: `{}`,
		},
		{
			name: "auth_key",
			d: caddyfile.NewTestDispenser(`
				tailscsale {
					auth_key tskey-default
				}`),
			want:    `{"auth_key":"tskey-default"}`,
			authKey: "tskey-default",
		},
		{
			name: "ephemeral",
			d: caddyfile.NewTestDispenser(`
				tailscsale {
					ephemeral
				}`),
			want:    `{"ephemeral":true}`,
			authKey: "",
		},
		{
			name: "ephemeral: true",
			d: caddyfile.NewTestDispenser(`
				tailscsale {
					ephemeral true
				}`),
			want:    `{"ephemeral":true}`,
			authKey: "",
		},
		{
			name: "ephemeral: 1",
			d: caddyfile.NewTestDispenser(`
				tailscsale {
					ephemeral 1
				}`),
			want:    `{"ephemeral":true}`,
			authKey: "",
		},
		{
			name: "ephemeral: false",
			d: caddyfile.NewTestDispenser(`
				tailscsale {
					ephemeral false
				}`),
			want:    `{}`, // no value because omitempty
			authKey: "",
		},
		{
			name: "missing auth key",
			d: caddyfile.NewTestDispenser(`
				tailscsale {
					auth_key
				}`),
			wantErr: true,
		},
		{
			name: "empty server",
			d: caddyfile.NewTestDispenser(`
				tailscsale {
					foo
				}`),
			want: `{"nodes":{"foo":{}}}`,
		},
		{
			name: "tailscale with server",
			d: caddyfile.NewTestDispenser(`
				tailscsale {
					auth_key tskey-default
					foo {
						auth_key tskey-node
						ephemeral false
						webui false
					}
				}`),
			want:    `{"auth_key":"tskey-default","nodes":{"foo":{"auth_key":"tskey-node","ephemeral":false,"webui":false}}}`,
			wantErr: false,
			authKey: "tskey-node",
		},
	}

	for _, testcase := range tests {
		t.Run(testcase.name, func(t *testing.T) {
			got, err := parseAppConfig(testcase.d, nil)
			if err != nil {
				if !testcase.wantErr {
					t.Errorf("parseApp() error = %v, wantErr %v", err, testcase.wantErr)
					return
				}
				return
			} else if testcase.wantErr {
				t.Errorf("parseApp() err = %v, wantErr %v", err, testcase.wantErr)
				return
			}
			gotJSON := string(got.(httpcaddyfile.App).Value)
			if diff := compareJSON(gotJSON, testcase.want, t); diff != "" {
				t.Errorf("parseApp() diff(-got +want):\n%s", diff)
			}
			app := new(App)
			if err := json.Unmarshal([]byte(gotJSON), &app); err != nil {
				t.Error("failed to unmarshal json into App")
			}
		})
	}

}

func compareJSON(s1, s2 string, t *testing.T) string {
	var v1, v2 map[string]any
	if err := json.Unmarshal([]byte(s1), &v1); err != nil {
		t.Error(err)
	}
	if err := json.Unmarshal([]byte(s2), &v2); err != nil {
		t.Error(err)
	}

	return cmp.Diff(v1, v2)
}
