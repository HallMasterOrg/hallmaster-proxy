package tamper

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func newCapturingLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	h := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), buf
}

func mkReq(t *testing.T, method, rawurl, body string) *http.Request {
	t.Helper()
	u, err := url.Parse(rawurl)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	var rc io.ReadCloser
	if body != "" {
		rc = io.NopCloser(strings.NewReader(body))
	}
	return &http.Request{Method: method, URL: u, Proto: "HTTP/1.1", Body: rc}
}

func TestLogging_RequestWithBodies(t *testing.T) {
	logger, buf := newCapturingLogger()
	l := Logging{Logger: logger, LogBodies: true}

	req := mkReq(t, "POST", "https://discord.com/api/v10/channels/1/messages", `{"content":"hi"}`)
	if _, err := l.Request(req); err != nil {
		t.Fatalf("Request: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"tamper http req", "POST", "discord.com", `content`, `hi`, "body_len=16"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected log to contain %q; got %q", want, out)
		}
	}
}

func TestLogging_RequestBodiesOptOut(t *testing.T) {
	logger, buf := newCapturingLogger()
	l := Logging{Logger: logger, LogBodies: false}

	req := mkReq(t, "POST", "https://discord.com/api/v10/channels/1/messages", `{"content":"secret"}`)
	if _, err := l.Request(req); err != nil {
		t.Fatalf("Request: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "secret") {
		t.Errorf("body content leaked with LogBodies=false: %q", out)
	}
	if !strings.Contains(out, "body_len=20") {
		t.Errorf("expected body_len attribute, got %q", out)
	}
}

func TestLogging_ResponseWithBodies(t *testing.T) {
	logger, buf := newCapturingLogger()
	l := Logging{Logger: logger, LogBodies: true}

	req := mkReq(t, "GET", "https://discord.com/api/v10/gateway", "")
	resp := &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{}}
	if _, err := l.Response(req, resp, []byte(`{"url":"wss://gateway.discord.gg"}`)); err != nil {
		t.Fatalf("Response: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"tamper http resp", "200 OK", "gateway.discord.gg", "body_len=34"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected log to contain %q; got %q", want, out)
		}
	}
}

func TestLogging_ResponseBodiesOptOut(t *testing.T) {
	logger, buf := newCapturingLogger()
	l := Logging{Logger: logger, LogBodies: false}

	req := mkReq(t, "GET", "https://discord.com/api/v10/gateway", "")
	resp := &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{}}
	if _, err := l.Response(req, resp, []byte(`{"url":"wss://gateway.discord.gg"}`)); err != nil {
		t.Fatalf("Response: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "gateway.discord.gg") {
		t.Errorf("response body leaked with LogBodies=false: %q", out)
	}
}

func TestLogging_WSFramesAtInfo(t *testing.T) {
	logger, buf := newCapturingLogger()
	l := Logging{Logger: logger, LogBodies: true}

	if _, err := l.WSIncoming([]byte(`{"op":10}`)); err != nil {
		t.Fatalf("WSIncoming: %v", err)
	}
	if _, err := l.WSOutgoing([]byte(`{"op":1}`)); err != nil {
		t.Fatalf("WSOutgoing: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"tamper ws<-discord", "tamper ws->discord", "op", "10"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected log to contain %q; got %q", want, out)
		}
	}
}

func TestLogging_NilLoggerFallsBackToDefault(t *testing.T) {
	// The zero value should not panic; it should resolve to slog.Default().
	l := Logging{}
	if _, err := l.WSIncoming([]byte("ping")); err != nil {
		t.Fatalf("WSIncoming: %v", err)
	}
}
