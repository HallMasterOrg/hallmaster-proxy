package tamper

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"
)

// payloadPreviewLimit caps how many bytes of any body / frame are surfaced
// in a single log line. Truncation happens on a UTF-8 rune boundary so
// multi-byte characters are never split.
const payloadPreviewLimit = 8192

// Logging is a verbose pass-through Tamperer that emits a structured log
// record for every HTTP request, HTTP response, and WebSocket frame it
// sees. It does not modify any payload — its return value is always equal
// to its input.
//
// HTTP request bodies are fully read into memory so the log line is complete
// and the body is then rewound for downstream readers. HTTP response bodies
// arrive pre-decoded via the `decodedBody` argument; nil means the body was
// empty, binary, or undecodable.
//
// Logger, when nil, falls back to slog.Default() — keeping the zero value
// useful so existing code that uses `tamper.Logging{}` still compiles.
// Production wiring in main builds it via `tamper.Logging{Logger:
// deps.Logger, LogBodies: cfg.LogBodies}` so every log line flows through
// the same logger that handlers and mitm use.
//
// LogBodies controls whether full request/response bodies are emitted.
// When false, only a `body_len` attribute is included so observers still
// see traffic volume but not payload content (operators may want this in
// shared environments). Defaults to false on the zero value; main flips
// it true to preserve developer-visibility behaviour.
type Logging struct {
	Logger    *slog.Logger
	LogBodies bool
}

func (l Logging) log() *slog.Logger {
	if l.Logger != nil {
		return l.Logger
	}
	return slog.Default()
}

func (l Logging) Request(req *http.Request) (*http.Request, error) {
	body, bodyLen := readAndRewindBody(req)
	attrs := []any{"method", req.Method, "url", req.URL.String(), "proto", req.Proto, "body_len", bodyLen}
	if l.LogBodies {
		attrs = append(attrs, "body", preview(body))
	}
	l.log().Info("tamper http req", attrs...)
	return req, nil
}

func (l Logging) Response(req *http.Request, resp *http.Response, decodedBody []byte) (*http.Response, error) {
	if resp == nil {
		return resp, nil
	}
	attrs := []any{"method", req.Method, "url", req.URL.String(), "status", resp.Status, "body_len", len(decodedBody)}
	if l.LogBodies {
		body := "<empty>"
		if decodedBody != nil {
			body = preview(strings.ReplaceAll(string(decodedBody), "\n", " "))
		}
		attrs = append(attrs, "body", body)
	}
	l.log().Info("tamper http resp", attrs...)
	return resp, nil
}

func (l Logging) WSIncoming(payload []byte) ([]byte, error) {
	attrs := []any{"len", len(payload)}
	if l.LogBodies {
		attrs = append(attrs, "payload", preview(string(payload)))
	}
	// Info, not Debug: the Logging tamperer is the developer-inspection
	// path, so WebSocket content is part of its primary product, just
	// like HTTP req/resp above. Per-frame operational chatter (every
	// heartbeat ack, every fragment) stays at Debug in handlers/ws.go.
	l.log().Info("tamper ws<-discord", attrs...)
	return payload, nil
}

func (l Logging) WSOutgoing(payload []byte) ([]byte, error) {
	attrs := []any{"len", len(payload)}
	if l.LogBodies {
		attrs = append(attrs, "payload", preview(string(payload)))
	}
	l.log().Info("tamper ws->discord", attrs...)
	return payload, nil
}

// readAndRewindBody drains req.Body, leaves it rewound to its full contents,
// and returns the bytes as a string along with the byte count. Returns
// ("<empty>", 0) if the body is nil or empty, and ("<read-error>", 0) if
// drain failed.
func readAndRewindBody(req *http.Request) (string, int) {
	if req.Body == nil {
		return "<empty>", 0
	}
	b, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		req.Body = io.NopCloser(bytes.NewReader(nil))
		return "<read-error>", 0
	}
	req.Body = io.NopCloser(bytes.NewReader(b))
	if len(b) == 0 {
		return "<empty>", 0
	}
	return string(b), len(b)
}

// preview returns `s` shortened to at most payloadPreviewLimit bytes on a
// UTF-8 rune boundary. If truncation happened, a "... [truncated]" suffix is
// appended.
func preview(s string) string {
	if len(s) <= payloadPreviewLimit {
		return s
	}
	end := payloadPreviewLimit
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + "... [truncated]"
}
