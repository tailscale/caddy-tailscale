package tscaddy

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/caddyauth"
	"tailscale.com/client/tailscale"
	"tailscale.com/tsnet"
)

var (
	servers = caddy.NewUsagePool()
	app     *TSApp
)

func init() {
	app = &TSApp{
		Servers: map[string]TSServer{},
	}
	caddy.RegisterModule(TailscaleAuth{})
	httpcaddyfile.RegisterHandlerDirective("tailscale_auth", parseCaddyfile)
	caddy.RegisterNetwork("tailscale", getPlainListener)
	caddy.RegisterNetwork("tailscale+tls", getTLSListener)
	caddy.RegisterModule(&TailscaleCaddyTransport{})
}

func getPlainListener(_ context.Context, _ string, addr string, _ net.ListenConfig) (any, error) {
	network, host, port, err := caddy.SplitNetworkAddress(addr)
	if err != nil {
		return nil, err
	}

	s, err := getServer("", host)
	if err != nil {
		return nil, err
	}

	if network == "" {
		network = "tcp"
	}

	ln := &tsnetServerDestructor{
		Server: s.Server,
	}
	return ln.Listen(network, ":"+port)
}

func getTLSListener(_ context.Context, _ string, addr string, _ net.ListenConfig) (any, error) {
	network, host, port, err := caddy.SplitNetworkAddress(addr)
	if err != nil {
		return nil, err
	}

	s, err := getServer("", host)
	if err != nil {
		return nil, err
	}

	if network == "" {
		network = "tcp"
	}
	ln, err := s.Listen(network, ":"+port)
	if err != nil {
		return nil, err
	}

	localClient, _ := s.LocalClient()

	ln = tls.NewListener(ln, &tls.Config{
		GetCertificate: localClient.GetCertificate,
	})

	return ln, nil
}

// getServer returns a tailscale tsnet.Server for Caddy apps to listen on. The specified
// address will take the form of "tailscale/host:port" or "tailscale+tls/host:port" with
// host being optional. If specified, host will be provided to tsnet as the desired
// hostname for the tailscale node. Only one tsnet server is created per host, even if
// multiple ports are being listened on for the host.
//
// Auth keys can be provided in environment variables of the form TS_AUTHKEY_<HOST>.  If
// no host is specified in the address, the environment variable TS_AUTHKEY will be used.
func getServer(_, addr string) (*tsnetServerDestructor, error) {
	_, host, _, err := caddy.SplitNetworkAddress(addr)
	if err != nil {
		return nil, err
	}

	s, _, err := servers.LoadOrNew(host, func() (caddy.Destructor, error) {
		s := &tsnet.Server{
			Hostname: host,
			Logf: func(format string, args ...any) {
				// TODO: parse out and always log authURL so you don't need
				// to turn on debug logging to get it.
				if os.Getenv("TS_VERBOSE") == "1" {
					log.Printf(format, args...)
				}
			},
		}

		if host != "" {
			s.AuthKey = getAuthKey(host, app)
			s.Ephemeral = getEphemeral(host, app)

			// Set config directory for tsnet.  By default, tsnet will use the name of the
			// running program, but we include the hostname as well so that a single
			// caddy instance can have multiple tsnet servers.
			configDir, err := os.UserConfigDir()
			if err != nil {
				return nil, err
			}
			s.Dir = path.Join(configDir, "tsnet-caddy-"+host)
			if err := os.MkdirAll(s.Dir, 0700); err != nil {
				return nil, err
			}
		}

		return &tsnetServerDestructor{
			s,
		}, nil
	})
	if err != nil {
		return nil, err
	}

	return s.(*tsnetServerDestructor), nil
}

func getAuthKey(host string, app *TSApp) string {
	svr := app.Servers[host]
	if svr.AuthKey != "" {
		return svr.AuthKey
	}

	if app.DefaultAuthKey != "" {
		return app.DefaultAuthKey
	}

	// Set authkey to "TS_AUTHKEY_<HOST>".  If empty,
	// fall back to "TS_AUTHKEY".
	authKey := os.Getenv("TS_AUTHKEY_" + strings.ToUpper(host))
	if authKey == "" {
		authKey = os.Getenv("TS_AUTHKEY")
	}
	return authKey
}

