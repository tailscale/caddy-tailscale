package tscaddy

import (
	"errors"
	"net"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"tailscale.com/util/must"
)

func Test_GetAuthKey(t *testing.T) {
	const host = "host"
	tests := map[string]struct {
		env        map[string]string // env vars to set
		defaultKey string            // default key in caddy config
		hostKey    string            // host key in caddy config
		want       string
	}{
		"default key from environment": {
			env:  map[string]string{"TS_AUTHKEY": "envkey"},
			want: "envkey",
		},
		"default key from caddy": {
			env:        map[string]string{"TS_AUTHKEY": "envkey"},
			defaultKey: "defaultkey",
			want:       "defaultkey",
		},
		"default key from caddy placeholder": {
			env: map[string]string{
				"TS_AUTHKEY": "envkey",
				"MYKEY":      "mykey",
			},
			defaultKey: "{env.MYKEY}",
			want:       "mykey",
		},
		"host key from environment": {
			env:  map[string]string{"TS_AUTHKEY_HOST": "envhostkey"},
			want: "envhostkey",
		},
		"host key from caddy": {
			env:     map[string]string{"TS_AUTHKEY": "envkey"},
			hostKey: "hostkey",
			want:    "hostkey",
		},
		"host key from caddy placeholder": {
			env:     map[string]string{"MYKEY": "mykey"},
			hostKey: "{env.MYKEY}",
			want:    "mykey",
		},
		"empty key from empty env var": {
			hostKey: "{env.DOES_NOT_EXIST}",
			want:    "",
		},
		"empty key from bad placeholder": {
			hostKey: "{bad.placeholder}",
			want:    "",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			app := &TSApp{
				DefaultAuthKey: tt.defaultKey,
				Servers:        make(map[string]TSServer),
			}
			app.Provision(caddy.Context{})
			if tt.hostKey != "" {
				app.Servers[host] = TSServer{
					AuthKey: tt.hostKey,
				}
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			got, _ := getAuthKey(host, app)
			if got != tt.want {
				t.Errorf("GetAuthKey() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_Listen(t *testing.T) {
	must.Do(caddy.Run(new(caddy.Config)))
	ctx := caddy.ActiveContext()

	svr, err := getServer(ctx, "testhost")
	if err != nil {
		t.Fatal("failed to get server", err)
	}

	ln, err := svr.Listen("tcp", ":80")
	if err != nil {
		t.Fatal("failed to listen", err)
	}
	count, exists := servers.References("testhost")
	if !exists && count != 1 {
		t.Fatal("reference doesn't exist")
	}
	ln.Close()

	count, exists = servers.References("testhost")
	if exists && count != 0 {
		t.Fatal("reference exists when it shouldn't")
	}

	err = svr.Close()
	if !errors.Is(err, net.ErrClosed) {
		t.Fatal("unexpected error", err)
	}
}
