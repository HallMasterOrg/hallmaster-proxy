package certs

import (
	"crypto/tls"
	"crypto/x509"
	"hallmasterorg/hallmaster-proxy/internals/internaltest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestServerNameFromHost(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"discord.com", "discord.com"},
		{"discord.com:443", "discord.com"},
		{"gateway.discord.gg:443", "gateway.discord.gg"},
		{"localhost", "localhost"},
	}
	for _, tc := range cases {
		got := serverNameFromHost(tc.in)
		if got != tc.want {
			t.Errorf("serverNameFromHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGetOrCreateCert_CachesPerHost(t *testing.T) {
	certPath, keyPath := internaltest.WriteTestCA(t)
	c, err := New(certPath, keyPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	first, err := c.GetOrCreateCert("discord.com")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := c.GetOrCreateCert("discord.com")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first != second {
		t.Fatalf("expected cached cert (pointer equality), got distinct certs")
	}

	other, err := c.GetOrCreateCert("gateway.discord.gg")
	if err != nil {
		t.Fatalf("other: %v", err)
	}
	if other == first {
		t.Fatalf("expected per-host cache, gateway.discord.gg shares cert with discord.com")
	}
}

func TestGetOrCreateCert_LeafIsPopulated(t *testing.T) {
	certPath, keyPath := internaltest.WriteTestCA(t)
	c, err := New(certPath, keyPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cert, err := c.GetOrCreateCert("discord.com")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if cert.Leaf == nil {
		t.Fatal("Leaf is nil; R6 requires it for the expiry check")
	}
	if cert.Leaf.NotAfter.Before(time.Now().Add(leafCertValidity - time.Hour)) {
		t.Fatalf("Leaf.NotAfter too soon: %v", cert.Leaf.NotAfter)
	}
}

func TestGetOrCreateCert_RenewsExpiringCert(t *testing.T) {
	certPath, keyPath := internaltest.WriteTestCA(t)
	c, err := New(certPath, keyPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Seed the cache with a cert whose Leaf.NotAfter is within the renew
	// window — the next GetOrCreateCert call must regenerate.
	stale := &tls.Certificate{Leaf: &x509.Certificate{NotAfter: time.Now().Add(1 * time.Hour)}}
	c.cache.Store("discord.com", stale)

	fresh, err := c.GetOrCreateCert("discord.com")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if fresh == stale {
		t.Fatalf("stale cert was returned; expected regeneration")
	}
	if !isExpiringSoon(stale) {
		t.Fatal("test premise broken: stale cert should report as expiring soon")
	}
	if isExpiringSoon(fresh) {
		t.Fatal("freshly issued cert reports as expiring soon")
	}
}

func TestCAKeyRejectsPermissiveMode(t *testing.T) {
	certPath, keyPath := internaltest.WriteTestCA(t)

	// Loosen the key permissions to world-readable. New() must refuse.
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	_, err := New(certPath, keyPath)
	if err == nil {
		t.Fatal("expected New to reject 0644 key, got nil error")
	}
	if !strings.Contains(err.Error(), "permissive mode") {
		t.Fatalf("expected permissive-mode error, got: %v", err)
	}

	// Restore 0600 and verify it loads.
	if err := os.Chmod(keyPath, 0o600); err != nil {
		t.Fatalf("chmod back: %v", err)
	}
	if _, err := New(certPath, keyPath); err != nil {
		t.Fatalf("expected 0600 key to load, got: %v", err)
	}
}

func TestIsExpiringSoon(t *testing.T) {
	if !isExpiringSoon(nil) {
		t.Error("nil cert must be treated as expiring")
	}
	if !isExpiringSoon(&tls.Certificate{}) {
		t.Error("cert with nil Leaf must be treated as expiring")
	}
	soon := &tls.Certificate{Leaf: &x509.Certificate{NotAfter: time.Now().Add(1 * time.Hour)}}
	if !isExpiringSoon(soon) {
		t.Error("cert expiring in 1h should be flagged")
	}
	far := &tls.Certificate{Leaf: &x509.Certificate{NotAfter: time.Now().Add(72 * time.Hour)}}
	if isExpiringSoon(far) {
		t.Error("cert valid for 72h should not be flagged")
	}
}
