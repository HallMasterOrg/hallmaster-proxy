package httpio

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"

	"github.com/andybalholm/brotli"
)

// Decode reads `resp.Body`, replaces it with an equivalent in-memory reader,
// and returns the decompressed body as a string for logging and inspection.
// It strips Content-Encoding from the response header so subsequent writers
// see the body as identity-encoded.
func Decode(resp *http.Response) (string, error) {
	if resp == nil || resp.Request == nil {
		return "<empty>", nil
	}

	if isBinary(resp.Header.Get("Content-Type")) {
		return "<blob media>", nil
	}

	if resp.Body == nil {
		return "<empty>", nil
	}

	fullBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	resp.Body = io.NopCloser(bytes.NewReader(fullBody))

	if len(fullBody) == 0 {
		return "<empty>", nil
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
		return string(fullBody), nil
	}

	if decodeErr != nil {
		return "", decodeErr
	}

	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	reader.Close()

	resp.Body = io.NopCloser(bytes.NewReader(decompressed))
	resp.Header.Del("Content-Encoding")

	return string(decompressed), nil
}
