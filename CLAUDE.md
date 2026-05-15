# CLAUDE.md

Operator notes for working on this repo. Read [docs/](docs/) for the full
story; this file is the "things I wish I had known before touching the code"
shortlist.

## What this is

A Go MITM proxy for Discord bot traffic, deployed as a Docker container.
Bots join the same Docker network, DNS aliases (`discord.com`, `discord.gg`,
`gateway.discord.gg`) point at the proxy, the proxy terminates TLS with
leaf certs signed by a self-managed Root CA, then forwards upstream.

It is the data plane for [Hallmaster](https://github.com/hallmasterorg/hallmaster)
— a hosting platform for Discord bots that wants to log/monitor/shard
without making bot authors instrument anything.

## Where to read what

- Project pitch + quick start: [README.md](README.md)
- Setup, env vars, dev vs prod workflow: [docs/setup.md](docs/setup.md)
- Network topology + request lifecycle: [docs/architecture.md](docs/architecture.md)
- Feature inventory: [docs/features.md](docs/features.md)
- Caveats / foot-guns / per-runtime trust-store table: [docs/known-issues.md](docs/known-issues.md)

## Repo layout

```
/
├── certificate-manager.sh         POSIX-sh wrapper around OpenSSL to mint the Root CA
├── docker-compose.example.yml     Template; copy to docker-compose.yml (git-ignored)
├── docker-compose.yml             Local instance (git-ignored)
├── docker-compose.test.yml        Test instance (uses robojs-mock + testing-bot)
├── certs/                         Generated CA (git-ignored; *.pem private, *.crt public)
├── docs/                          Long-form docs (see above)
├── robojs-mock/                   IGNORE for now — moves to its own branch
├── testing-bot/                   IGNORE for now — moves to its own branch
└── proxy/                         The Go proxy
    ├── main.go                    Wires Config -> Certs -> Resolver -> Tamperer -> MITMProxy
    ├── Dockerfile                 Two-stage build, alpine base, ~15-20 MB final image
    ├── .golangci.yml              Active linters: errcheck/govet/ineffassign/staticcheck/unused/gofmt/goimports
    └── internals/
        ├── mitm.go                Listen / Serve / Handshake; HandlerDeps + Handshaker
        ├── certs/certs.go         Root CA load + on-the-fly leaf signing (sync.Map + singleflight + renewal)
        ├── config/config.go       Flat Config struct, env-driven, Load() builds it
        ├── discord/wscompress.go  ZlibStreamDecoder for gateway compress=zlib-stream
        ├── dnsbypass/resolver.go  Resolver interface (mockable) + ExternalResolver
        ├── handlers/https.go      Per-request HTTPS forwarding + isDiscordHost + Tamperer.Request/Response
        ├── handlers/ws.go         InspectWS: bidirectional WS pump + Tamperer.WSIncoming/WSOutgoing
        ├── healthz/healthz.go     Loopback /healthz, 200 if ready() else 503
        ├── httpio/                gzip/deflate/brotli DecodeBody + framing-normalising Encode
        ├── internaltest/          Test-only helpers (WriteTestCA, IssueLeaf) — never imported from production
        └── tamper/                Tamperer interface + Nop (silent) + Logging (verbose default)
```

## Mental model (the part that isn't obvious from the code)

1. **One accept loop, two arrival modes.** `MITMProxy.Serve` peeks the first
   byte: `0x16` -> direct TLS (iptables redirect landed real bot traffic);
   anything else -> read an HTTP request, expect `CONNECT host:port`,
   acknowledge, then TLS-terminate. Both end up calling `handlers.HttpsHandler`
   on a `*tls.Conn`.

2. **The bot's DNS is rewired, so the proxy can't reuse hostnames upstream.**
   `dnsbypass.ExternalResolver` dials `$PROXY_DNS_SERVER` (default `8.8.8.8:53`)
   to resolve real Discord IPs. The proxy then dials the IP but keeps
   `ServerName: originalHost` in the upstream TLS config so SNI + cert
   validation against Discord both work.

3. **`MITMProxy` is a pure listener + `Handshaker`.** Collaborators
   (tamperer, resolver, hostnames, logger, optional `UpstreamTLSConfig` and
   `DialUpstream` hooks) travel as `internals.HandlerDeps`, built in `main`
   and passed through `Listen` / `Serve` to every handler invocation.
   Handlers depend on the `Handshaker` interface, never on the concrete
   `*MITMProxy`. `Listen` opens `net.Listen` and delegates to
   `Serve(ctx, ln, deps, handler)`; tests call `Serve` directly with a
   `net.Pipe`-backed listener and their own `context.Context` for graceful
   shutdown.

4. **The `Tamperer` interface is THE test seam.** Tests swap
   `tamper.Logging`/`tamper.Nop` for a recording implementation by building
   their own `HandlerDeps`. The interface has four methods:
   - `Request(*http.Request) (*http.Request, error)`
   - `Response(*http.Request, *http.Response, decodedBody []byte) (*http.Response, error)`
   - `WSIncoming([]byte) ([]byte, error)`
   - `WSOutgoing([]byte) ([]byte, error)`

5. **Compressed gateway streams are observation-only.** When
   `compress=zlib-stream` is negotiated, `InspectWS` always forwards the
   **original compressed frame** to the bot regardless of what the Tamperer
   returns. The Tamperer is handed the decoded view for inspection only.
   Uncompressed streams are fully bidirectional — Tamperer's return value
   IS what gets forwarded.

6. **`httpio.DecodeBody` is pure; `httpio.Encode` only normalises framing.**
   `DecodeBody` returns decoded bytes for the tamperer without mutating
   `resp` (Content-Encoding stays put). `Encode` reads the body and resets
   `Content-Length` / drops `Transfer-Encoding` so `Response.Write`
   produces clean framing — it does NOT re-compress. The bot gets the
   exact bytes Discord sent, with the same Content-Encoding. A Tamperer
   that rewrites `resp.Body` is responsible for keeping `Content-Encoding`
   consistent with the new bytes.

7. **`tamper.Logging` is the default.** `main.go` wires it as the proxy's
   Tamperer; HTTP req/resp and WS in/out content all log at Info. The
   chatty per-frame operational metadata in `handlers/ws.go` (one line per
   frame including heartbeats) stays at Debug. Set `PROXY_LOG_BODIES=false`
   to keep payload bytes out of logs while still seeing traffic volume
   (`body_len` / `len` attributes are always emitted).

8. **Cert cache renewal.** Leaf certs are issued with 7-day validity.
   `GetOrCreateCert` checks `NotAfter` and regenerates when within 24h
   of expiry. The CA private key file is rejected at startup if its
   permissions allow group/other read (`certificate-manager.sh` enforces
   `chmod 0600`).

## Open follow-ups

- **`ZlibStreamDecoder` race fix.** The `drain()` / `writeDone`
  synchronisation is non-deterministic. See
  [docs/known-issues.md](docs/known-issues.md) for the failure mode.
- **WebSocket end-to-end test.** Only the HTTP path is exercised
  end-to-end today; the WS upgrade path with `wsutil` frame injection is
  a small extension on top of the existing harness.
- **No SIGTERM handling in main.** `Serve` accepts a `context.Context`
  but `main.Listen` passes `context.Background`. Wire
  `signal.NotifyContext` in `main.go` for production drain.
- **HTTP/2 upstream.** Currently locked to `http/1.1` in the upstream
  `NextProtos`. See [docs/known-issues.md](docs/known-issues.md).

## Commands

```bash
# Generate the Root CA (one-time; idempotent, --force to regenerate)
./certificate-manager.sh

# Build + run the whole stack
docker compose up --build hallmaster-proxy -d
docker compose up -d                    # bots come up after proxy is healthy

# Logs
docker compose logs -f hallmaster-proxy

# Local Go workflow (from proxy/)
go build -o hallmaster-proxy .
go vet ./...
go fmt ./...
go test ./... -race -count=1
golangci-lint run                       # uses .golangci.yml

# Test compose (uses robojs-mock + testing-bot — currently moving to own branch)
docker compose -f docker-compose.test.yml up --build
```

## Things to know before changing code

- **`Tamperer`'s four-method interface is a public contract** for the test
  branch. Adding methods is fine; changing signatures requires
  coordination.
- **`Resolver`, `Handshaker`, `UpstreamTLSConfig`, `DialUpstream`,
  `MITMProxy.Serve`** are also test seams. Don't inline `net.LookupHost`,
  `tls.DialWithDialer`, or `net.Listen` calls in places that bypass
  these hooks.
- **The proxy is not designed to be reachable from outside the Docker
  network.** Don't publish ports in the compose examples.
- **`certs/*.pem` is a private key.** `.gitignore` covers it; double-check
  before any `git add -A`. The proxy refuses to start if the key file is
  more permissive than `0600`.
- **The Hallmaster Runner base image (separate repo)** is what installs
  the Root CA into the OS trust store and configures `iptables`. Bots that
  don't inherit from it will mostly work via DNS aliases but will bypass
  the proxy for any raw-IP dial.
- **Runtime trust stores are a per-language minefield.** Node / Bun /
  Python / Deno / Java each need their own env var (or worse). The table
  is in [docs/known-issues.md](docs/known-issues.md). Don't promise a
  new runtime works until you've actually verified its trust-store
  behaviour.

## Things NOT to do here

- Don't add the same content to both [README.md](README.md) and `docs/*` —
  README points, docs explain.
- Don't introduce code for `robojs-mock/` or `testing-bot/`; they are
  moving to their own branch.
- Don't add `*.md` documentation files outside `docs/` unless the user
  explicitly asks for them.
- Don't skip pre-commit hooks (`--no-verify`) without explicit user
  approval.
