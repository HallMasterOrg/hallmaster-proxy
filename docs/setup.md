# Setup

This document walks through everything needed to run the Hallmaster Proxy
together with one or more bot containers. It covers both a development
workflow (build from source, iterate locally) and a production-like
deployment (use the built container image, pre-baked bot images).

## 1. Prerequisites

| Tool | Version | Used for |
| --- | --- | --- |
| Docker Engine | 24+ | Building and running every container. |
| Docker Compose | v2 (`docker compose ...`) | Orchestrating the proxy + bot services. |
| Go | 1.23.2 | Only if you want to build the proxy outside Docker (development, profiling, tests). The container build pins its own Go toolchain in [proxy/Dockerfile](../proxy/Dockerfile). |
| OpenSSL | any modern version | Generating the Root CA via [certificate-manager.sh](../certificate-manager.sh). Ships with macOS and most Linux distros. |
| A POSIX shell (`/bin/sh`) | — | Running [certificate-manager.sh](../certificate-manager.sh). |
| `git` | any | Cloning the repo. |

The proxy itself depends on three Go modules (pinned in
[proxy/go.mod](../proxy/go.mod)):

- `github.com/andybalholm/brotli` — Brotli encode/decode for HTTP bodies.
- `github.com/gobwas/ws` — WebSocket frame parsing.
- `golang.org/x/sync` — `singleflight.Group` for the cert cache.

When building inside Docker these are fetched automatically.

## 2. Clone

```bash
git clone https://github.com/hallmasterorg/hallmaster
cd hallmaster
```

(If your working repo is the proxy alone, replace the URL accordingly — the
layout described below assumes the root of this repository.)

## 3. Generate the Root Certificate Authority

The proxy signs per-host leaf certs with this CA on the fly, and every bot
container must trust the same CA. Generate it once:

```bash
./certificate-manager.sh
```

This produces:

```
certs/
├── hallmaster-rootca.pem      # private key — mounted into the proxy only
└── hallmaster-rootca.crt      # public certificate — mounted into bot containers
```

The script `chmod 0600`s the private key — the proxy refuses to start if
the key file is readable by anyone besides the owner. If you have an older
key around with looser permissions, `chmod 0600 certs/hallmaster-rootca.pem`
will fix it.

