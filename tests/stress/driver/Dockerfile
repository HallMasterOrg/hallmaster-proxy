# syntax=docker/dockerfile:1.7
FROM golang:1.23.2-alpine3.20 AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/driver .

# The driver shells out to `docker exec` and `docker logs`, so the
# runtime image needs the docker CLI. /var/run/docker.sock is mounted
# from the host by docker-compose.stress.yml.
FROM alpine:3.20
RUN apk add --no-cache docker-cli

WORKDIR /app
COPY --from=builder /out/driver /app/driver

ENTRYPOINT [ "/app/driver" ]