func getEphemeral(host string, app *TSApp) bool {
	if svr, ok := app.Servers[host]; ok {
		return svr.Ephemeral
	}

	return app.Ephemeral
}

type TailscaleAuth struct {
	localclient *tailscale.LocalClient
}

func (TailscaleAuth) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.authentication.providers.tailscale",
		New: func() caddy.Module { return new(TailscaleAuth) },
	}
}

// client returns the tailscale LocalClient for the TailscaleAuth module.  If the LocalClient
// has not already been configured, the provided request will be used to set it up for the
// appropriate tsnet server.
func (ta *TailscaleAuth) client(r *http.Request) (*tailscale.LocalClient, error) {
	if ta.localclient != nil {
		return ta.localclient, nil
	}

	// if request was made through a tsnet listener, set up the client for the associated tsnet
	// server.
	server := r.Context().Value(caddyhttp.ServerCtxKey).(*caddyhttp.Server)
	for _, listener := range server.Listeners() {
		if tsl, ok := listener.(tsnetListener); ok {
			var err error
			ta.localclient, err = tsl.Server().LocalClient()
			if err != nil {
				return nil, err
			}
		}
	}

	if ta.localclient == nil {
		// default to empty client that will talk to local tailscaled
		ta.localclient = new(tailscale.LocalClient)
	}

	return ta.localclient, nil
}

type tsnetListener interface {
	Server() *tsnet.Server
}

func (ta TailscaleAuth) Authenticate(w http.ResponseWriter, r *http.Request) (caddyauth.User, bool, error) {
	user := caddyauth.User{}

	client, err := ta.client(r)
	if err != nil {
		return user, false, err
	}

	info, err := client.WhoIs(r.Context(), r.RemoteAddr)
	if err != nil {
		return user, false, err
	}

	if len(info.Node.Tags) != 0 {
		return user, false, fmt.Errorf("node %s has tags", info.Node.Hostinfo.Hostname())
	}

	var tailnet string
	if !info.Node.Hostinfo.ShareeNode() {
		if s, found := strings.CutPrefix(info.Node.Name, info.Node.ComputedName+"."); found {
			if s, found := strings.CutSuffix(s, ".beta.tailscale.net."); found {
				tailnet = s
			}
		}
	}

	user.ID = info.UserProfile.LoginName
	user.Metadata = map[string]string{
		"tailscale_login":           strings.Split(info.UserProfile.LoginName, "@")[0],
		"tailscale_user":            info.UserProfile.LoginName,
		"tailscale_name":            info.UserProfile.DisplayName,
		"tailscale_profile_picture": info.UserProfile.ProfilePicURL,
		"tailscale_tailnet":         tailnet,
	}
	return user, true, nil
}

func parseCaddyfile(_ httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var ta TailscaleAuth

	return caddyauth.Authentication{
		ProvidersRaw: caddy.ModuleMap{
			"tailscale": caddyconfig.JSON(ta, nil),
		},
	}, nil
}

type tsnetServerDestructor struct {
	*tsnet.Server
}

func (t tsnetServerDestructor) Destruct() error {
	return t.Close()
}

func (t *tsnetServerDestructor) Listen(network string, addr string) (net.Listener, error) {
	ln, err := t.Server.Listen(network, addr)
	if err != nil {
		return nil, err
	}
	serverListener := &tsnetServerListener{
		hostname: t.Hostname,
		Listener: ln,
	}
	return serverListener, nil
}

type tsnetServerListener struct {
	hostname string
	net.Listener
}

func (t *tsnetServerListener) Close() error {
	fmt.Println("Delete", t.hostname)
	_, err := servers.Delete(t.hostname)
	return err
}
