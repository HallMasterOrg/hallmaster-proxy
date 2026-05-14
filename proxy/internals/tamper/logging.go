package tamper

import (
	"bytes"
	"hallmasterorg/hallmaster-proxy/internals/httpio"
	"io"
	"log"
	"net/http"
	"strings"
	"unicode/utf8"
)

// payloadPreviewLimit caps how many bytes of any body / frame are surfaced
// in a single log line. Truncation happens on a UTF-8 rune boundary so
// multi-byte characters are never split.
const payloadPreviewLimit = 8192

// Logging is a verbose pass-through Tamperer that prints every HTTP request,
// HTTP response, and WebSocket frame it sees to the standard log. It does
// not modify any payload — its return value is always equal to its input.
//
// HTTP request bodies are fully read into memory so the log line is complete
// and the body is then rewound for downstream readers. Response bodies are
// decoded (decompressed) via httpio.Decode, which also rewinds them.
type Logging struct{}

func (Logging) Request(req *http.Request) (*http.Request, error) {
	body := readAndRewindBody(req)
	log.Printf("[TAMPER --> %s] %s %s body=%s", req.Method, req.URL.String(), req.Proto, preview(body))
	return req, nil
}

func (Logging) Response(req *http.Request, resp *http.Response) (*http.Response, error) {
	if resp == nil {
		return resp, nil
	}
	body, err := httpio.Decode(resp)
	if err != nil {
		log.Printf("[TAMPER <-- %s] %s body-decode-error=%v", req.Method, req.URL.String(), err)
		return resp, nil
	}
	log.Printf("[TAMPER <-- %s] %s status=%s body=%s",
		req.Method, req.URL.String(), resp.Status, preview(strings.ReplaceAll(body, "\n", " ")))
	return resp, nil
}

func (Logging) WSIncoming(payload []byte) ([]byte, error) {
	log.Printf("[TAMPER WS<-Discord] len=%d payload=%s", len(payload), preview(string(payload)))
	return payload, nil
}

func (Logging) WSOutgoing(payload []byte) ([]byte, error) {
	log.Printf("[TAMPER WS->Discord] len=%d payload=%s", len(payload), preview(string(payload)))
	return payload, nil
}

// readAndRewindBody drains req.Body, leaves it rewound to its full contents,
// and returns the bytes as a string. Returns "<empty>" if the body is nil or
// empty, and "<read-error>" if drain failed.
func readAndRewindBody(req *http.Request) string {
	if req.Body == nil {
		return "<empty>"
	}
	b, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		req.Body = io.NopCloser(bytes.NewReader(nil))
		return "<read-error>"
	}
	req.Body = io.NopCloser(bytes.NewReader(b))
	if len(b) == 0 {
		return "<empty>"
	}
	return string(b)
}

// preview returns `s` shortened to at most payloadPreviewLimit bytes on a
// UTF-8 rune boundary. If truncation happened, a "... [truncated N more]"
// suffix is appended.
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
