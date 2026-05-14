package httpio

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"strconv"

	"github.com/andybalholm/brotli"
)

// Encode normalises `resp.Body` for forwarding to the client: it reads the
// (already-decoded) body, optionally re-compresses it per the response's
// Content-Encoding header, and updates Content-Length so the response can
// be written with a fresh framing.
func Encode(resp *http.Response) error {
	if resp == nil || resp.Request == nil || resp.Body == nil {
		return nil
	}

	if isBinary(resp.Header.Get("Content-Type")) {
		return nil
	}

	fullBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if len(fullBody) == 0 {
		return nil
	}

	resp.Header.Del("Transfer-Encoding")
	resp.Header.Del("Content-Length")
	resp.TransferEncoding = nil

	encoding := resp.Header.Get("Content-Encoding")
	var finalBody []byte

	if encoding == "" || encoding == "identity" {
		finalBody = fullBody
	} else {
		var compressedBuffer bytes.Buffer
		var writer io.WriteCloser

		switch encoding {
		case "gzip":
			writer = gzip.NewWriter(&compressedBuffer)
		case "deflate":
			writer = zlib.NewWriter(&compressedBuffer)
		case "br":
			writer = brotli.NewWriter(&compressedBuffer)
		}
		if writer != nil {
			if _, err = writer.Write(fullBody); err != nil {
				return err
			}
			if err = writer.Close(); err != nil {
				return err
			}
			finalBody = compressedBuffer.Bytes()
		} else {
			finalBody = fullBody
		}
	}

	resp.Body = io.NopCloser(bytes.NewReader(finalBody))
	updateContentLength(resp, len(finalBody))

	return nil
}

func updateContentLength(resp *http.Response, length int) {
	resp.ContentLength = int64(length)
	resp.Header.Set("Content-Length", strconv.Itoa(length))
	if length == 0 {
		resp.Header.Del("Content-Type")
	}
}
