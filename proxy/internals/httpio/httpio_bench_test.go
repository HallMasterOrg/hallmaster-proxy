package httpio

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"testing"

	"github.com/andybalholm/brotli"
)

// makeBody freshly wraps `payload` so each iteration has a Body that
// hasn't been drained yet. DecodeBody rewinds the body but consumes it,
// so we can't share one resp across iterations.
func makeBody(encoding, contentType string, body []byte) func() *http.Response {
	return func() *http.Response {
		r := &http.Response{
			Request: &http.Request{},
			Header:  make(http.Header),
			Body:    io.NopCloser(bytes.NewReader(body)),
		}
		if encoding != "" {
			r.Header.Set("Content-Encoding", encoding)
		}
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		return r
	}
}

func encodeGzip(b *testing.B, payload []byte) []byte {
	b.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(payload); err != nil {
		b.Fatalf("gzip write: %v", err)
	}
	if err := w.Close(); err != nil {
		b.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func encodeDeflate(b *testing.B, payload []byte) []byte {
	b.Helper()
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(payload); err != nil {
		b.Fatalf("zlib write: %v", err)
	}
	if err := w.Close(); err != nil {
		b.Fatalf("zlib close: %v", err)
	}
	return buf.Bytes()
}

func encodeBrotli(b *testing.B, payload []byte) []byte {
	b.Helper()
	var buf bytes.Buffer
	w := brotli.NewWriter(&buf)
	if _, err := w.Write(payload); err != nil {
		b.Fatalf("brotli write: %v", err)
	}
	if err := w.Close(); err != nil {
		b.Fatalf("brotli close: %v", err)
	}
	return buf.Bytes()
}

const (
	smallPayloadSize = 1024
	largePayloadSize = 100 * 1024
)

func runDecodeBench(b *testing.B, mk func() *http.Response) {
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		resp := mk()
		out, err := DecodeBody(resp)
		if err != nil {
			b.Fatalf("decode: %v", err)
		}
		if len(out) == 0 {
			b.Fatalf("empty decode result")
		}
	}
}

func BenchmarkDecodeBody_Gzip_1KB(b *testing.B) {
	payload := bytes.Repeat([]byte("a"), smallPayloadSize)
	body := encodeGzip(b, payload)
	runDecodeBench(b, makeBody("gzip", "application/json", body))
}

func BenchmarkDecodeBody_Gzip_100KB(b *testing.B) {
	payload := bytes.Repeat([]byte("a"), largePayloadSize)
	body := encodeGzip(b, payload)
	runDecodeBench(b, makeBody("gzip", "application/json", body))
}

func BenchmarkDecodeBody_Deflate_1KB(b *testing.B) {
	payload := bytes.Repeat([]byte("a"), smallPayloadSize)
	body := encodeDeflate(b, payload)
	runDecodeBench(b, makeBody("deflate", "application/json", body))
}

func BenchmarkDecodeBody_Deflate_100KB(b *testing.B) {
	payload := bytes.Repeat([]byte("a"), largePayloadSize)
	body := encodeDeflate(b, payload)
	runDecodeBench(b, makeBody("deflate", "application/json", body))
}

func BenchmarkDecodeBody_Brotli_1KB(b *testing.B) {
	payload := bytes.Repeat([]byte("a"), smallPayloadSize)
	body := encodeBrotli(b, payload)
	runDecodeBench(b, makeBody("br", "application/json", body))
}

func BenchmarkDecodeBody_Brotli_100KB(b *testing.B) {
	payload := bytes.Repeat([]byte("a"), largePayloadSize)
	body := encodeBrotli(b, payload)
	runDecodeBench(b, makeBody("br", "application/json", body))
}

func BenchmarkDecodeBody_Identity_100KB(b *testing.B) {
	payload := bytes.Repeat([]byte("a"), largePayloadSize)
	runDecodeBench(b, makeBody("", "application/json", payload))
}
