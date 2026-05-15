package handlers_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"hallmasterorg/hallmaster-proxy/internals"
	"hallmasterorg/hallmaster-proxy/internals/certs"
	"hallmasterorg/hallmaster-proxy/internals/config"
	"hallmasterorg/hallmaster-proxy/internals/handlers"
	"hallmasterorg/hallmaster-proxy/internals/internaltest"

	"log/slog"
)

// recordingTamperer captures every call into a slice so tests can assert
// on what the handler observed. It is the shape the future integration
// test branch will use as its test seam.
type recordingTamperer struct {
	mu sync.Mutex

	Requests  []*http.Request
	Responses []recordedResponse
	WSIn      [][]byte
	WSOut     [][]byte
}

type recordedResponse struct {
	Req         *http.Request
	Status      int
	DecodedBody []byte
}

func (r *recordingTamperer) Request(req *http.Request) (*http.Request, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Requests = append(r.Requests, req)
	return req, nil
}

func (r *recordingTamperer) Response(req *http.Request, resp *http.Response, decodedBody []byte) (*http.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Responses = append(r.Responses, recordedResponse{Req: req, Status: resp.StatusCode, DecodedBody: append([]byte(nil), decodedBody...)})
	return resp, nil
}

func (r *recordingTamperer) WSIncoming(payload []byte) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.WSIn = append(r.WSIn, append([]byte(nil), payload...))
	return payload, nil
}

func (r *recordingTamperer) WSOutgoing(payload []byte) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.WSOut = append(r.WSOut, append([]byte(nil), payload...))
	return payload, nil
}

// stubResolver returns a single canned host:port for every lookup. The
// shape mirrors dummyResolver in dnsbypass/resolver_test.go.
type stubResolver struct{ addr string }

func (s stubResolver) Resolve(_ context.Context, _ string) (string, error) {
	if s.addr == "" {
		return "", fmt.Errorf("no addr")
	}
	return s.addr, nil
}

// readPEMCert loads a PEM-encoded x509 certificate from disk.
func readPEMCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	block, _ := pem.Decode(b)
	if block == nil {
		t.Fatalf("no PEM block in %s", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert
}

// buildE2EHarness wires everything the e2e test needs:
//   - A fake Discord backend on httptest.NewTLSServer driven by `backend`.
//   - An in-memory proxy CA + MITMProxy.
//   - HandlerDeps with UpstreamTLSConfig trusting the backend.
//   - A net.Pipe pair; one end is TLS-terminated via MITMProxy.Handshake.
//
// Returns the recording tamperer (for assertions), the client-side
// *tls.Conn the test will drive, and a cleanup function.
func buildE2EHarness(t *testing.T, backend http.Handler) (*recordingTamperer, *tls.Conn, func()) {
	t.Helper()

	// 1. Upstream CA + a leaf cert for "discord.com" so the proxy's
	//    upstream TLS dial (ServerName=discord.com) is satisfied. This is
	//    the precise scenario UpstreamTLSConfig.RootCAs exists to enable.
	upstreamCACertPath, upstreamCAKeyPath := internaltest.WriteTestCA(t)
	upstreamLeaf, upstreamCACert := internaltest.IssueLeaf(t, upstreamCACertPath, upstreamCAKeyPath, []string{"discord.com"})

	server := httptest.NewUnstartedServer(backend)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{upstreamLeaf}}
	server.StartTLS()

	backendCAs := x509.NewCertPool()
	backendCAs.AddCert(upstreamCACert)

	// 2. In-memory proxy CA (the one the bot client trusts).
	caCertPath, caKeyPath := internaltest.WriteTestCA(t)
	proxyCerts, err := certs.New(caCertPath, caKeyPath)
	if err != nil {
		t.Fatalf("certs.New: %v", err)
	}
	proxyCACert := readPEMCert(t, caCertPath)

	// 3. Build a minimal Config + MITMProxy.
	cfg := &config.Config{
		Hostname:            "hallmaster-proxy",
		Port:                "443",
		UpstreamDialTimeout: 5 * time.Second,
		DNSTimeout:          1 * time.Second,
	}
	p := internals.NewMITMProxy(cfg, proxyCerts)

	// 4. Strip the scheme from server.URL to get host:port for the
	//    Resolver stub.
	backendAddr := server.Listener.Addr().String()

	// 5. recordingTamperer + HandlerDeps with the new upstream hooks.
	rec := &recordingTamperer{}
	deps := internals.HandlerDeps{
		Cfg:               cfg,
		Tamperer:          rec,
		Resolver:          stubResolver{addr: backendAddr},
		Handshaker:        p,
		ProxyHostPort:     "hallmaster-proxy:443",
		CleanHostname:     "hallmaster-proxy",
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		UpstreamTLSConfig: &tls.Config{RootCAs: backendCAs},
	}

	// 6. net.Pipe: serverSide is what the handler sees post-Handshake,
	//    clientSide is what the test drives via tls.Client.
	serverSide, clientSide := net.Pipe()

	// 7. Spawn the proxy-side handler in a goroutine. It performs the TLS
	//    server handshake using a leaf cert for "discord.com" then runs
	//    HttpsHandler on the resulting *tls.Conn.
	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		tlsConn, err := p.Handshake(serverSide, "discord.com")
		if err != nil {
			t.Logf("proxy-side handshake: %v", err)
			return
		}
		handlers.HttpsHandler(deps, tlsConn)
	}()

	// 8. Client side: trust the proxy CA, ServerName=discord.com.
	clientPool := x509.NewCertPool()
	clientPool.AddCert(proxyCACert)
	tlsClient := tls.Client(clientSide, &tls.Config{
		RootCAs:    clientPool,
		ServerName: "discord.com",
	})

	cleanup := func() {
		_ = tlsClient.Close()
		_ = serverSide.Close()
		server.Close()
		// Wait for the handler goroutine to exit so the test doesn't leak.
		select {
		case <-handlerDone:
		case <-time.After(2 * time.Second):
			t.Log("handler goroutine did not exit within 2s")
		}
	}

	return rec, tlsClient, cleanup
}

