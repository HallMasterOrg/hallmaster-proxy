package utils

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"strings"

	"github.com/andybalholm/brotli"
)

func DecodeHttpResponse(resp *http.Response) (string, error) {
	if resp == nil || resp.Request == nil {
		return "<empty>", nil
	}

	contentType := resp.Header.Get("Content-Type")
	if isBinary(contentType) {
		return "<blob media>", nil
	}

	if resp.Body ==nil{
		return "<empty>",nil
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

	if reader == nil {
		return "<unable to decode>", nil
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

func isBinary(contentType string) bool {
	ct := strings.ToLower(contentType)
	binaryTypes := []string{"image/", "video/", "audio/", "application/octet-stream", "application/zip", "font/"}
	for _, t := range binaryTypes {
		if strings.Contains(ct, t) {
			return true
		}
	}
	return false
}
