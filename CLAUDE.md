# CLAUDE.md

## What this project is

The data plane of [Hallmaster](https://github.com/hallmasterorg/hallmaster),
a hosting platform for Discord bots. It's a Go man-in-the-middle proxy that
runs on the same Docker network as the bot shard containers and sees **all**
of their Discord traffic (REST over HTTPS, and the gateway over WSS)
without the bot authors having to instrument anything.

```
bot shard ──TLS──▶ proxy ──TLS──▶ real Discord
          ◀──────  (decrypt, decompress, read)  ◀──────
```

Both directions go through the same steps:

1. **Catch.** Docker DNS aliases (`discord.com`, `discord.gg`,
   `gateway.discord.gg`) resolve to the proxy. The Hallmaster Runner base
   image (a separate repo) also adds `iptables` redirects and installs our
   Root CA in the bot's trust store.
2. **Decrypt.** The proxy terminates TLS with a leaf cert for the requested
   host, signed on the fly by a self-managed Root CA
   (`certificate-manager.sh`).
3. **Decompress + read.** HTTP bodies (gzip/deflate/brotli) and gateway
   frames (`zlib-stream`) are decoded and handed to the `Tamperer`.
   Today it only logs; the plan is to ship to the analytics backend.
4. **Forward.** The proxy can't resolve `discord.com` through Docker DNS,
   because that name points back to the proxy itself. It resolves real
   Discord IPs through an external DNS server (`PROXY_DNS_SERVER`), dials
   the IP, and keeps `ServerName: <original host>` so SNI and certificate
   validation against Discord still work.
5. **Return path.** Responses and gateway frames from Discord go through
   the same decrypt → decompress → read steps before reaching the shard.

### Where it's going

The proxy is the foundation for features Discord's API doesn't offer:

- **Zero-downtime rescaling.** Resharding without losing or duplicating
  gateway events. It needs a cache/state layer in the proxy that
  understands sessions, sequence numbers and resume.
- **Telemetry forwarding.** Logs, errors, rate limits and traffic stats go
  to the Hallmaster backend (a separate repo). It stores them in a DB and
  shows them on a dashboard.

That direction implies some rules for code written now:

- **The bot sees exactly what Discord sent**, unless we change it on
  purpose. Pass-through fidelity comes first; observing traffic is a side
  effect.
- **Observation must never slow or break forwarding.** A slow or failing
  analytics sink must not stall a gateway pump or drop a frame. Anything
  that ships data off-box has to be async and bounded, and should shed its
  own load rather than the bot's traffic.
- **The gateway path is the critical path.** Event loss or duplication is
  what the rescaling feature exists to prevent, so the proxy must not add
  either.

## Where to read what

README points, `docs/` explains. Don't duplicate content between them.

- Setup, env vars, dev vs prod: [docs/setup.md](docs/setup.md)
- Topology + request lifecycle: [docs/architecture.md](docs/architecture.md)
- Feature inventory: [docs/features.md](docs/features.md)
- Foot-guns, runtime trust-store table, open bugs: [docs/known-issues.md](docs/known-issues.md)

## Code map (`proxy/`)

| Path | Role |
|---|---|
| `main.go` | Wires Config → Certs → Resolver → Tamperer → MITMProxy (`//go:build !stress`) |
| `main_stress.go` | `-tags stress` hook: upstream → robojs-mock, optional `Nop` Tamperer, loopback pprof |
| `internals/mitm.go` | Listen/Serve/Handshake, `HandlerDeps`, `Handshaker` |
| `internals/handlers/https.go` | Per-request HTTPS forwarding, `isDiscordHost` |
| `internals/handlers/ws.go` | `InspectWS`: bidirectional gateway pump |
| `internals/certs/` | Root CA load, leaf signing (sync.Map + singleflight + renewal) |
| `internals/dnsbypass/` | `Resolver` interface + external DNS resolver |
| `internals/discord/wscompress.go` | `zlib-stream` decoder |
| `internals/httpio/` | Pure `DecodeBody`; `Encode` only normalises framing |
| `internals/tamper/` | `Tamperer` interface, `Nop`, `Logging` (default) |
| `internals/healthz/` | Loopback `/healthz` |
| `internals/internaltest/` | Test/bench helpers. Never import from production code |

Test support outside `proxy/`:

- `robojs-mock/`: a Robo.js app running `@robojs/mock`, a fake Discord
  gateway + REST API with a control API (`/mock/api/control/...`) for
  injecting events.
- `testing-bot/`: a discord.js sharded bot that talks to the real Discord.
  It reads `DISCORD_BOT_TOKEN` from the shell or the git-ignored root `.env`,
  and is the only service that needs it.
- `tests/stress/driver/` + `scripts/run-stress.sh` + `docker-compose.stress.yml`:
  the load harness. It never touches the real Discord, which avoids rate
  limits and bans:
  - The `-tags stress` proxy resolves every Discord host to
    `robojs-mock/tls-front.mjs`, a TLS front on :3443, because
    `@robojs/server` is HTTP-only.
  - The driver acts as a bot shard, with one gateway connection through
    the proxy and one straight to the TLS front on the same mock session.
    It injects events through the mock's control API. The per-event
    difference between the two paths is the proxy's overhead.
  - Output goes to `tests/stress/results/` (git-ignored): `summary.md`,
    `latencies.csv` and `stats.csv`, plus `proxy.log` with the
    `tamper` records.
  - If `summary.md` shows 0 proxy `tamper` records, nothing went
    through the proxy and the numbers are meaningless.

## Things that aren't obvious from the code

1. **One accept loop, two arrival modes.** `Serve` peeks at the first byte.
   `0x16` means direct TLS (traffic redirected by iptables). Anything else
   is read as an HTTP `CONNECT`. Both end in `handlers.HttpsHandler` on a
   `*tls.Conn`.
2. **Compressed gateway streams are observation-only.** With
   `compress=zlib-stream`, the bot always gets the original compressed
   frame. The Tamperer sees the decoded view, but its return value is
   ignored. Uncompressed streams are fully bidirectional.
3. **`Encode` doesn't re-compress.** Bodies keep Discord's
   `Content-Encoding`. A Tamperer that rewrites a body is responsible for
   keeping that header consistent.
4. **Leaf certs** last 7 days and are regenerated within 24 h of expiry.
   Issuing a new leaf is expensive (~90 ms, see `BenchmarkLeafIssue_CacheMiss`),
   so the cache matters.
5. **The CA key must be `0600`** or the proxy refuses to start.

## Contracts and test seams

- `Tamperer`'s four methods (`Request`, `Response`, `WSIncoming`,
  `WSOutgoing`) are a public contract. Adding methods is fine; changing
  signatures needs coordination.