func TestHttpsHandler_RequestResponseRoundTrip(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v10/gateway" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"url":"wss://gateway.discord.gg"}`))
	})

	rec, tlsClient, cleanup := buildE2EHarness(t, backend)
	defer cleanup()

	// Drive the request from the client side. http.Request.Write needs an
	// absolute URL OR an explicit Host header — the connection is to
	// "discord.com" via TLS SNI.
	req, err := http.NewRequest("GET", "https://discord.com/api/v10/gateway", nil)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	if err := req.Write(tlsClient); err != nil {
		t.Fatalf("write req: %v", err)
	}

	// Read the response.
	resp, err := http.ReadResponse(bufio.NewReader(tlsClient), req)
	if err != nil {
		t.Fatalf("read resp: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if got, want := string(body), `{"url":"wss://gateway.discord.gg"}`; got != want {
		t.Fatalf("body: got %q want %q", got, want)
	}

	// Assert the tamperer saw the round trip.
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.Requests) != 1 {
		t.Fatalf("Requests: got %d want 1", len(rec.Requests))
	}
	if rec.Requests[0].URL.Path != "/api/v10/gateway" {
		t.Fatalf("request path: got %q", rec.Requests[0].URL.Path)
	}
	if len(rec.Responses) != 1 {
		t.Fatalf("Responses: got %d want 1", len(rec.Responses))
	}
	if rec.Responses[0].Status != http.StatusOK {
		t.Fatalf("response status seen by tamperer: %d", rec.Responses[0].Status)
	}
	if string(rec.Responses[0].DecodedBody) != `{"url":"wss://gateway.discord.gg"}` {
		t.Fatalf("decoded body: got %q", rec.Responses[0].DecodedBody)
	}
}

// TestHttpsHandler_GzippedUpstream is the regression test for the
// double-gzip bug that surfaced against discord.js: Encode used to
// re-compress an already-gzipped body because DecodeBody no longer
// strips Content-Encoding (R5). The bot would then receive
// gzip-wrapped-in-gzip and fail to JSON-parse the result.
func TestHttpsHandler_GzippedUpstream(t *testing.T) {
	const jsonBody = `{"url":"wss://gateway.discord.gg","shards":1,"session_start_limit":{"max_concurrency":1,"remaining":1000,"reset_after":0,"total":1000}}`

	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Compress the body just like Discord does when the client
		// advertises Accept-Encoding: gzip.
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		_, _ = gz.Write([]byte(jsonBody))
		_ = gz.Close()

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", buf.Len()))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf.Bytes())
	})

	rec, tlsClient, cleanup := buildE2EHarness(t, backend)
	defer cleanup()

	req, err := http.NewRequest("GET", "https://discord.com/api/v10/gateway/bot", nil)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	if err := req.Write(tlsClient); err != nil {
		t.Fatalf("write req: %v", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(tlsClient), req)
	if err != nil {
		t.Fatalf("read resp: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding: got %q want gzip", got)
	}

	// Manually gunzip and verify the bot sees the original JSON.
	gr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v (this is the discord.js failure mode — Encode double-gzipped the body)", err)
	}
	defer gr.Close()
	decoded, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("read gunzipped: %v", err)
	}
	if string(decoded) != jsonBody {
		t.Fatalf("body mismatch:\n got %q\nwant %q", decoded, jsonBody)
	}

	// Tamperer should still have observed the *decoded* JSON, not the
	// compressed bytes (that's the R5 contract).
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.Responses) != 1 {
		t.Fatalf("Responses: got %d want 1", len(rec.Responses))
	}
	if string(rec.Responses[0].DecodedBody) != jsonBody {
		t.Fatalf("tamperer decoded body:\n got %q\nwant %q", rec.Responses[0].DecodedBody, jsonBody)
	}
}
