# Tailscale Caddy plugin

[![status: experimental](https://img.shields.io/badge/status-experimental-blue)](https://tailscale.com/kb/1167/release-stages/#experimental)

The Tailscale Caddy plugin brings Tailscale integration to the Caddy web server.
It's really multiple plugins in one, providing:

 - the ability for a Caddy server to directly join your Tailscale network
   without needing a separate Tailscale client.
 - a Caddy authentication provider, so that you can pass a user's Tailscale
   identity to an applicatiton.
 - a Caddy subcommand to quickly setup a reverse-proxy using either or both of
   the network listener or authentication provider.

This plugin is still very experimental.

## Installation

Use [xcaddy](https://github.com/caddyserver/xcaddy) to build Caddy with the
Tailscale plugin included.

```
xcaddy build v2.6.4 --with github.com/tailscale/caddy-tailscale
```

## Caddy network listener

New in Caddy 2.6, modules are able to provide custom network listeners. This
allows your Caddy server to directly join your Tailscale network without needing
a separate Tailcale client running on the machine exposing a network device.
Each site can be configured in Caddy to join your network as a separate node, or
you can have multiple sites listening on different ports of a single node.

### Configuration

Configure Caddy to listen on a special "tailscale" network address.  If using a
Caddyfile, use the [bind directive](https://caddyserver.com/docs/caddyfile/directives/bind):

```
:80 {
    bind tailscale/
}
```

You can also specify a hostname to use for the Tailscale node:

```
:80 {
    bind tailscale/myhost
}
```

If using the Caddy JSON configuration, specify a "tailscale/" network in your
listen address:

```json
{
  "apps": {
    "http": {
      "servers": {
        "srv0": {
          "listen": [
            "tailscale/myhost:80"
          ]
        }
      }
    }
  }
}
```

Caddy will join your Tailscale network and listen only on that network
interface.  Multiple addresses can be specified if you want to listen on the
Tailscale address as well as a local address:

```
:80 {
  bind tailscale/myhost localhost
}
```

Different sites can be configured to join the network as different nodes:

```
:80 {
  bind tailscale/a
}

:80 {
  bind tailscale/b
}
```

However, having a single Caddy site connect to separate Tailscale nodes doesn't
quite work correctly. If this is something you actually need, please open an
issue.

### HTTPS support

At this time, the Tailscale plugin for Caddy doesn't support using Caddy's
native HTTPS resolvers. You will need to use the `tailscale+tls` bind protocol
with a configuration like this:

```
{
    order tailscale_auth after basicauth
    auto_https off
}

:443 {
    bind tailscale+tls/myhost
}
```

Please note that because you currently need to turn `auto_https` support off, it
is not advised to use the same instance of Caddy for your external-facing apps
as you use for your internal-facing apps. This deficiency will be resolved as
soon as possible.

### Authenticating to the Tailcale network

New nodes can be added to your Tailscale network by providing an [Auth
key](https://tailscale.com/kb/1085/auth-keys/) or by following a special URL.
Auth keys are provided to Caddy via the `TS_AUTHKEY` or `TS_AUTHKEY_<HOST>`
environment variable.  So if your network listener was `tailscale/myhost`, then
it would look first for the `TS_AUTHKEY_MYHOST` environment variable, then
`TS_AUTHKEY`.

If no auth key is provided, then Tailscale will generate a URL that can be used
to add the new node and print it to the Caddy log.  Tailscale logs can be
somewhat noisy so are turned off by default. Set `TS_VERBOSE=1` to see the URL
logged.  After the node had been added to your network, you can restart Caddy
without the debug flag.


## Caddy authentication provider

Setup the Tailscale authentication provider with `tailscale_auth` directive.
The provider will enforce that all requests are coming from a Tailscale user, as
well as set various fields on the Caddy user object that can be passed to
applications, similar to [nginx-auth][].

[nginx-auth]: https://github.com/tailscale/tailscale/tree/main/cmd/nginx-auth

Set the [`order`](https://caddyserver.com/docs/caddyfile/options#order)
directive in your global options to instruct Caddy when to process
`tailscale_auth`.  For example, in a Caddyfile:

```
{
  order tailscale_auth after basicauth
}

:80 {
  tailscale_auth
}
```

The following fields are set on the Caddy user object:

 - `user.id`: the Tailscale email-ish user ID
 - `user.tailscale_login`: the username portion of the Tailscale user ID
 - `user.tailscale_user`: same as `user.id`
 - `user.tailscale_name`: the display name of the Tailscale user
 - `user.tailscale_profile_picture`: the URL of the Tailscale user's profile picture
 - `user.tailscale_tailnet`: the name of the Tailscale network the user is a member of

These can be mapped to HTTP headers passed to an application using something
like the following in your Caddyfile:

```
header_up X-Webauth-User {http.auth.user.tailscale_login}
header_up X-Webauth-Email {http.auth.user.tailscale_user}
header_up X-Webauth-Name {http.auth.user.tailscale_name}
```

When used with a Tailscale listener (described above), that Tailscale connection
is used to identify the remote user.  Otherwise, the authentication provider
will attempt to connect to the Tailscale daemon running on the local machine.

## tailscale-proxy subcommand

The Tailscale Caddy plugin also includes a `tailscale-proxy` subcommand that
sets up a simple reverse proxy that can optionally join your Tailscale network,
and will enforce Tailscale authentication and map user values to HTTP headers.

For example:

```
xcaddy tailscale-proxy --from "tailscale/myhost:80" --to localhost:8000
```