- `Resolver`, `Handshaker`, `HandlerDeps.UpstreamTLSConfig`,
  `HandlerDeps.DialUpstream` and `MITMProxy.Serve` are the test seams.
  Don't inline `net.LookupHost`, `tls.DialWithDialer` or `net.Listen` in
  code paths that would bypass them.

## Commands

```bash
./certificate-manager.sh                       # Root CA (idempotent; --force regenerates)
docker compose up --build -d                   # proxy + bots (bots wait for healthy proxy)
docker compose logs -f hallmaster-proxy

# from proxy/
go vet ./... && go vet -tags stress ./...
go test ./... -race -count=1
go test -run '^$' -bench . -benchmem ./...     # in-process microbenchmarks
../scripts/lint.sh                             # golangci-lint v2, all tags + stress driver; quiet on success

# once per clone (from repo root): pre-commit = secret guard + scripts/lint.sh
git config core.hooksPath .githooks

# end-to-end load harness (from repo root)
./scripts/run-stress.sh                        # all scenarios
./scripts/run-stress.sh burst_10k              # one scenario
```

## Guardrails

- The proxy must not be reachable from outside the Docker network. Don't
  publish its ports.
- `certs/*.pem` and `.env` hold secrets. The pre-commit hook blocks
  `.env*` (except `*.example`), `*.pem` and `*.key`, but only once
  `core.hooksPath` is set.
- Don't claim a new bot runtime works until you've checked how it handles
  its trust store (see the table in `docs/known-issues.md`).
- Don't add `*.md` files outside `docs/` unless asked.
- Don't skip pre-commit hooks (`--no-verify`) without approval.
