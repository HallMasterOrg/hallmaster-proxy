package handlers

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"hallmasterorg/hallmaster-proxy/internals"
	"hallmasterorg/hallmaster-proxy/internals/middlewares"
	"hallmasterorg/hallmaster-proxy/internals/utils"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
)

func HttpsHandler(clientTLS *tls.Conn, hs internals.Handshaker, proxyHostname string) {
	clientReader := bufio.NewReader(clientTLS)

	var serverTLS *tls.Conn
    var serverReader *bufio.Reader

	defer func() {
		if serverTLS != nil {
			serverTLS.Close()
		}
	}()

    gatewayRegex := regexp.MustCompile(`(?i)\/api\/v\d+\/gateway(?:\/bot)?`)

	for {
		req, err := http.ReadRequest(clientReader)
		if err != nil {
			if err != io.EOF {
				log.Printf("read request over TLS: %v", err)
			}
			return
		}

        cleanProxyHost := strings.Split(proxyHostname, ":")[0]
		isRelay := req.Host == proxyHostname || req.Host == cleanProxyHost
        shouldIntercept := isRelay || strings.Contains(req.Host, "discord.com") || strings.Contains(req.Host, "gateway.discord.gg")

		if req.Method == http.MethodConnect {
            log.Printf("Internal CONNECT intercepted for %s. Upgrading to nested TLS...", req.Host)

            fmt.Fprintf(clientTLS, "HTTP/1.1 200 Connection established\r\n\r\n")

            nestedTLS, err := hs.Handshake(clientTLS, req.Host)
            if err != nil {
                log.Printf("Nested TLS handshake failed: %v", err)
                return
            }

            HttpsHandler(nestedTLS, hs, proxyHostname)
            return
        }

        originalHost := req.Host
		targetHost := originalHost
        if strings.Contains(targetHost, "discord.com") || strings.Contains(targetHost, "gateway.discord.gg") {
            realAddr, err := utils.ResolveExternal(targetHost)
            if err == nil {
                log.Printf("[DNS-BYPASS] Redirecting %s -> %s", targetHost, realAddr)
                targetHost = realAddr
            }
        }

		if serverTLS == nil {
            upstreamCfg := &tls.Config{
                ServerName: originalHost,
                NextProtos: []string{"http/1.1"},
            }
            serverTLS, err = tls.Dial("tcp", targetHost, upstreamCfg)

            if err != nil {
                log.Printf("dial upstream %s: %v", targetHost, err)
                return
            }
            serverReader = bufio.NewReader(serverTLS)
        }

        if !shouldIntercept {
            log.Printf("[PASSTHROUGH] %s -> Blind forwarding", req.Host)

            if err := req.Write(serverTLS); err != nil {
                log.Printf("passthrough towards %s failed: %v", req.Host, err)
                return
            }

            errChan := make(chan error, 2)
			go func() { io.Copy(serverTLS, io.MultiReader(clientReader, clientTLS)); errChan <- nil }()
			go func() { io.Copy(clientTLS, io.MultiReader(serverReader, serverTLS)); errChan <- nil }()
			<-errChan
            return
        }

		log.Printf("Read HTTPS request: %s %s", req.Method, req.URL.String())
        middlewares.LogRequest(req)

        var finalRequest *http.Request
        tamperedRequest, err := middlewares.TamperRequest(req)
        if err != nil {
            log.Printf("An error occured while tampering the request: %v", err)
            log.Printf("Forwarding the non-tampered request")
            finalRequest = req
        } else {
            finalRequest = tamperedRequest
        }

        if err = finalRequest.Write(serverTLS); err != nil {
            log.Printf("Failed to write request to upstream: %v", err)
            return
        }

        resp, err := http.ReadResponse(serverReader, finalRequest)
        if err != nil {
            log.Printf("Error reading response from upstream: %v", err)
            return
        }

        if utils.IsWebsocketUpgrade(finalRequest) && resp.StatusCode == http.StatusSwitchingProtocols {
            log.Printf("WebSocket upgrade for %s -> switching to raw tunnel", targetHost)
			resp.Write(clientTLS)
            isCompressed := false
            if strings.Contains(finalRequest.URL.RawQuery, "compress=zlib-stream") {
                isCompressed = true
                log.Printf("[WS] Zlib-stream compression detected for %s", finalRequest.Host)
            }
            InspectWS(clientTLS, clientReader, serverTLS, serverReader, proxyHostname, isCompressed)
            return
        }

		if gatewayRegex.MatchString(finalRequest.URL.Path) && resp.StatusCode == 200 {
            log.Printf("[TAMPER] Modifying gateway response for bot")
            utils.TamperGatewayResponse(resp, proxyHostname)
        }

        middlewares.LogResponse(resp)

        tamperedResponse, err := middlewares.TamperResponse(finalRequest, resp)
        var finalResponse *http.Response
        if err != nil {
            log.Printf("An error occured while tampering the response: %v", err)
            log.Printf("Forwarding the original response")
            finalResponse=resp
        } else {
            finalResponse=tamperedResponse
        }

		utils.EncodeHttpResponse(finalResponse)
        if err = finalResponse.Write(clientTLS); err != nil {
            log.Printf("Error writing response to client: %v", err)
            return
        }

        finalResponse.Body.Close()
	}
}
