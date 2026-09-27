package certs

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"testing"
	"time"

	"hallmasterorg/hallmaster-proxy/internals/internaltest"
)

// BenchmarkLeafIssue_CacheMiss measures the cold path: a new hostname
// requires generating an RSA key, building a template, signing it with
// the CA, and parsing the result back into a *x509.Certificate. Each
// iteration uses a fresh hostname so the sync.Map lookup misses.
func BenchmarkLeafIssue_CacheMiss(b *testing.B) {
	certPath, keyPath := internaltest.WriteTestCA(b)
	c, err := New(certPath, keyPath)
	if err != nil {
		b.Fatalf("New: %v", err)
	}

	before := internaltest.SnapshotRuntime()
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		host := fmt.Sprintf("h-%d.discord.gg", i)
		if _, err := c.GetOrCreateCert(host); err != nil {
			b.Fatalf("get: %v", err)
		}
	}

	b.StopTimer()
	after := internaltest.SnapshotRuntime()
	diff := before.Sub(after)
	b.ReportMetric(float64(diff.HeapAllocBytes)/float64(b.N), "heap-delta-B/op")
}

// BenchmarkLeafIssue_CacheHit measures the fast path: same hostname
// every iter, hits sync.Map and the not-expiring branch. This is the
// number that dominates production once the cache is warm.
func BenchmarkLeafIssue_CacheHit(b *testing.B) {
	certPath, keyPath := internaltest.WriteTestCA(b)
	c, err := New(certPath, keyPath)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	// Pre-warm the cache.
	if _, err := c.GetOrCreateCert("discord.com"); err != nil {
		b.Fatalf("warm: %v", err)
	}

	before := internaltest.SnapshotRuntime()
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if _, err := c.GetOrCreateCert("discord.com"); err != nil {
			b.Fatalf("get: %v", err)
		}
	}

	b.StopTimer()
	after := internaltest.SnapshotRuntime()
	diff := before.Sub(after)
	b.ReportMetric(float64(diff.HeapAllocBytes)/float64(b.N), "heap-delta-B/op")
}

// BenchmarkLeafIssue_NearExpiry forces the renewal branch every
// iteration by seeding a cert that reports as expiring soon, so every
// call re-issues. Worst case for a long-running proxy whose certs all
// hit the 24 h renew window in the same minute.
func BenchmarkLeafIssue_NearExpiry(b *testing.B) {
	certPath, keyPath := internaltest.WriteTestCA(b)
	c, err := New(certPath, keyPath)
	if err != nil {
		b.Fatalf("New: %v", err)
	}

	before := internaltest.SnapshotRuntime()
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Seed a stale cert before each call so the cache hit branch
		// detects it as expiring and triggers re-issue.
		stale := &tls.Certificate{Leaf: &x509.Certificate{NotAfter: time.Now().Add(1 * time.Hour)}}
		c.cache.Store("discord.com", stale)
		if _, err := c.GetOrCreateCert("discord.com"); err != nil {
			b.Fatalf("get: %v", err)
		}
	}

	b.StopTimer()
	after := internaltest.SnapshotRuntime()
	diff := before.Sub(after)
	b.ReportMetric(float64(diff.HeapAllocBytes)/float64(b.N), "heap-delta-B/op")
}

// BenchmarkLeafIssue_ParallelDistinctHosts simulates the worst real-world
// pattern: N concurrent goroutines each requesting a unique host. The
// singleflight group never coalesces (every key is unique), so all
// goroutines pay the full RSA cost in parallel. This is the closest
// per-host upper bound the proxy can experience.
func BenchmarkLeafIssue_ParallelDistinctHosts(b *testing.B) {
	certPath, keyPath := internaltest.WriteTestCA(b)
	c, err := New(certPath, keyPath)
	if err != nil {
		b.Fatalf("New: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			host := fmt.Sprintf("p-%p-%d.discord.gg", pb, i)
			i++
			if _, err := c.GetOrCreateCert(host); err != nil {
				b.Fatalf("get: %v", err)
			}
		}
	})
}
