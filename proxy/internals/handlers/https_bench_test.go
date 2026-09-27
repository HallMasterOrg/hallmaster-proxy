package handlers_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"testing"
	"time"

	"hallmasterorg/hallmaster-proxy/internals/internaltest"
	"hallmasterorg/hallmaster-proxy/internals/tamper"
)

const benchPath = "/api/v10/gateway"

// smallJSONBackend serves a fixed 200-ish-byte JSON payload with identity
// encoding — the cheapest realistic response shape from Discord.
func smallJSONBackend() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"url":"wss://gateway.discord.gg","shards":1,"session_start_limit":{"max_concurrency":1,"remaining":1000,"reset_after":0,"total":1000}}`))
	})
}

// gzipped10KBackend serves a 10 KB gzipped JSON payload, exercising the
// httpio.DecodeBody path the tamperer is fed.
func gzipped10KBackend(b *testing.B) http.Handler {
	b.Helper()
	payload := bytes.Repeat([]byte(`{"k":"abcdefghij"},`), 500)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(payload); err != nil {
		b.Fatalf("gzip write: %v", err)
	}
	if err := gz.Close(); err != nil {
		b.Fatalf("gzip close: %v", err)
	}
	body := buf.Bytes()

	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
}

// runRoundTrips pipelines b.N HTTP/1.1 requests over a single TLS
// session so the TLS handshake cost is amortised out of the per-op
// number. The bench reports HeapAlloc delta + p50/p95/p99 of
// Request->Response durations via the LatencyTamperer.
func runRoundTrips(b *testing.B, backend http.Handler) {
	tamp := internaltest.NewLatencyTamperer(b.N * 2 /* one Request + one Response per iter */)
	tlsClient, cleanup := buildE2EHarness(b, backend, tamp)
	defer cleanup()

	reader := bufio.NewReader(tlsClient)

	preReq, err := http.NewRequest("GET", "https://discord.com"+benchPath, nil)
	if err != nil {
		b.Fatalf("new req: %v", err)
	}
	preReq.Header.Set("Accept-Encoding", "gzip")

	before := internaltest.SnapshotRuntime()
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if err := preReq.Write(tlsClient); err != nil {
			b.Fatalf("write req at i=%d: %v", i, err)
		}
		resp, err := http.ReadResponse(reader, preReq)
		if err != nil {
			b.Fatalf("read resp at i=%d: %v", i, err)
		}
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			b.Fatalf("drain body: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b.Fatalf("status %d", resp.StatusCode)
		}
	}

	b.StopTimer()
	after := internaltest.SnapshotRuntime()
	diff := before.Sub(after)

	durations := tamp.Pairs(internaltest.KindRequest, internaltest.KindResponse)
	p50, p95, p99 := internaltest.Percentiles(durations)

	b.ReportMetric(float64(p50.Nanoseconds()), "p50-ns/op")
	b.ReportMetric(float64(p95.Nanoseconds()), "p95-ns/op")
	b.ReportMetric(float64(p99.Nanoseconds()), "p99-ns/op")
	b.ReportMetric(float64(diff.HeapAllocBytes)/float64(b.N), "heap-delta-B/op")
	b.ReportMetric(float64(diff.Goroutines), "goroutine-delta")
	b.ReportMetric(float64(tamp.Dropped.Load()), "dropped-samples")
}

func BenchmarkHTTPSRoundtrip_Small(b *testing.B) {
	runRoundTrips(b, smallJSONBackend())
}

func BenchmarkHTTPSRoundtrip_Gzip10K(b *testing.B) {
	runRoundTrips(b, gzipped10KBackend(b))
}

// BenchmarkHTTPSRoundtrip_Parallel measures how the proxy holds up when
// many independent client TLS sessions are active simultaneously. Each
// goroutine builds its own harness — there is no shared client state.
// This isolates per-connection cost rather than per-request cost.
func BenchmarkHTTPSRoundtrip_Parallel(b *testing.B) {
	backend := smallJSONBackend()

	before := internaltest.SnapshotRuntime()
	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		tlsClient, cleanup := buildE2EHarness(b, backend, tamper.Nop{})
		defer cleanup()
		reader := bufio.NewReader(tlsClient)

		req, err := http.NewRequest("GET", "https://discord.com"+benchPath, nil)
		if err != nil {
			b.Errorf("new req: %v", err)
			return
		}

		for pb.Next() {
			if err := req.Write(tlsClient); err != nil {
				b.Errorf("write: %v", err)
				return
			}
			resp, err := http.ReadResponse(reader, req)
			if err != nil {
				b.Errorf("read: %v", err)
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	})

	b.StopTimer()
	after := internaltest.SnapshotRuntime()
	diff := before.Sub(after)
	b.ReportMetric(float64(diff.HeapAllocBytes), "heap-delta-B")
	b.ReportMetric(float64(diff.Goroutines), "goroutine-delta")
}

// BenchmarkConnectHandshake measures the cost of a single CONNECT + TLS
// handshake — i.e. cold-connection setup. Each iteration creates a fresh
// harness, performs the TLS handshake, and tears down. No HTTP requests
// are sent. This isolates per-connection overhead from per-request
// throughput.
func BenchmarkConnectHandshake(b *testing.B) {
	backend := smallJSONBackend()

	// Warm up the runtime so the first iteration isn't an outlier.
	tc, cleanup := buildE2EHarness(b, backend, tamper.Nop{})
	_ = tc.Handshake()
	cleanup()
	runtime.GC()

	before := internaltest.SnapshotRuntime()
	b.ResetTimer()
	b.ReportAllocs()

	latencies := make([]time.Duration, 0, b.N)
	for i := 0; i < b.N; i++ {
		tlsClient, cleanup := buildE2EHarness(b, backend, tamper.Nop{})
		start := time.Now()
		if err := tlsClient.Handshake(); err != nil {
			b.Fatalf("handshake at i=%d: %v", i, err)
		}
		latencies = append(latencies, time.Since(start))
		cleanup()
	}

	b.StopTimer()
	after := internaltest.SnapshotRuntime()
	diff := before.Sub(after)

	p50, p95, p99 := internaltest.Percentiles(latencies)
	b.ReportMetric(float64(p50.Nanoseconds()), "p50-ns/op")
	b.ReportMetric(float64(p95.Nanoseconds()), "p95-ns/op")
	b.ReportMetric(float64(p99.Nanoseconds()), "p99-ns/op")
	b.ReportMetric(float64(diff.HeapAllocBytes)/float64(b.N), "heap-delta-B/op")
}
