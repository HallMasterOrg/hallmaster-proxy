FROM golang:tip-alpine3.22 AS builder

WORKDIR /app

COPY ./go.mod ./

RUN go mod download

COPY . .

RUN go build -v -o ./proxy .

FROM alpine:3.20

RUN apk add --no-cache ca-certificates

WORKDIR /app
COPY --from=builder /app/proxy /app/proxy

ENV PROXY_HTTP_PORT=8080
ENV PROXY_SSL_CA_CERT_PATH="/app/cert/proxy-ca.crt"
ENV PROXY_SSL_CA_KEY_PATH="/app/cert/proxy-ca.pem"

ENTRYPOINT [ "/app/proxy" ]
