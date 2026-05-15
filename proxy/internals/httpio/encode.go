package httpio

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
)

// Encode normalises the framing of `resp` so http.Response.Write produces
// clean Content-Length-based output. It does NOT touch the body bytes:
// the response is forwarded with the same on-the-wire encoding the
// upstream sent (matching Content-Encoding).
//
// This pairs with DecodeBody (which is read-only on resp.Body / headers):
// observers get the decoded payload as a separate []byte; the bot gets
// the original encoded bytes verbatim. A Tamperer that rewrites the body
// is responsible for keeping `resp.Body` and `Content-Encoding` mutually
// consistent.
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

	resp.Header.Del("Transfer-Encoding")
	resp.TransferEncoding = nil

	resp.Body = io.NopCloser(bytes.NewReader(fullBody))
	resp.ContentLength = int64(len(fullBody))
	resp.Header.Set("Content-Length", strconv.Itoa(len(fullBody)))
	if len(fullBody) == 0 {
		resp.Header.Del("Content-Type")
	}

	return nil
}
