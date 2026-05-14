package dnsbypass

import (
	"context"
	"fmt"
	"net"
	"time"
)

// Resolver is the interface MITM hot paths consume. Tests can pass a stub
// that returns canned IPs instead of hitting real DNS.
type Resolver interface {
	Resolve(ctx context.Context, hostPort string) (string, error)
}

// ExternalResolver performs DNS lookups against a configured server, bypassing
// the in-container resolver (which is typically rewired to point at the proxy
// itself via Docker networking).
type ExternalResolver struct {
	resolver *net.Resolver
}

// NewExternalResolver builds a resolver that dials `server` (host:port) for
// every lookup. `timeout` bounds each lookup attempt.
func NewExternalResolver(server string, timeout time.Duration) *ExternalResolver {
	dialer := &net.Dialer{Timeout: timeout}
	return &ExternalResolver{
		resolver: &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "udp", server)
			},
		},
	}
}

// Resolve returns `host:port` with `host` replaced by an IP looked up via the
// external resolver. If `hostPort` lacks a port, 443 is assumed.
func (e *ExternalResolver) Resolve(ctx context.Context, hostPort string) (string, error) {
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		host = hostPort
		port = "443"
	}

	ips, err := e.resolver.LookupHost(ctx, host)
	if err != nil || len(ips) == 0 {
		return "", fmt.Errorf("could not resolve %s externally: %v", host, err)
	}

	return net.JoinHostPort(ips[0], port), nil
}
