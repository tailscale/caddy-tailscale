package tscaddy

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"
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
		"default key from module": {
			want: testkey,
			host: "testhost",
		},
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
			want: testHostKey,
			host: testHost,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			app = caddy.NewUsagePool()
			if !tt.skipApp {
				app.LoadOrStore(authUsageKey, testkey)
				app.LoadOrStore(testHost, TSServer{
					AuthKey: testHostKey,
				})
			}
			os.Setenv("TS_AUTHKEY", testenvkey)
			hostKey := fmt.Sprintf("TS_AUTHKEY_%s", strings.ToUpper(testHost))
			os.Setenv(hostKey, testHostKey)
			t.Cleanup(func() {
				os.Unsetenv("TS_AUTHKEY")
				os.Unsetenv(hostKey)
			})

			got := getAuthKey(tt.host)
			if got != tt.want {
				t.Errorf("GetAuthKey() = %v, want %v", got, tt.want)
			}
		})
	}
}
