# Architecture

This document describes how the Hallmaster Proxy sits between Discord bot
containers and Discord's infrastructure, the Docker networking that makes the
interception transparent to the bots, and the request lifecycle inside the
proxy itself.

## High-level picture

```
+----------------------+           +----------------------+           +---------------+
|  bot container       |           |  hallmaster-proxy    |           |  Discord      |
|  (Node, Python, ...) |  HTTPS /  |  container           |  HTTPS /  |  discord.com  |
|                      |   WSS     |                      |   WSS     |  gateway.dgg  |
|  trusts hallmaster   | <-------> |  terminates TLS with | <-------> |               |
|  Root CA             |           |  per-host leaf cert  |           |               |
+----------------------+           +----------------------+           +---------------+
        ^                                    ^
        |                                    |
        | docker network DNS aliases         | external DNS (8.8.8.8:53)
        | (discord.com -> proxy)             | bypass to reach real Discord IPs
        |                                    |
        +------------ hallmaster-proxy-net --+
```

Every container — proxy and bots — joins the user-defined Docker network
`hallmaster-proxy-net`. The proxy registers itself with the Docker DNS server
under several aliases:

- `discord.com`
- `discord.gg`
- `gateway.discord.gg`

so that any in-container DNS lookup for those hostnames resolves to the proxy's
IP on that network. The Hallmaster Runner base image (which bot images inherit
from) also installs `iptables` rules that redirect outgoing traffic on ports
80 and 443 to the proxy, catching the cases where a bot dials by raw IP rather
than hostname.

The end result is that the bot believes it is talking directly to Discord, but
its TLS handshake terminates inside the proxy.

## Components

```
proxy/
├── main.go                                  wires everything together
└── internals/
    ├── mitm.go                              MITMProxy + HandlerDeps + Handshaker + Serve
    ├── certs/                               Root CA loading, on-the-fly leaf signing (cached + auto-renewing)
    ├── config/                              env-driven Config struct
    ├── discord/                             gateway-specific bits (zlib-stream decoder)
    ├── dnsbypass/                           external DNS resolver (Resolver interface)
    ├── handlers/
    │   ├── https.go                         per-request HTTPS forwarding + Tamperer hooks
    │   └── ws.go                            bidirectional WebSocket pump + Tamperer hooks
    ├── healthz/                             loopback /healthz, 200 if ready() else 503
    ├── httpio/                              pure DecodeBody + framing-only Encode
    ├── internaltest/                        shared test helpers (not imported from production)
    └── tamper/                              Tamperer interface + Nop + Logging implementations
```

## Request lifecycle

### 1. Connection accept

The proxy listens for TCP on `0.0.0.0:$PROXY_PORT` (typically `443`).
`MITMProxy.Serve(ctx, ln, deps, handler)` runs the accept loop until `ctx`
is cancelled or the listener errors out; `Listen` is a thin convenience
wrapper around `Serve(context.Background(), net.Listen(…), …)` used by
`main.go`. For each accepted connection the proxy peeks the first byte:

- `0x16` -> TLS ClientHello, treat as a direct TLS connection.
- Anything else -> read a plain HTTP request; if it is `CONNECT host:port`,
  acknowledge with `200 Connection established` and treat the remainder of the
  socket as a TLS handshake. Other HTTP methods are rejected.

This dual-mode accept means both `bot -> proxy: HTTPS` (when an `iptables`
redirect lands a real Discord-bound TLS handshake on the proxy) and
`bot -> proxy: HTTP CONNECT` (when the runtime is configured to use the proxy
explicitly, e.g. `NODE_USE_ENV_PROXY=1`) both work.

### 2. TLS termination with a freshly signed leaf

On every TLS handshake the proxy uses a `GetCertificate` callback. The callback
reads the SNI hostname from `ClientHelloInfo.ServerName`, asks the
`MITMProxyCerts` cache for a leaf cert for that hostname, and returns it.

`MITMProxyCerts`:

- Loads the Root CA (public cert + private key) at startup from the paths in
  `PROXY_SSL_CA_CERT_PATH` / `PROXY_SSL_CA_KEY_PATH`. Refuses to load a key
  whose file mode permits group/other read.
- On every new hostname, generates a 2048-bit RSA key and a leaf cert valid
  for 7 days, signed by the Root CA. `tls.Certificate.Leaf` is populated so
  the cache can check `NotAfter`.
- Caches the resulting `*tls.Certificate` in a `sync.Map`, with a
  `singleflight.Group` ensuring only one goroutine builds a given hostname's
  cert when many connections race for it.
- Auto-renews any cached cert within 24h of expiry by falling through into
  the singleflight regeneration path.

The bot, having the Root CA installed in its trust store, accepts the leaf
cert and the handshake completes — the bot now has a plaintext channel to the
proxy that it believes is a plaintext channel to Discord.

### 3. Upstream dial + DNS bypass

Because the bot's DNS lookups for `discord.com` etc. resolve to the proxy
itself, the proxy cannot reuse the same hostname to reach the *real* Discord.
It uses `dnsbypass.ExternalResolver` — a `net.Resolver` configured to dial
`$PROXY_DNS_SERVER` (default `8.8.8.8:53`) — to resolve the real upstream IP,
then dials `tcp` to `<real-ip>:<port>` with `ServerName` still set to the
original hostname so SNI and certificate validation against Discord's cert
both work.

