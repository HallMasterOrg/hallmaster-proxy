package httpio

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
)

func mkResp(encoding, contentType string, body []byte) *http.Response {
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

func TestDecodeBody_Identity(t *testing.T) {
	resp := mkResp("", "application/json", []byte(`{"hello":"world"}`))
	got, err := DecodeBody(resp)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if string(got) != `{"hello":"world"}` {
		t.Fatalf("got %q", got)
	}
	// Body must still be readable downstream.
	rest, _ := io.ReadAll(resp.Body)
	if string(rest) != `{"hello":"world"}` {
		t.Fatalf("body not rewound: got %q", rest)
	}
}

func TestDecodeBody_Gzip(t *testing.T) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write([]byte("gzipped payload"))
	w.Close()

	resp := mkResp("gzip", "application/json", buf.Bytes())
	got, err := DecodeBody(resp)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if string(got) != "gzipped payload" {
		t.Fatalf("got %q", got)
	}
	// R5 invariant: Content-Encoding must not be stripped.
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding stripped: %q", resp.Header.Get("Content-Encoding"))
	}
	// Body must still be the *original compressed* bytes for downstream
	// Encode() to re-frame.
	rest, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(rest, buf.Bytes()) {
		t.Fatalf("body mutated: expected original compressed bytes")
	}
}

func TestDecodeBody_Deflate(t *testing.T) {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	w.Write([]byte("deflate payload"))
	w.Close()

	resp := mkResp("deflate", "application/json", buf.Bytes())
	got, err := DecodeBody(resp)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if string(got) != "deflate payload" {
		t.Fatalf("got %q", got)
	}
	if resp.Header.Get("Content-Encoding") != "deflate" {
		t.Fatalf("Content-Encoding stripped")
	}
}

func TestDecodeBody_Br(t *testing.T) {
	var buf bytes.Buffer
	w := brotli.NewWriter(&buf)
	w.Write([]byte("brotli payload"))
	w.Close()

	resp := mkResp("br", "application/json", buf.Bytes())
	got, err := DecodeBody(resp)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if string(got) != "brotli payload" {
		t.Fatalf("got %q", got)
	}
	if resp.Header.Get("Content-Encoding") != "br" {
		t.Fatalf("Content-Encoding stripped")
	}
}

func TestDecodeBody_NilBody(t *testing.T) {
	resp := &http.Response{Request: &http.Request{}, Header: make(http.Header)}
	got, err := DecodeBody(resp)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %q", got)
	}
}

func TestDecodeBody_NilResponse(t *testing.T) {
	got, err := DecodeBody(nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %q", got)
	}
}

func TestDecodeBody_EmptyBody(t *testing.T) {
	resp := mkResp("", "application/json", []byte{})
	got, err := DecodeBody(resp)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for empty body, got %q", got)
	}
}

func TestDecodeBody_BinaryContentType(t *testing.T) {
	resp := mkResp("", "image/png", []byte{0x89, 0x50, 0x4e, 0x47})
	got, err := DecodeBody(resp)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for binary content type, got %v", got)
	}
}

func TestDecodeBody_UnknownEncodingPassthrough(t *testing.T) {
	resp := mkResp("identity", "application/json", []byte("raw"))
	got, err := DecodeBody(resp)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if string(got) != "raw" {
		t.Fatalf("got %q", got)
	}
}

func TestDecodeBody_LongPayloadRoundTrip(t *testing.T) {
	payload := strings.Repeat("abcdefghij", 10_000)
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write([]byte(payload))
	w.Close()

	resp := mkResp("gzip", "application/json", buf.Bytes())
	got, err := DecodeBody(resp)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("length mismatch: got %d want %d", len(got), len(payload))
	}
}
