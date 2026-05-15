package tamper

import (
	"net/http"
)

// Tamperer is the seam test suites and feature code hook into to observe or
// rewrite proxied traffic. The default implementation, Nop, is a pass-through.
//
// Implementations may return a new object (e.g. a request with a rewritten
// header) or mutate the passed-in one and return it back. Returning a non-nil
// error causes the proxy to log a warning and fall back to the original
// payload — tamperers are an enhancement, not a critical path.
//
// Response receives the decoded body bytes as a separate argument so the
// handler is free to feed observers a readable payload without altering the
// response's Content-Encoding. `decodedBody` is nil when the response has no
// body, the content type is binary, or decoding failed; in that case
// tamperers that need bytes should fall back to reading `resp.Body`
// themselves (and rewind it).
//
// WSIncoming receives the *decoded* payload when the gateway is using
// compress=zlib-stream — so tamperers see readable JSON instead of raw
// deflate bytes. For compressed connections the proxy always forwards the
// original frame to the bot regardless of what the tamperer returns; the
// hook is observation-only in that case. For uncompressed connections the
// tamperer's return value is what gets forwarded.
type Tamperer interface {
	Request(req *http.Request) (*http.Request, error)
	Response(req *http.Request, resp *http.Response, decodedBody []byte) (*http.Response, error)
	WSIncoming(payload []byte) ([]byte, error)
	WSOutgoing(payload []byte) ([]byte, error)
}

// Nop is the no-op tamperer used when no test/feature code is plugged in.
type Nop struct{}

func (Nop) Request(req *http.Request) (*http.Request, error) {
	return req, nil
}

func (Nop) Response(_ *http.Request, resp *http.Response, _ []byte) (*http.Response, error) {
	return resp, nil
}

func (Nop) WSIncoming(payload []byte) ([]byte, error) {
	return payload, nil
}

func (Nop) WSOutgoing(payload []byte) ([]byte, error) {
	return payload, nil
}
