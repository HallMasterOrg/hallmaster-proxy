# Hallmaster Proxy

A transparent MITM proxy for Discord bot traffic — written in Go, deployed
as a Docker container, designed to sit invisibly between a bot and Discord's
infrastructure.

## Why

[Hallmaster](https://github.com/hallmasterorg/hallmaster) is an open-source
hosting platform for Discord bots: monitoring, logging, shard and cluster
scaling, smarter caching, and an overall better developer / CI-CD experience
than rolling your own VPS.

To deliver that without making bot authors instrument their code, Hallmaster
needs to see what their bots actually send and receive — gateway events,
interaction payloads, REST calls, the lot. **This proxy is how it sees them.**

## How

- **Docker networking + DNS rewiring.** Bot containers join a user-defined
  network on which the proxy is registered with DNS aliases (`discord.com`,
  `discord.gg`, `gateway.discord.gg`). The bot's view of "Discord" is
  actually the proxy.
- **Transparent TLS termination.** The proxy presents a freshly-minted leaf
  certificate signed by a Hallmaster Root CA the bot trusts, terminates the
  TLS session, observes the plaintext, then re-encrypts towards Discord
  using an external DNS bypass to reach the real upstream IPs.
- **A pluggable `Tamperer` seam.** Every HTTP request, HTTP response, and
  WebSocket frame passes through a Go interface. The default implementation
  logs; the integration test suite (forthcoming) injects faults and
  assertions through the same hook.
- **Discord-aware.** Understands gateway `compress=zlib-stream` so observers
  see readable JSON instead of raw deflate bytes.

## Documentation

Detailed docs live in [docs/](docs/):

- **[docs/setup.md](docs/setup.md)** — prerequisites, generating the Root
  CA, configuring environment and compose, dev vs production workflow,
  verifying it works.
- **[docs/architecture.md](docs/architecture.md)** — network topology,
  request lifecycle from accept to TLS termination to upstream dial,
  WebSocket pump, healthchecks.
- **[docs/features.md](docs/features.md)** — full inventory of what the
  proxy does today: interception, leaf signing, DNS bypass, tampering,
  zlib-stream, logging, codec helpers, env-driven config, the test seam.
- **[docs/known-issues.md](docs/known-issues.md)** — caveats and foot-guns,
  including the per-runtime trust-store table (Node, Bun, Deno, Python,
  Java, …) — runtimes that ship their own CA bundle ignore the system
  trust store and need a runtime-specific environment variable to accept
  the proxy's certs.

## Quick start

```bash
./certificate-manager.sh                          # generate the Root CA
cp docker-compose.example.yml docker-compose.yml  # then edit for your bots
docker compose up --build hallmaster-proxy -d
docker compose up -d                              # bring up the bots
```

The proxy is healthy when `docker compose ps` shows it as `healthy` (~30s).
For the full walkthrough, see [docs/setup.md](docs/setup.md).

## License

Part of the [Hallmaster](https://github.com/hallmasterorg/hallmaster) open
source project.
