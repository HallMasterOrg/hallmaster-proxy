package middlewares

import (
	"bytes"
	"fmt"
	"hallmasterorg/hallmaster-proxy/internals/utils"
	"io"
	"log"
	"net/http"
	"strings"
)

func LogRequest(r *http.Request) {
	body := "<empty>"
	if r.Body != nil {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1024))
		_ = r.Body.Close()

		r.Body = io.NopCloser(bytes.NewReader(b))
		if len(b) > 0 {
			body = fmt.Sprintf("%q", b)
		}
	}
	log.Printf("--> %s %s %s (body %s)", r.Method, r.URL.String(), r.Proto, body)
}

func LogResponse(resp *http.Response) {
	if resp == nil || resp.Request == nil {
		return
	}

	bodyPreview, err := utils.DecodeHttpResponse(resp)
	if err != nil {
		log.Printf("Response body decoding error: %v", err)
		return
	}

	if len(bodyPreview) > 2048 {
		bodyPreview = bodyPreview[:2048] + "... [truncated]"
	}
	bodyPreview = strings.ReplaceAll(bodyPreview, "\n", " ")

	log.Printf("<-- %s %s (status %s, body %s)",
		resp.Request.Method,
		resp.Request.URL.String(),
		resp.Status,
		bodyPreview,
	)
}
