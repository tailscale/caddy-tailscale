# caddy tailscale auth

Using [nginx-auth][] together [with Caddy][] is pretty cool.  But since Caddy
is written in Go anyway, what if we combined the two?

This repo provides a Caddy module that will talk with a local tailscaled in
exactly the same way that nginx-auth does, but without needing a separate
binary.  Alternately, you can provide an auth_key and caddy will use tsnet to
join your tailnet directly without needing tailscaled.

[nginx-auth]: https://github.com/tailscale/tailscale/tree/main/cmd/nginx-auth
[with Caddy]: https://caddyserver.com/docs/caddyfile/directives/forward_auth#tailscale

## demo

First, you'll need to install [xcaddy][] to build a caddy binary with custom modules.

    $ go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest

[xcaddy]: https://github.com/caddyserver/xcaddy

Clone this repo and run `xcaddy run` to build and run a caddy server.

**NOTE:** The first time you run it, you may be prompted for your password
because caddy uses your system certificate store to [install a certificate
authority] to sign development certificates.

[install a certificate authority]: https://caddyserver.com/docs/automatic-https

Once caddy starts up, open http://yourhost:7000/ in your browser.  You actually
need to use your tailscale hostname or IP address; you can't use localhost.
Ideally, you should be greeted with your tailscale account info.  Now visit
http://yourhost:7100/ and you should see the same information in a little
different format.  This demonstrates two different ways that the module can be
used (see more details in [Caddyfile](./Caddyfile)).

  1. port 7000 - authenticate as normal and populate the standard caddy user object
  2. port 7100 - do the same, but populate X-Webauth headers and proxy the
     request to a separate application that knows nothing about Tailscale.

Some other options you can try:

Specify an auth key to use tsnet mode:

    tailscale_auth {
      auth_key ts-key-xxxxxxCNTRL-xxxxxx
    }

    # or with an environment variable
    tailscale_auth {
      auth_key {env.TS_AUTH_KEY}
    }

In order to use the environment variable form, you'll need to actually build a
caddy binary rather than just using `xcaddy run`.  To do that:

    xcaddy build --with github.com/tailscale/caddy=./
    TS_AUTH_KEY=ts-key-xxxxxxCNTRL-xxxxxx ./caddy run

When using tsnet mode, you can also specify a custom hostname for your node as
well as verbose logging:

```
    tailscale_auth {
      auth_key ts-key-xxxxxxCNTRL-xxxxxx
      hostname myhost
      verbose
    }
```

tsnet mode still actually requires a local tailscaled running so that caddy can
listen on the tailscale network interface.  We're looking into options to remove
the need for tailscaled at all.

# Related work

It looks like <https://github.com/astrophena/tsid> is very similar and did this
in mid 2021, but hooks into caddy as an http handler rather than an
authentication provider. It also doesn't support tsnet, which likely didn't
exist at the time.
