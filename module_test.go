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
			app := &App{
				DefaultAuthKey: tt.defaultKey,
				Nodes:          make(map[string]Node),
			}
			app.Provision(caddy.Context{})
			if tt.hostKey != "" {
				app.Nodes[host] = Node{
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

func Test_GetControlURL(t *testing.T) {
	const nodeName = "node"
	tests := map[string]struct {
		env        map[string]string // env vars to set
		defaultURL string            // default control_url in caddy config
		nodeURL    string            // node control_url in caddy config
		want       string
	}{
		"default empty URL": {
			want: "",
		},
		"custom URL from app config": {
			defaultURL: "http://custom.example.com",
			want:       "http://custom.example.com",
		},
		"custom URL from node config": {
			defaultURL: "xxx",
			nodeURL:    "http://custom.example.com",
			want:       "http://custom.example.com",
		},
		"custom URL from env on app config": {
			env:        map[string]string{"CONTROL_URL": "http://env.example.com"},
			defaultURL: "{env.CONTROL_URL}",
			want:       "http://env.example.com",
		},
		"custom URL from env on node config": {
			env:        map[string]string{"CONTROL_URL": "http://env.example.com"},
			defaultURL: "xxx",
			nodeURL:    "{env.CONTROL_URL}",
			want:       "http://env.example.com",
		},
	}
	for tn, tt := range tests {
		t.Run(tn, func(t *testing.T) {
			app := &App{
				ControlURL: tt.defaultURL,
				Nodes:      make(map[string]Node),
			}
			if tt.nodeURL != "" {
				app.Nodes[nodeName] = Node{
					ControlURL: tt.nodeURL,
				}
			}
			app.Provision(caddy.Context{})
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			got, _ := getControlURL(nodeName, app)
			if got != tt.want {
				t.Errorf("GetControlURL() = %v, want %v", got, tt.want)
			}
		})
	}
}
func Test_GetHostname(t *testing.T) {
	const nodeName = "node"
	tests := map[string]struct {
		env      map[string]string // env vars to set
		hostname string            // hostname value in caddy config
		want     string
	}{
		"default hostname from node name": {
			want: nodeName,
		},
		"custom hostname from node config": {
			hostname: "custom",
			want:     "custom",
		},
		"custom hostname with env vars": {
			env:      map[string]string{"REGION": "eu", "ENV": "prod"},
			hostname: "custom-{env.REGION}-{env.ENV}",
			want:     "custom-eu-prod",
		},
	}
	for tn, tt := range tests {
		t.Run(tn, func(t *testing.T) {
			app := &App{Nodes: map[string]Node{
				nodeName: {Hostname: tt.hostname},
			}}
			app.Provision(caddy.Context{})
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			got, _ := getHostname(nodeName, app)
			if got != tt.want {
				t.Errorf("GetHostname() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_Listen(t *testing.T) {
	must.Do(caddy.Run(new(caddy.Config)))
	ctx := caddy.ActiveContext()

	node, err := getNode(ctx, "testhost")
	if err != nil {
		t.Fatal("failed to get server", err)
	}

	ln, err := node.Listen("tcp", ":80")
	if err != nil {
		t.Fatal("failed to listen", err)
	}
	count, exists := nodes.References("testhost")
	if !exists && count != 1 {
		t.Fatal("reference doesn't exist")
	}
	ln.Close()

	count, exists = nodes.References("testhost")
	if exists && count != 0 {
		t.Fatal("reference exists when it shouldn't")
	}

	err = node.Close()
	if !errors.Is(err, net.ErrClosed) {
		t.Fatal("unexpected error", err)
	}
}