Re-running the script refuses to overwrite an existing CA. Pass `--force` to
regenerate (you will then need to redistribute the new public cert to every
bot container and restart the proxy so its in-memory leaf-cert cache is
cleared — see
[known-issues.md §2](known-issues.md#2-re-running-certificate-managersh---force-does-not-clear-leaf-caches)).

The `certs/` directory is git-ignored.

## 4. Configure environment

Copy the example env file and fill in the bot-side values:

```bash
cp .env.example .env
```

`.env` content (typical):

```
# --- Proxy ---
PROXY_HOSTNAME=hallmaster-proxy
PROXY_PORT=443
PROXY_HEALTH_PORT=8081
PROXY_SSL_CA_CERT_PATH=/app/cert/proxy-ca.crt   # path inside the container
PROXY_SSL_CA_KEY_PATH=/app/cert/proxy-ca.pem    # path inside the container
PROXY_DNS_SERVER=8.8.8.8:53                     # any reachable resolver
# PROXY_LOG_BODIES=true                         # set false to omit body content from logs

# --- Bots ---
DISCORD_BOT_TOKEN=
TOTAL_SHARDS=2
SHARD_ID_LIST=0,1
```

### Where to get `DISCORD_BOT_TOKEN`

1. Go to https://discord.com/developers/applications.
2. Pick the application that backs your bot (or create one).
3. In the left nav, choose **Bot**.
4. Under "Token", click **Reset Token** (or **Copy** if one is already
   shown). The token is shown once — copy it into `.env` immediately.
5. While you are there, in **OAuth2 -> URL Generator** select the `bot`
   scope and the permissions your bot needs, then visit the generated URL
   to invite the bot to a test guild.

The token is bot-account-scoped, not user-scoped. Keep it out of version
control; `.env` is git-ignored.

## 5. Configure the Compose file

The repo ships [docker-compose.example.yml](../docker-compose.example.yml)
with the proxy service plus commented-out bot stubs. Copy it to
`docker-compose.yml` (also git-ignored) and uncomment the bot services you
want to run, pointing `build:` / `image:` at your own bot images:

```bash
cp docker-compose.example.yml docker-compose.yml
```

Key things inside the compose file:

- The proxy joins the user-defined network `hallmaster-proxy-net` with DNS
  aliases `discord.com`, `discord.gg`, `gateway.discord.gg` so bots on the
  same network resolve those hostnames to the proxy.
- The Root CA's public cert and private key are bind-mounted into the proxy
  at the paths in `PROXY_SSL_CA_CERT_PATH` / `PROXY_SSL_CA_KEY_PATH`.
- Each bot service must:
  - `depends_on: [hallmaster-proxy]` to wait for the proxy's healthcheck.
  - Share the `hallmaster-proxy-net` network.
  - Mount the Root CA *public* cert (only) at a stable path inside the
    container — typically `/usr/local/share/ca-certificates/hallmaster-root-ca.crt`.
  - Set the runtime-specific trust-store env var (see below).

### Per-runtime environment variables

| Runtime | Required env vars |
| --- | --- |
| Node.js / Bun | `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/hallmaster-root-ca.crt` and (Node 24+) `NODE_USE_ENV_PROXY=1` |
| Python (`requests`) | `REQUESTS_CA_BUNDLE=/usr/local/share/ca-certificates/hallmaster-root-ca.crt` |
| Python (anything OpenSSL-based) | `SSL_CERT_FILE=/usr/local/share/ca-certificates/hallmaster-root-ca.crt` |
| Deno | `DENO_CERT=/usr/local/share/ca-certificates/hallmaster-root-ca.crt` |
| Java | `keytool -importcert -file hallmaster-root-ca.crt -alias hallmaster -keystore $JAVA_HOME/lib/security/cacerts` inside the bot image |
| Go | usually nothing — Go reads the OS trust store after `update-ca-certificates` |

See [known-issues.md](known-issues.md#1-some-runtimes-ignore-the-system-trust-store)
for the full table and rationale.

## 6. Build & run the proxy

```bash
docker compose up --build hallmaster-proxy -d
```

The healthcheck (`GET /healthz` on the loopback `PROXY_HEALTH_PORT`) marks
the container healthy within ~30s. Check with:

```bash
docker compose ps
docker compose logs -f hallmaster-proxy
```

Then start the bot services:

```bash
docker compose up -d
```

Because every bot service `depends_on: hallmaster-proxy`, the bots will not
start until the proxy reports healthy.

## 7. Development workflow

For iterating on the proxy itself without rebuilding the container each
time, build and run the Go binary directly on the host:

```bash
cd proxy
go build -o hallmaster-proxy .
```

Then run with the env vars set inline (note that the paths point at the
host filesystem this time, not container paths):

```bash
PROXY_HOSTNAME=localhost \
PROXY_PORT=8443 \
PROXY_HEALTH_PORT=8081 \
PROXY_SSL_CA_CERT_PATH=../certs/hallmaster-rootca.crt \
PROXY_SSL_CA_KEY_PATH=../certs/hallmaster-rootca.pem \
PROXY_DNS_SERVER=8.8.8.8:53 \
  ./hallmaster-proxy
```

Useful while developing:

- `go fmt ./...` — format on save (also enforced by `gofmt` and `goimports`
  in [proxy/.golangci.yml](../proxy/.golangci.yml)).
- `go vet ./...` — quick check.
- `go test ./... -race -count=1` — run the full test suite, including the
  end-to-end handler harness (`handlers/https_test.go`) and the `Serve`
  graceful-shutdown tests (`mitm_test.go`).
- `golangci-lint run` — full lint per the config in `.golangci.yml`
  (`errcheck`, `govet`, `ineffassign`, `staticcheck`, `unused`, `gofmt`,
  `goimports`).

When you change the Go code, rebuild the container with:

```bash
docker compose up --build hallmaster-proxy -d
```

The proxy Dockerfile uses BuildKit cache mounts for `/go/pkg/mod` and
`/root/.cache/go-build`, so subsequent rebuilds are fast.

## 8. Production-like deployment

For a deployment that does not build from source on the box:

1. Build and push the proxy image:

   ```bash
   docker build -t your-registry/hallmaster-proxy:latest ./proxy
   docker push your-registry/hallmaster-proxy:latest
   ```

2. On the target host, copy in `certs/`, `docker-compose.yml`, and the
   `.env` file. The Root CA's *private key* must reach the host securely
   — treat it as a secret. The Root CA's *public cert* is just a cert and
   ships happily in the image of each bot.

3. Change the proxy service in `docker-compose.yml` to `image:
   your-registry/hallmaster-proxy:latest` (drop the `build:` block) and
   run `docker compose up -d`.

4. Per-bot images should be built from the Hallmaster Runner base image
   (separate repository) so that the Root CA is installed in the system
   trust store and `iptables` rules are configured at container start.

## 9. Verifying the proxy works

From inside a bot container on the `hallmaster-proxy-net` network:

```bash
curl -v https://discord.com/api/v10/gateway
```

You should see:

- The TLS handshake completing against a leaf cert with `CN=discord.com`
  signed by `O=HallMaster, CN=hallmasterorg.com` (the Root CA).
- The proxy logs (`docker compose logs hallmaster-proxy`) showing a
  structured JSON `tamper http req` line for the request and a
  `tamper http resp` line for the response, each with the decoded body.
- Discord's JSON response (`{"url":"wss://gateway.discord.gg"}` or
  similar).

If `curl` fails certificate verification, double-check that the Root CA was
installed and that `update-ca-certificates` (or the runtime equivalent) was
run during the bot image build.

If `curl` works but the bot's runtime still rejects certs, you are hitting
the issue documented in
[known-issues.md](known-issues.md#1-some-runtimes-ignore-the-system-trust-store):
the runtime ignores the system trust store and needs its own env var
pointed at the Root CA.

## 10. Tearing it down

```bash
docker compose down
```

This stops and removes the containers and the user-defined network. The
`certs/` directory and any generated images are kept.
