package utils

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

func TamperGatewayResponse(resp *http.Response, proxyHostname string) error {
	body, err := DecodeHttpResponse(resp)
	if err != nil {
		return err
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return err
	}

	// data["url"] = "wss://" + proxyHostname

	newBody, _ := json.Marshal(data)

	resp.Body = io.NopCloser(bytes.NewReader(newBody))
	updateContentLength(resp, len(newBody))

	return nil
}
