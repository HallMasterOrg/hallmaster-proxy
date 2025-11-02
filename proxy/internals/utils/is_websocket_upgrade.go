package utils

import (
	"net/http"
	"strings"
)

func IsWebsocketUpgrade(req *http.Request) bool {
	return strings.Contains(strings.ToLower(req.Header.Get("Connection")), "upgrade") &&
        strings.EqualFold(req.Header.Get("Upgrade"), "websocket")
}
