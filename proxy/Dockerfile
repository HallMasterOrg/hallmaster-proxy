# syntax=docker/dockerfile:1.7
FROM golang:1.23.2-alpine3.20 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/proxy .

FROM alpine:3.20

RUN apk add --no-cache ca-certificates wget \
 && adduser -D -u 1000 proxy

WORKDIR /app
COPY --from=builder --chown=proxy:proxy /out/proxy /app/proxy

USER proxy

ENV PROXY_PORT=8080
ENV PROXY_HEALTH_PORT=8081
ENV PROXY_SSL_CA_CERT_PATH="/app/cert/proxy-ca.crt"
ENV PROXY_SSL_CA_KEY_PATH="/app/cert/proxy-ca.pem"

HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -q --spider http://127.0.0.1:${PROXY_HEALTH_PORT}/healthz || exit 1

ENTRYPOINT [ "/app/proxy" ]
