# Hallmaster Proxy

## Introduction

`Hallmaster Proxy` is a service that allows for HTTP/S requests and responses
inspections.

It is meant to be used in a Docker container network, where all the
`hallmaster-runner` containers traffic on port `80` for HTTP and `443` for HTTPS
is being redirected.

The goal is to provide a tool that is able to read through all requests to
understand their purpose, especially those at destination of the Discord API.

We are targetting Discord's API so that we can extract different metrics from
the requests to build a solid metric and logs database, but also allowing for
other opportunities, such as caching requests to avoid rate limits, and so more.

## Usage

### Root Certificate Authority (Root CA)

For the proxy to read through HTTPS request without raising a 'self-signed
certificate' error from the HTTP client, the `hallmaster-runner` container,
which MUST be used as a base for the Discord bot container, includes a Root CA
that can be generated using the `certificate-manager.sh` shell script.

It will create a `./certs` folder if it does not already exist, and generate a
new pair of public and private keys, used in TLS context, where the public key
is the Root CA being mounted on the `hallmaster-runner` container at build time,
and where the private key is used by the proxy to sign a certificate created by
the proxy on-the-fly when an HTTPS request is made.

The `./certs` folder will also output a `openssl.conf` file that contains the
configuration used by `openssl` during the key pair generation. Although this
may be useful for debugging, you can safely remove it, or simply ignore it.

Be careful, if you already have files in the `./certs` folder and you still run
the script, all the files will be overwritten without warning.

### Building the Hallmaster Proxy

As previously said in the introduction, the Hallmaster Proxy service is meant to
be used in a containerized environment. In that reguard, there is a `Dockerfile`
at the root of this project that contains the Docker build recipe. To make it
easier, there is also a `docker-compose.yml` file that helps during the build
stage.

To start the build process, type :
```bash
docker compose build hallmaster-proxy
```

Please note that this assumes you have the Docker Engine and Docker Compose
installed and available on your machine.

Then, you can start the proxy service using this command :
```bash
docker compose up hallmaster-proxy -d # detach the process from the shell
```

### The Hallmaster Runner

The Hallmaster Runner creates an environment dedicated for hosting Discord bots
on Hallmaster clusters.

First, it copies the Root CA public key to the dedicated `ca-certificates` path
so that when the container's traffic gets routed to the proxy, the HTTP client
already trusts the returned certificate, thus not raising a 'self-signed
certificate' error.

Then it sets up `iptables` rules to redirect all the outgoing traffic from port
`80` (HTTP) and `443` (HTTPS) to the Hallmaster Proxy. Note that all derived
images such the one presented in the `./bots` folder as an example inherits of
that behavior. The traffic redirection only happens at runtime, meaning that you
can still intall your bot's dependencies just fine at build time.

### Usecase

To make sure everything is setup, we also provide a simple Discord bot example,
written in TypeScript, using Bun. Although you do not need to understand the
code, you still have to get your hands on a Discord bot token that you own to
test it.

At the root of the project, create a new `.env` file and include the following
environment variable declaration :
```bash
DISCORD_BOT_TOKEN="<your discord bot token here>"
```

Once done, you can build the Docker image of the `discord-bot` service using the
following command :
```bash
docker compose build discord-bot
```

This will also build the Docker image of the `hallmaster-runner` if it is not
already built.

Once the image is built, you can create a container from it using the following
command :
```bash
docker compose up discord-bot -d
```

You should see your Discord bot up and running, with some logs about the shards.

## 'Self-signed certificate' error

Although we did our best to minimize the risk, there is still a chance that
depending on the stack you are using, or how you configured it, your bot is
unable to trust the Root CA from the proxy.

Effectively, even if the Root CA is mounted on the container, some technologies
such as Node and Bun decide not to trust non-default certificates. A workaround
for those two exists, which consists of an environment variable declaration that
you can find in the `docker-compose.yml`, which is `NODE_EXTRA_CA_CERTS`. This
variable points to the mounted Root CA path.

We are aware of that issue for Node and Bun, but it is possible that the stack
you are using requires a different configuration to actually trust the Root CA.
You can add an environment variable for that.

Another workaround we are willing to implement is to use an environment variable
to override the default Discord API URL so that it will reach to our own API. In
that regard, we will still be able to read through the requests and responses to
provide metrics and logs, but at the same time, returning a valid TLS response
that is not perceived as 'self-signed' by the client.

NOTE: this last paragraph yapping may be wrong. Perhaps the solution is just to
use LetsEncrypt to generate the Root CA, as it is popular and widely trusted.
