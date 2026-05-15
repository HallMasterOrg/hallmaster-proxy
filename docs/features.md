# Features

This page is a feature inventory of the proxy as it stands today. Each entry
describes what the feature does, where it lives, and how it is exposed to
operators and the integration test suite.

## 1. Transparent HTTPS interception

The proxy terminates TLS for any client connection landed on it, presenting
a freshly-minted leaf certificate signed by the Hallmaster Root CA. As long
as the client trusts that Root CA, the bot believes it is talking directly
to Discord.

- Implementation: [proxy/internals/mitm.go](../proxy/internals/mitm.go),
  [proxy/internals/certs/certs.go](../proxy/internals/certs/certs.go).
- Two arrival modes are supported on the same listening port:
  - **Direct TLS** — first byte is `0x16` (ClientHello), typical when
    `iptables` redirects 443-bound traffic to the proxy.
  - **HTTP CONNECT** — the proxy acks `200 Connection established`, then a
    TLS handshake follows; typical when a runtime is configured to use the
    proxy as an explicit HTTPS proxy.
- Nested CONNECTs (`CONNECT` inside an already-CONNECT'd TLS tunnel) are
  unwrapped iteratively in the same handler loop, capped at
  `maxConnectDepth = 4` to bound recursion.

## 2. On-the-fly leaf certificate signing with caching and renewal

Per-hostname leaf certs are generated lazily, signed by the Root CA, cached
in memory keyed by hostname, and auto-renewed before they expire.

- Implementation: [proxy/internals/certs/certs.go](../proxy/internals/certs/certs.go).
- 2048-bit RSA, 7-day validity, `serverAuth` extended key usage, hostname in
  both Common Name and SAN DNSNames. `tls.Certificate.Leaf` is populated so
  the cache can read `NotAfter` without re-parsing.
- `singleflight.Group` deduplicates concurrent first-time requests for the
  same hostname so a connection spike only generates one cert.
- `sync.Map` storage means lookups are lock-free in the steady state.
- Renewal: `GetOrCreateCert` checks `NotAfter` against
  `leafCertRenewBefore = 24h` and falls through to the singleflight
  regeneration path when within the window. A long-running proxy keeps
  issuing valid leafs without operator intervention.
- The CA private key file is rejected at startup if its mode permits
  group/other read; `certificate-manager.sh` enforces `chmod 0600`.

## 3. External DNS bypass

Because Docker network DNS rewires `discord.com` to point at the proxy
itself, the proxy needs an out-of-band resolver to actually reach Discord.

- Implementation: [proxy/internals/dnsbypass/resolver.go](../proxy/internals/dnsbypass/resolver.go).
- `ExternalResolver` wraps a `net.Resolver` configured to dial a
  user-specified UDP DNS server (default `8.8.8.8:53`).
- The `Resolver` interface is the seam tests inject through to return
  canned IPs instead of hitting real DNS.

## 4. Suffix-allowlist host detection

The proxy intercepts traffic whose `Host` header matches a Discord
hostname; everything else falls through to blind passthrough.

- Implementation: `isDiscordHost` in [proxy/internals/handlers/https.go](../proxy/internals/handlers/https.go).
- Allowlist suffixes: `discord.com`, `discord.gg`, `gateway.discord.gg`.
  Matches are exact, one-level subdomain (`foo.discord.gg`), case- and
  trailing-dot-insensitive. `evil-discord.com.attacker.io` does NOT match.

## 5. Request/response tampering

A pluggable `Tamperer` interface gives feature code and tests a single hook
to observe or rewrite every HTTP request, every HTTP response, every
incoming WebSocket frame, and every outgoing WebSocket frame.

- Implementation: [proxy/internals/tamper/tamper.go](../proxy/internals/tamper/tamper.go).
- Interface methods:
  - `Request(req) (req, error)` — mutate or replace an outgoing HTTP request.
  - `Response(req, resp, decodedBody []byte) (resp, error)` — observe or
    rewrite an HTTP response before it is written back to the bot. The
    decoded body bytes are passed as a separate argument so the tamperer
    sees readable JSON without `resp.Body` or `Content-Encoding` being
    mutated under it.
  - `WSIncoming(payload) (payload, error)` — observe / rewrite a frame
    coming from Discord. For zlib-stream connections, the *decoded* bytes
    are passed; the *original compressed* bytes are forwarded (see §7).
  - `WSOutgoing(payload) (payload, error)` — observe / rewrite a frame
    coming from the bot before it goes to Discord.
- Error semantics: returning a non-nil error logs a warning and falls back
  to the unmodified original. Tampering is an enhancement, not a critical
  path.
- A tamperer that replaces `resp.Body` is responsible for keeping
  `Content-Encoding` consistent with the new bytes (clear it for plaintext,
  or re-encode to match).
- Two implementations ship today:
  - `tamper.Nop` — silent pass-through.
  - `tamper.Logging` — verbose pass-through; see §8.

The Tamperer is selected by `main.go` and passed to handlers via
`HandlerDeps.Tamperer`. The test suite swaps in its own recording
implementation by constructing a `HandlerDeps` directly (rather than going
through `main`).

## 6. WebSocket interception with bidirectional pumping

After the `101 Switching Protocols` handshake, the proxy hands the connection
to a frame-level pump that reads each WebSocket frame, runs it through the
Tamperer, and writes it on the other side.

- Implementation: [proxy/internals/handlers/ws.go](../proxy/internals/handlers/ws.go).
- Uses `github.com/gobwas/ws` for frame parsing and `wsutil` for masking.
- Two goroutines: `bot -> Discord` and `Discord -> bot`. When either side
  hits an `OpClose` or an error, both close.
- Masking is handled correctly: incoming client frames have their masks
  XOR'd off before the payload is passed to the Tamperer.

## 7. Discord gateway zlib-stream compression support

Discord's gateway optionally streams frames through a single shared zlib
deflate context terminated by the `Z_SYNC_FLUSH` marker (`00 00 ff ff`).
The proxy detects `compress=zlib-stream` in the gateway upgrade URL and
runs a stateful decoder so the Tamperer sees readable JSON.

- Implementation: [proxy/internals/discord/wscompress.go](../proxy/internals/discord/wscompress.go).
- `ZlibStreamDecoder` wraps an `io.Pipe` + `zlib.Reader` and decodes
  cumulatively across frames. `Decode([]byte)` returns the JSON bytes when
  a frame ends on a `Z_SYNC_FLUSH` boundary; intermediate frames return
  `(nil, nil)`.
- For zlib-stream sessions, the proxy always forwards the *original
  compressed* frame to the bot regardless of what the Tamperer returns —
  the bot negotiated compression and expects compressed bytes. The Tamperer
  hook is therefore observation-only on compressed gateway streams.
- Uncompressed gateway sessions (`encoding=json` without `compress=`) are
  fully bidirectional: the Tamperer's return value is what gets forwarded.

## 8. Verbose logging tamperer (default)

`tamper.Logging` is the default tamperer shipped in `main`. Every
request/response/frame is logged via the configured `*slog.Logger`:

- HTTP requests and responses at Info: method, URL, status, body length,
  and (if `LogBodies`) a body preview.
- WebSocket incoming and outgoing frames at Info: payload length and (if
  `LogBodies`) a payload preview. The chatty per-frame *operational*
  metadata (one Debug line per raw frame including heartbeats) is emitted
  separately by [handlers/ws.go](../proxy/internals/handlers/ws.go).

Implementation: [proxy/internals/tamper/logging.go](../proxy/internals/tamper/logging.go).

- Bodies are previewed up to 8192 bytes; truncation happens on a UTF-8 rune
  boundary so multi-byte characters are never split.
- Binary content types (`image/*`, `video/*`, `audio/*`,
  `application/octet-stream`, `application/zip`, `font/*`) yield no body
  preview (`DecodeBody` returns nil for them).
- Request bodies are read into memory and the body is rewound for the
  downstream writer; the round-trip is byte-identical for the bot.
- Construct via `tamper.Logging{Logger: logger, LogBodies: cfg.LogBodies}`
  in production; tests can inject a buffer-backed `slog.Handler` to assert
  on captured output. The zero value (`Logging{}`) falls back to
  `slog.Default()` with `LogBodies` off.

## 9. HTTP body codec helpers

`internals/httpio` provides `DecodeBody(resp) -> ([]byte, error)` and
`Encode(resp) error`.

- `DecodeBody`: reads `resp.Body`, rewinds it (so downstream readers see
  the same original bytes), decompresses per `Content-Encoding`, and
  returns the decompressed payload. Does NOT mutate `resp.Header` —
  `Content-Encoding` stays put so the body forwarded to the bot retains
  its on-the-wire encoding. Returns `(nil, nil)` for nil / empty / binary
  bodies; callers treat that as "no readable body."
- `Encode`: normalises the framing of `resp` so `http.Response.Write`
  produces clean `Content-Length`-based output. Reads the body, drops
  `Transfer-Encoding`, sets `Content-Length`. **Does not re-compress** —
  the body bytes are forwarded verbatim in whatever encoding upstream
  sent.

This pairing means the bot always receives the exact bytes Discord sent,
with the matching `Content-Encoding`. The Tamperer is the only thing in
the loop that gets a decoded view, and it gets it via a separate `[]byte`
argument rather than via mutation.

## 10. Healthcheck endpoint with readiness gating

A tiny HTTP server on `127.0.0.1:$PROXY_HEALTH_PORT` (default `8081`)
answers `GET /healthz`.

- Implementation: [proxy/internals/healthz/healthz.go](../proxy/internals/healthz/healthz.go).
- Returns `200 ok` once `MITMProxy.Ready()` reports true (i.e. `Serve` has
  entered its accept loop), `503 not ready` otherwise. The healthcheck
  flips green only when the proxy is actually serving.
- Loopback-only — never exposed on the Docker network.
- Wired into the container's `HEALTHCHECK` directive in the Dockerfile and
  in `docker-compose.yml`. Bot services use `depends_on: hallmaster-proxy`
  with the implicit "service healthy" condition, so they only start after
  the proxy is reachable.

## 11. Env-driven configuration

[proxy/internals/config/config.go](../proxy/internals/config/config.go)
loads everything from environment variables with sensible defaults. The
`Config` struct is intentionally flat so tests can construct it directly
without going through `Load()`.

| Variable | Default | Purpose |
| --- | --- | --- |
| `PROXY_HOSTNAME` | system hostname | DNS name clients reach the proxy on; used for relay detection. |
| `PROXY_PORT` | `8080` | TCP port the proxy listens on. |
| `PROXY_HEALTH_PORT` | `8081` | Loopback port for `/healthz`. |
| `PROXY_SSL_CA_CERT_PATH` | required | Path to the Root CA public cert. |
| `PROXY_SSL_CA_KEY_PATH` | required | Path to the Root CA private key. |
| `PROXY_DNS_SERVER` | `8.8.8.8:53` | External resolver used by the DNS bypass. |
| `PROXY_LOG_BODIES` | `true` | When `false`, the default `Logging` tamperer omits full HTTP request / response bodies and WS payloads from log lines (a `body_len` / `len` attribute is still emitted). Useful in shared environments. |

## 12. Test seams — full handler harness available

The proxy exposes a coherent set of seams for integration tests:

- **`Tamperer`** — observe / rewrite every HTTP request, HTTP response
  (with `decodedBody []byte` arg), and WebSocket frame in both
  directions.
- **`Resolver`** — stub DNS lookups.
- **`Handshaker`** — perform TLS termination against a caller-supplied
  `net.Conn`.
- **`HandlerDeps.UpstreamTLSConfig`** — non-nil clones into the upstream
  dial config so tests can trust an `httptest.NewTLSServer` cert.
- **`HandlerDeps.DialUpstream`** — non-nil overrides the default
  `tls.DialWithDialer`; tests can route upstream dials through
  `net.Pipe` or a fixture without binding sockets.
- **`MITMProxy.Serve(ctx, ln, deps, handler)`** — accept loop on a
  caller-supplied `net.Listener` with `context.Context` for graceful
  shutdown. Tests construct a `pipeListener` and drive accepted
  connections via `net.Pipe`.
- **`MITMProxy.Ready()`** — reports whether `Serve` is in its accept
  loop; lets tests synchronise without sleeps.

`proxy/internals/internaltest` provides shared helpers (`WriteTestCA`,
`IssueLeaf`) so test packages don't duplicate the OpenSSL dance.
`handlers/https_test.go` and `mitm_test.go` show the full pattern in
~100 lines per test.

## 13. Structured JSON logging

All log output goes through `log/slog`. `main.go` installs a
`slog.NewJSONHandler(os.Stdout, ...)` at Info level as the default.

- Handler-side logs (per-request, per-frame, lifecycle) travel through
  `deps.Logger`; package-level code (`mitm`, `healthz`, `main`) uses
  `slog.Default()` directly. Tests can swap a buffer-backed handler in
  via `HandlerDeps.Logger` plus `slog.SetDefault` to capture everything.
- Level conventions: lifecycle ("listening", "ws upgrade") at Info;
  recoverable errors (tamper failure, decode failure) at Warn;
  upstream/network failures at Error; per-frame operational chatter at
  Debug.

## 14. Resource-conscious implementation

The proxy is written in Go to keep the resource ceiling low (sub-100 MB
container memory in the typical case) and to take advantage of cheap
goroutines for the concurrent forwarding model. Specific decisions that
trade complexity for resource efficiency:

- `sync.Map` + `singleflight.Group` for the cert cache — no global lock,
  no thundering herd on first connection.
- `io.Pipe` for the zlib-stream decoder rather than buffering every frame
  in memory.
- `bufio.Reader` peek-then-replay to dual-mode TLS / CONNECT on a single
  port without a second listener.
- One upstream TLS connection kept alive per `(client session, host)`
  pair, redialled only on host change — no per-request connection churn.
- Static binary built with `-trimpath -ldflags='-s -w'` on Alpine — final
  image weighs around 6 MB (binary alone) / 15–20 MB (image).