A `cfg.UpstreamDialTimeout` (10s) bounds the dial. Tests can override the
dial itself via `HandlerDeps.DialUpstream` (e.g. to route through `net.Pipe`)
and can inject a custom `RootCAs` / `InsecureSkipVerify` posture via
`HandlerDeps.UpstreamTLSConfig` — the handler clones it before setting
`ServerName` and `NextProtos`.

The proxy maintains one upstream TLS connection per
`(client TLS session, originalHost)` pair. If a subsequent request on the
same client session targets a different host, the existing upstream is
closed and a fresh dial is made — pipelining across hosts is supported by
re-dialling, not multiplexing.

### 4. Forwarding + tampering

Once the upstream TLS connection is up, [handlers/https.go](../proxy/internals/handlers/https.go)
loops:

1. `http.ReadRequest` from the client.
2. Decide whether to intercept or blind-forward via `isDiscordHost(req.Host)`
   (suffix allowlist: `discord.com`, `discord.gg`, `gateway.discord.gg`,
   plus their one-level subdomains) or relay detection (`req.Host` equals
   the proxy hostname). Anything else falls through to a blind `io.Copy`
   pump in both directions.
3. For intercepted traffic: hand the request to `Tamperer.Request`, write
   the (possibly rewritten) request upstream, read the response back, call
   `httpio.DecodeBody(resp)` to get a decoded `[]byte` view for the
   tamperer, hand both to `Tamperer.Response(req, resp, decodedBody)`,
   normalise framing via `httpio.Encode` (`Content-Length` reset,
   `Transfer-Encoding` dropped — the body bytes themselves are forwarded
   as-is, matching the original `Content-Encoding`), and write back to the
   client.
4. Nested `CONNECT` inside an already-tunnelled session is handled
   iteratively in the same loop, capped at `maxConnectDepth = 4`. The outer
   upstream is closed before re-handshaking against the inner host.
5. If the response is `101 Switching Protocols` for a WebSocket upgrade, the
   loop hands off to `InspectWS` (see below) and stops reading HTTP from
   this connection.

### 5. WebSocket inspection

For Discord's gateway, the bot sends a `GET /?v=10&encoding=json&compress=zlib-stream`
which upgrades to WebSocket. After the `101` response is mirrored back to the
bot, [handlers/ws.go](../proxy/internals/handlers/ws.go)'s `InspectWS` pumps
frames in both directions concurrently:

- `bot -> Discord`: read a frame off the client, unmask if needed, hand the
  unmasked payload to `Tamperer.WSOutgoing` (whose return value is forwarded
  upstream).
- `Discord -> bot`: read a frame off the upstream. If `compress=zlib-stream`
  was negotiated, feed binary frames into a stateful `ZlibStreamDecoder` so
  the Tamperer sees readable JSON; *but* always forward the original
  compressed frame to the bot — bots that negotiated compression expect
  compressed bytes. For uncompressed connections, the Tamperer's return value
  IS what gets forwarded.

When either direction hits an `OpClose` or an error, both goroutines drain and
the connection terminates.

## Networking caveats

- **DNS aliases vs `iptables`**: DNS aliases catch hostname-based connections.
  `iptables` (installed by the Hallmaster Runner image) catches IP-based
  connections (some runtimes resolve once and dial the IP from then on, or
  ship hard-coded fallback IPs).
- **External resolver leak**: `PROXY_DNS_SERVER` is dialled via UDP from the
  proxy container. The Docker network must allow egress to that resolver, or
  Discord hostnames will never be resolved.
- **Relay traffic**: if a runtime uses the proxy as an explicit HTTP proxy
  (e.g. `HTTPS_PROXY=http://hallmaster-proxy:443`), the bot dials the proxy
  and then issues `CONNECT discord.com:443`. The proxy's `req.Host` will be
  the bot's view of the target (Discord), and the `req.Host == proxyHostPort`
  branch covers the case where a bot accidentally targets the proxy
  directly.
- **The proxy is not designed to be reachable from outside the Docker
  network**. It binds `0.0.0.0` on `PROXY_PORT` inside the container, but no
  port is published in the example compose. Keep it that way: this is a
  per-deployment MITM with a self-signed CA, not a public service.

## Health checks

The proxy starts a second HTTP server on `127.0.0.1:$PROXY_HEALTH_PORT`
serving `GET /healthz`. The endpoint returns `200 ok` once
`MITMProxy.Ready()` reports true (i.e. `Serve` has entered its accept loop)
and `503 not ready` otherwise. Docker's healthcheck `wget`s that endpoint
every 15s. Because it binds loopback only, it is not exposed on the Docker
network.

## What lives outside this repo

- **The Hallmaster Runner base image** — installs the Root CA into the system
  trust store and configures `iptables`. Bot containers extend this image.
- **The bot images themselves** — these are pulled into compose via `image:`
  or `build:`; the proxy is agnostic to what runtime they use as long as the
  runtime trusts the Root CA.
