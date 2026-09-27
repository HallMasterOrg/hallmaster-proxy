package tamper

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"testing"
)

// fixedRequest returns a *http.Request with a body that can be re-read by
// repeatedly resetting `body`. Each bench iteration rewinds before the
// hook is invoked so the tamperer always sees the full payload.
func fixedRequest(payload []byte) (*http.Request, *bytes.Reader) {
	body := bytes.NewReader(payload)
	req, _ := http.NewRequest("POST", "https://discord.com/api/v10/channels/1/messages", body)
	req.ContentLength = int64(len(payload))
	return req, body
}

// fixedResponse returns a *http.Response with `body` as its body bytes
// already-decoded. Logging.Response receives decodedBody as a separate
// argument so the response Body doesn't have to be re-readable.
func fixedResponse() *http.Response {
	return &http.Response{
		Status:     "200 OK",
		StatusCode: 200,
		Header:     http.Header{},
	}
}

const sampleBody = `{"id":"1234567890","channel_id":"42","content":"hello from bench","author":{"id":"99","username":"bench"}}`

func BenchmarkTamperer_Nop_Request(b *testing.B) {
	req, body := fixedRequest([]byte(sampleBody))
	var t Nop

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = body.Seek(0, io.SeekStart)
		if _, err := t.Request(req); err != nil {
			b.Fatalf("req: %v", err)
		}
	}
}

func BenchmarkTamperer_Nop_Response(b *testing.B) {
	req, _ := fixedRequest(nil)
	resp := fixedResponse()
	decoded := []byte(sampleBody)
	var t Nop

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := t.Response(req, resp, decoded); err != nil {
			b.Fatalf("resp: %v", err)
		}
	}
}

// silentLogging is a Logging tamperer whose slog handler writes to
// io.Discard, isolating the bench from terminal I/O cost.
func silentLogging(bodies bool) Logging {
	return Logging{
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		LogBodies: bodies,
	}
}

func BenchmarkTamperer_Logging_Request_NoBodies(b *testing.B) {
	req, body := fixedRequest([]byte(sampleBody))
	t := silentLogging(false)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = body.Seek(0, io.SeekStart)
		req.Body = io.NopCloser(body)
		if _, err := t.Request(req); err != nil {
			b.Fatalf("req: %v", err)
		}
	}
}

func BenchmarkTamperer_Logging_Request_WithBodies(b *testing.B) {
	req, body := fixedRequest([]byte(sampleBody))
	t := silentLogging(true)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = body.Seek(0, io.SeekStart)
		req.Body = io.NopCloser(body)
		if _, err := t.Request(req); err != nil {
			b.Fatalf("req: %v", err)
		}
	}
}

func BenchmarkTamperer_Logging_Response_NoBodies(b *testing.B) {
	req, _ := fixedRequest(nil)
	resp := fixedResponse()
	decoded := []byte(sampleBody)
	t := silentLogging(false)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := t.Response(req, resp, decoded); err != nil {
			b.Fatalf("resp: %v", err)
		}
	}
}

func BenchmarkTamperer_Logging_Response_WithBodies(b *testing.B) {
	req, _ := fixedRequest(nil)
	resp := fixedResponse()
	decoded := []byte(sampleBody)
	t := silentLogging(true)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := t.Response(req, resp, decoded); err != nil {
			b.Fatalf("resp: %v", err)
		}
	}
}

// BenchmarkTamperer_Logging_WSFrame benchmarks the WS-frame hot path —
// most relevant under stress because gateway frames are by far the most
// frequent observer call.
func BenchmarkTamperer_Logging_WSFrame(b *testing.B) {
	payload := bytes.Repeat([]byte("a"), 1024)
	t := silentLogging(false)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := t.WSIncoming(payload); err != nil {
			b.Fatalf("ws: %v", err)
		}
	}
}
