// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: Apache-2.0

package tscaddy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"tailscale.com/client/local"
)

func authTestRequest() *http.Request {
	request := httptest.NewRequest("GET", "http://example.com/", nil)
	request.RemoteAddr = "100.64.0.1:1234"
	return request.WithContext(context.WithValue(request.Context(), caddyhttp.ServerCtxKey, &caddyhttp.Server{}))
}

func TestAuthenticateReusesLocalClient(t *testing.T) {
	auth := new(Auth)
	request := authTestRequest()
	ctx, cancel := context.WithCancel(request.Context())
	cancel()
	request = request.WithContext(ctx)

	// A canceled WhoIs call must still leave the initialized client available.
	_, _, _ = auth.Authenticate(httptest.NewRecorder(), request)
	first := auth.localclient
	if first == nil {
		t.Fatal("Authenticate did not retain its local client")
	}

	_, _, _ = auth.Authenticate(httptest.NewRecorder(), request)
	if auth.localclient != first {
		t.Fatal("Authenticate replaced its local client")
	}
}

func TestAuthClientConcurrent(t *testing.T) {
	auth := new(Auth)
	request := authTestRequest()
	const count = 32
	clients := make([]*local.Client, count)
	errs := make([]error, count)
	var wg sync.WaitGroup
	for i := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			clients[i], errs[i] = auth.client(request)
		}()
	}
	wg.Wait()

	for i, client := range clients {
		if errs[i] != nil {
			t.Fatalf("client %d: %v", i, errs[i])
		}
		if client != clients[0] {
			t.Fatalf("client %d differs from the first client", i)
		}
	}
}
