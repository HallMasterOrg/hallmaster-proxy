# Known issues & caveats

This page collects gotchas that are not bugs per se but will bite a fresh
contributor or operator. They are sorted from "you will definitely hit this"
down to "you might one day hit this".

## 1. Some runtimes ignore the system trust store

The Hallmaster Root CA lives in the bot's system trust store (e.g.
`/usr/local/share/ca-certificates/hallmaster-root-ca.crt` on Debian/Ubuntu
after `update-ca-certificates`). Tools that read the OS trust store directly
— `curl`, `wget`, `openssl s_client`, Go's `crypto/tls` — accept the leaf
certs the proxy presents with no fuss.

A number of language runtimes, however, ship and use **their own bundled
trust store** and ignore the OS one entirely. They will reject the proxy's
leaf certs with `UNABLE_TO_VERIFY_LEAF_SIGNATURE` /
`SELF_SIGNED_CERT_IN_CHAIN` / `CERTIFICATE_VERIFY_FAILED` errors even when
`curl https://discord.com` from the very same container works fine.

Each runtime needs a different escape hatch:

| Runtime | Variable(s) | Notes |
| --- | --- | --- |
| Node.js (≥ 7.3) | `NODE_EXTRA_CA_CERTS=/path/to/hallmaster-root-ca.crt` | Appends to the bundled list. Does NOT replace it; safe. |
| Node.js (proxy-aware libs) | `NODE_USE_ENV_PROXY=1` plus `HTTPS_PROXY` / `HTTP_PROXY` | Needed when the runtime should honour env-var proxy settings (Node 24+). |
| Bun | `NODE_EXTRA_CA_CERTS` | Bun mirrors Node's behaviour. |
| Deno | `--cert <path>` flag or `DENO_CERT=<path>` | No append semantics — provide the file. |
| Python `requests` | `REQUESTS_CA_BUNDLE=/path/to/hallmaster-root-ca.crt` | Specific to `requests`. |
| Python `httpx`, `urllib`, OpenSSL-based things | `SSL_CERT_FILE=/path/to/hallmaster-root-ca.crt` | Honoured by anything that uses OpenSSL's defaults. |
| Java | `keytool -importcert -file hallmaster-root-ca.crt -keystore $JAVA_HOME/lib/security/cacerts -alias hallmaster` | JVMs have their own truststore (`cacerts`); env vars don't help. |
| .NET | install via system store on Linux, or use `X509Store` to import at runtime | Behaviour varies wildly per platform. |
| Ruby `net/http`, `Faraday` | `SSL_CERT_FILE` | OpenSSL-based, same as Python. |
| Go | system store, usually OK after `update-ca-certificates` | Reads OS trust store on Linux. |

The example `docker-compose.yml` wires `NODE_EXTRA_CA_CERTS` for the JS bot
and `SSL_CERT_FILE` + `REQUESTS_CA_BUNDLE` for the Python bot. For any new
language you bring in, expect to find the right knob, document it here, and
mount the Root CA into the container at a stable path.

**The list above is not exhaustive.** Runtimes ship new versions; new
languages will land in the test matrix. When you add a runtime, treat
"figure out which env var is needed" as part of the integration work, not a
bug.

## 2. Re-running `certificate-manager.sh --force` does not clear leaf caches

`certificate-manager.sh` refuses to overwrite an existing CA unless you pass
`--force`. The leaf cert cache inside the proxy auto-renews stale certs
(see [features.md §2](features.md)), but it has no way to learn that the
**Root CA itself** has been replaced — the existing cached leafs were
signed by the old CA and will fail verification against the new one.

After a force-regeneration you need to:

1. Restart the proxy container so its in-memory cache is cleared.
2. Redistribute the new public cert to every bot container (the old one
   will no longer chain to the now-revoked CA).

## 3. `iptables` rules are out-of-tree

The runner image (separate repository) is responsible for installing
`iptables` rules that catch IP-based connections. If you build a bot image
that does **not** inherit from the Hallmaster Runner, any HTTPS dial to a
raw IP will bypass the proxy entirely. Always inherit from the runner, or
add equivalent `iptables` rules yourself.

## 4. The proxy speaks HTTP/1.1 only

The upstream TLS dial sets `NextProtos: []string{"http/1.1"}`
([handlers/https.go](../proxy/internals/handlers/https.go)). Discord's
REST API is HTTP/1.1 (or H2 with fallback), and the gateway is WebSocket,
so this works today. The day Discord forces H2 for some endpoint, the proxy
will need an HTTP/2 transport.

## 5. Graceful shutdown is wired but main doesn't trap signals

[`MITMProxy.Serve`](../proxy/internals/mitm.go) accepts a
`context.Context`: cancelling it closes the listener and the accept loop
returns nil. In-flight handler goroutines are NOT cancelled — they run
to completion. Tests use this path today.

`main.Listen`, however, calls `Serve` with `context.Background()` and
never traps a signal — so `docker compose stop` still SIGTERMs the
container with no drain. Wiring `signal.NotifyContext(SIGTERM, SIGINT)`
in `main.go` is the small remaining change to make this production-grade.

## 6. `ZlibStreamDecoder` has a goroutine-scheduling race

[proxy/internals/discord/wscompress.go](../proxy/internals/discord/wscompress.go)'s
`drain()` uses a non-blocking `select` on `writeDone` between deflate
reads. The write goroutine fires `writeDone` shortly after `pipe.Write`
returns; whether the main goroutine sees that fire before deciding to
issue another `Read` depends on the Go scheduler. Under tight
single-threaded scheduling (notably `go test` with single-frame inputs)
the race is consistently lost — drain issues one more `Read` on the
deflate stream, which blocks indefinitely because the inflater needs the
header of the next block.

In production the bug is rarely observed because (a) `GOMAXPROCS` is
typically > 1, so the write goroutine runs in parallel, and (b) Discord
sends a continuous stream of frames, so even if drain "hangs", the next
`Decode` call writes more bytes to the pipe and unblocks the read. A
proper fix replaces the pipe + writeDone synchronisation with something
deterministic (a bounded buffer with explicit "end-of-frame" semantics).
Tracked for a dedicated branch.
