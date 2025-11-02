package utils

import (
	"context"
	"fmt"
	"net"
	"time"
)

const DNS_SERVER = "8.8.8.8"

var (
    externalResolver = &net.Resolver{
        PreferGo: true,
        Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
            d := net.Dialer{Timeout: 5 * time.Second}
            return d.DialContext(ctx, "udp", DNS_SERVER + ":53")
        },
    }
)

func ResolveExternal(host string) (string, error) {
    hostname, port, err := net.SplitHostPort(host)
    if err != nil {
        hostname = host
        port = "443"
    }

    ips, err := externalResolver.LookupHost(context.Background(), hostname)
    if err != nil || len(ips) == 0 {
        return "", fmt.Errorf("could not resolve %s externally: %v", hostname, err)
    }

    return net.JoinHostPort(ips[0], port), nil
}