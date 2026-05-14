# Hallmaster Proxy

## Introduction

`Hallmaster Proxy` is a service that intercepts HTTP/S requests and WebSocket
traffic so they can be inspected, logged, tampered with for tests, and
analysed for metrics.

It is meant to be used inside a Docker network alongside `hallmaster-runner`
containers — the runner image (built and published from a separate repository)
installs `iptables` rules that redirect port 80 / 443 traffic to this proxy.

The proxy targets Discord's API in particular: it terminates TLS using a
self-signed CA, hands the bot a freshly-minted certificate signed by that CA,
forwards the request upstream, and observes both directions.

## Repository layout

```
/                          Root CA generation, top-level compose example
├── certificate-manager.sh OpenSSL wrapper to produce the Root CA
├── docker-compose.example.yml
└── proxy/                 Go proxy source + Dockerfile
    └── internals/
        ├── certs/         CA loading + on-the-fly leaf signing (singleflight cache)
        ├── config/        Env-var-driven configuration
        ├── discord/       Discord-specific bits (zlib-stream decoder)
        ├── dnsbypass/     External DNS resolver (Resolver interface, mockable)
        ├── handlers/      HTTPS + WebSocket forwarding handlers
        ├── healthz/       /healthz endpoint for container healthchecks
        ├── httpio/        Generic HTTP request/response encode/decode helpers
        ├── proxylog/      Request/response logging
        └── tamper/        Tamperer interface — the test/feature seam
```

## Usage

### 1. Generate the Root Certificate Authority

The Root CA's public certificate is mounted into every bot container so it
can validate the leaf certs the proxy presents. The private key is mounted
into the proxy itself.

```bash
./certificate-manager.sh
```

The script creates `./certs/hallmaster-rootca.pem` (private key) and
`./certs/hallmaster-rootca.crt` (public cert). Re-running refuses to
overwrite unless you pass `--force`.

### 2. Configure the compose file

The repo ships a `docker-compose.example.yml`; copy it to
`docker-compose.yml` (which is git-ignored), uncomment the example bot
services, and point their `build:` / `image:` at your own bot images.

```bash
cp docker-compose.example.yml docker-compose.yml
```

### 3. Run the proxy

```bash
docker compose up --build hallmaster-proxy -d
```

The healthcheck (`GET /healthz` on port `PROXY_HEALTH_PORT`) flips the
container to `healthy` within ~30 seconds. After that you can `compose up`
the bot services.

## Environment variables

| Name | Default | Purpose |
| --- | --- | --- |
| `PROXY_HOSTNAME` | system hostname | DNS name the bot uses to reach the proxy. Used to detect relay requests. |
| `PROXY_PORT` | `8080` | TCP port the proxy listens on for client traffic. |
| `PROXY_HEALTH_PORT` | `8081` | Port the `/healthz` endpoint binds (loopback only). |
| `PROXY_SSL_CA_CERT_PATH` | — (required) | Path to the Root CA public certificate inside the container. |
| `PROXY_SSL_CA_KEY_PATH` | — (required) | Path to the Root CA private key inside the container. |
| `PROXY_DNS_SERVER` | `8.8.8.8:53` | External DNS resolver used to bypass the in-container DNS that resolves Discord hostnames to the proxy. |

## The Hallmaster Runner

The Hallmaster Runner is a base image (maintained in a separate repository)
for hosting Discord bots:

1. It copies the Root CA public cert into `/usr/local/share/ca-certificates/`
   so the system trust store accepts the leaf certs the proxy issues.
2. It installs `iptables` rules at runtime that redirect outgoing traffic on
   ports 80 / 443 to the `hallmaster-proxy` container.

Bot containers should inherit from the Hallmaster Runner image, but the
runner is not part of this repository.

## Self-signed certificate errors

Although the Root CA is mounted into bot containers, some language runtimes
do not trust non-default CAs out of the box and need an extra hint:

| Runtime | Variable |
| --- | --- |
| Node.js / Bun | `NODE_EXTRA_CA_CERTS` |
| Python (`requests`) | `REQUESTS_CA_BUNDLE` |
| OpenSSL (most things) | `SSL_CERT_FILE` |
| Go | reads the system store, usually OK after `update-ca-certificates` |
| Java | requires `keytool -import` into the JVM truststore |

The example compose snippets show the Node and Python knobs. For new
runtimes, set whichever variable points at the mounted Root CA path.

## Running tests

(placeholder — the integration test suite hooks into the `Tamperer`
interface defined in `proxy/internals/tamper/`. Implement a custom
`Tamperer` in your test process, pass it to `internals.NewMITMProxy` in
place of `tamper.Nop{}`, and exercise the proxy from a real or simulated
bot.)
