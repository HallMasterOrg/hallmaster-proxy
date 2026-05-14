package config

import (
	"fmt"
	"os"
	"time"
)

// Config holds every runtime knob the proxy reads from its environment. It
// is intentionally flat so callers can construct it directly in tests.
type Config struct {
	// Hostname is the DNS name clients use to reach the proxy. It is used
	// to detect "relay" requests (i.e. an HTTPS request whose Host header
	// matches the proxy itself) and to compose the proxy URL passed down
	// to handlers.
	Hostname string

	// Port the proxy listens on for client traffic.
	Port string

	// HealthPort the proxy serves /healthz on.
	HealthPort string

	// CACertPath / CAKeyPath point at the Root CA used to sign per-host
	// leaf certificates on the fly.
	CACertPath string
	CAKeyPath  string

	// UpstreamDialTimeout bounds how long a TLS dial to Discord may take.
	UpstreamDialTimeout time.Duration

	// DNSServer is the external resolver dialled when bypassing the
	// in-container DNS (which is typically rewired to redirect Discord
	// hostnames to the proxy itself).
	DNSServer  string
	DNSTimeout time.Duration
}

// Load reads the proxy's configuration from environment variables and falls
// back to sensible defaults for the optional ones.
func Load() (*Config, error) {
	hostname := os.Getenv("PROXY_HOSTNAME")
	if hostname == "" {
		// Fall back to the system hostname (Docker injects this).
		if h, err := os.Hostname(); err == nil {
			hostname = h
		}
	}
	if hostname == "" {
		return nil, fmt.Errorf("PROXY_HOSTNAME is required")
	}

	port := getenvDefault("PROXY_PORT", "8080")
	healthPort := getenvDefault("PROXY_HEALTH_PORT", "8081")

	caCertPath := os.Getenv("PROXY_SSL_CA_CERT_PATH")
	if caCertPath == "" {
		return nil, fmt.Errorf("PROXY_SSL_CA_CERT_PATH is required")
	}
	caKeyPath := os.Getenv("PROXY_SSL_CA_KEY_PATH")
	if caKeyPath == "" {
		return nil, fmt.Errorf("PROXY_SSL_CA_KEY_PATH is required")
	}

	return &Config{
		Hostname:            hostname,
		Port:                port,
		HealthPort:          healthPort,
		CACertPath:          caCertPath,
		CAKeyPath:           caKeyPath,
		UpstreamDialTimeout: 10 * time.Second,
		DNSServer:           getenvDefault("PROXY_DNS_SERVER", "8.8.8.8:53"),
		DNSTimeout:          5 * time.Second,
	}, nil
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
