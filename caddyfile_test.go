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
					auth_key abcdefghijklmnopqrstuvwxyz
				}`),
			want: `{"auth_key":"abcdefghijklmnopqrstuvwxyz"}`,
		},
		{
			name: "ephemeral",
			d: caddyfile.NewTestDispenser(`
				tailscsale {
					ephemeral
				}`),
			want: `{"ephemeral":true}`,
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
			want: `{"servers":{"foo":{}}}`,
		},
		{
			name: "tailscale with server",
			d: caddyfile.NewTestDispenser(`
				tailscsale {
					foo {
						auth_key  abcdefghijklmnopqrstuvwxyz
					}
				}`),
			want:    `{"servers":{"foo":{"auth_key":"abcdefghijklmnopqrstuvwxyz"}}}`,
			wantErr: false,
		},
	}

	for _, testcase := range tests {
		t.Run(testcase.name, func(t *testing.T) {
			got, err := parseApp(testcase.d, nil)
			if err != nil {
				if !testcase.wantErr {
					t.Errorf("parseApp() error = %v, wantErr %v", err, testcase.wantErr)
					return
				}
				return
			}
			if testcase.wantErr && err == nil {
				t.Errorf("parseApp() err = %v, wantErr %v", err, testcase.wantErr)
				return
			}
			gotJSON := string(got.(httpcaddyfile.App).Value)
			if diff := compareJSON(gotJSON, testcase.want, t); diff != "" {
				t.Errorf("parseApp() diff(-got +want):\n%s", diff)
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
