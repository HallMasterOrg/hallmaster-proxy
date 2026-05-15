package httpio

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"

	"github.com/andybalholm/brotli"
)

// DecodeBody reads `resp.Body`, rewinds it (so downstream readers still see
// the original bytes), and returns the decompressed payload per the
// response's Content-Encoding header.
//
// Unlike a tampering-style decode, DecodeBody does NOT mutate `resp.Header`:
// `Content-Encoding` stays put so a subsequent Encode() can re-frame the
// response with the same on-the-wire encoding.
//
// Returns (nil, nil) when there is nothing useful to decode: nil/missing
// body, binary content type, or empty payload. Callers should treat nil as
// "no readable body" and fall back to whatever default makes sense
// (e.g. "<empty>" / "<blob media>" in log lines).
func DecodeBody(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Request == nil || resp.Body == nil {
		return nil, nil
	}

	if isBinary(resp.Header.Get("Content-Type")) {
		return nil, nil
	}

	fullBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	resp.Body = io.NopCloser(bytes.NewReader(fullBody))

	if len(fullBody) == 0 {
		return nil, nil
	}

	encoding := resp.Header.Get("Content-Encoding")
	var reader io.ReadCloser
	var decodeErr error

	switch encoding {
	case "gzip":
		reader, decodeErr = gzip.NewReader(bytes.NewReader(fullBody))
	case "deflate":
		reader, decodeErr = zlib.NewReader(bytes.NewReader(fullBody))
	case "br":
		reader = io.NopCloser(brotli.NewReader(bytes.NewReader(fullBody)))
	default:
		return fullBody, nil
	}

	if decodeErr != nil {
		return nil, decodeErr
	}

	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	reader.Close()

	return decompressed, nil
}
