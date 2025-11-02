package utils

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"strconv"

	"github.com/andybalholm/brotli"
)

func EncodeHttpResponse(resp *http.Response) error {
	if resp == nil || resp.Request == nil || resp.Body == nil {
		return nil
	}

	contentType := resp.Header.Get("Content-Type")
	if isBinary(contentType) {
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

	if encoding == "" || encoding=="identity" {
		finalBody=fullBody
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
			_, err = writer.Write(fullBody)
			if err != nil {
				return err
			}
			writer.Close()
			finalBody=compressedBuffer.Bytes()
		} else {
			finalBody=fullBody
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
