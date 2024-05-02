package tscaddy

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
)

func Test_GetAuthKey(t *testing.T) {

	const testkey = "abcdefghijklmnopqrstuvwxyz"
	const testHostKey = "1234567890"
	const testenvkey = "zyxwvutsrqponmlkjihgfedca"
	const testHost = "unittest"

	tests := map[string]struct {
		host    string
		skipApp bool
		want    string
	}{
		"default key from environment": {
			want:    testenvkey,
			skipApp: true,
			host:    "testhost",
		},
		"host key from module": {
			want: testHostKey,
			host: testHost,
		},
		"host key from environment": {
			want:    testHostKey,
			skipApp: true,
			host:    testHost,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			app := &TSApp{
				Servers: make(map[string]TSServer),
			}
			if !tt.skipApp {
				app.DefaultAuthKey = testkey
				app.Servers[testHost] = TSServer{
					AuthKey: testHostKey,
				}
			}
			t.Setenv("TS_AUTHKEY", testenvkey)
			hostKey := fmt.Sprintf("TS_AUTHKEY_%s", strings.ToUpper(testHost))
			t.Setenv(hostKey, testHostKey)

			got := getAuthKey(tt.host, app)
			if got != tt.want {
				t.Errorf("GetAuthKey() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_Listen(t *testing.T) {
	svr, err := getServer("", "testhost")
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
