package handlers

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"hallmasterorg/hallmaster-proxy/internals"
	"hallmasterorg/hallmaster-proxy/internals/httpio"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
)

// HttpsHandler is the entry point passed to internals.MITMProxy.Listen. It
// reads HTTPS requests off `clientTLS`, optionally tampers them via the
// proxy's Tamperer, forwards them upstream, then mirrors the response back.
func HttpsHandler(p *internals.MITMProxy, clientTLS *tls.Conn) {
	clientReader := bufio.NewReader(clientTLS)

	var serverTLS *tls.Conn
	var serverReader *bufio.Reader

	defer func() {
		if serverTLS != nil {
			serverTLS.Close()
		}
	}()

	cfg := p.Cfg()
	tamperer := p.Tamperer()
	resolver := p.Resolver()
	proxyHostPort := p.ProxyHostPort()
	cleanProxyHost := p.CleanHostname()

	for {
		req, err := http.ReadRequest(clientReader)
		if err != nil {
			if err != io.EOF {
				log.Printf("read request over TLS: %v", err)
			}
			return
		}

		isRelay := req.Host == proxyHostPort || req.Host == cleanProxyHost
		shouldIntercept := isRelay || strings.Contains(req.Host, "discord.com") || strings.Contains(req.Host, "gateway.discord.gg")

		if req.Method == http.MethodConnect {
			log.Printf("Internal CONNECT intercepted for %s. Upgrading to nested TLS...", req.Host)

			fmt.Fprintf(clientTLS, "HTTP/1.1 200 Connection established\r\n\r\n")

			nestedTLS, err := p.Handshake(clientTLS, req.Host)
			if err != nil {
				log.Printf("Nested TLS handshake failed: %v", err)
				return
			}

			HttpsHandler(p, nestedTLS)
			return
		}

		originalHost := req.Host
		targetHost := originalHost
		if strings.Contains(targetHost, "discord.com") || strings.Contains(targetHost, "gateway.discord.gg") {
			ctx, cancel := context.WithTimeout(req.Context(), cfg.DNSTimeout)
			realAddr, err := resolver.Resolve(ctx, targetHost)
			cancel()
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
			dialer := &net.Dialer{Timeout: cfg.UpstreamDialTimeout}
			serverTLS, err = tls.DialWithDialer(dialer, "tcp", targetHost, upstreamCfg)
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

			var wg sync.WaitGroup
			wg.Add(2)
			closeBoth := func() {
				_ = serverTLS.Close()
				_ = clientTLS.Close()
			}
			go func() {
				defer wg.Done()
				defer closeBoth()
				_, _ = io.Copy(serverTLS, clientReader)
			}()
			go func() {
				defer wg.Done()
				defer closeBoth()
				_, _ = io.Copy(clientTLS, serverReader)
			}()
			wg.Wait()
			return
		}

		log.Printf("Read HTTPS request: %s %s", req.Method, req.URL.String())

		finalRequest := req
		if tampered, err := tamperer.Request(req); err != nil {
			log.Printf("An error occured while tampering the request: %v - forwarding original", err)
		} else {
			finalRequest = tampered
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

		closed := false
		closeResp := func() {
			if !closed {
				resp.Body.Close()
				closed = true
			}
		}

		if isWebsocketUpgrade(finalRequest) && resp.StatusCode == http.StatusSwitchingProtocols {
			log.Printf("WebSocket upgrade for %s -> switching to raw tunnel", targetHost)
			resp.Write(clientTLS)
			closeResp()
			isCompressed := strings.Contains(finalRequest.URL.RawQuery, "compress=zlib-stream")
			if isCompressed {
				log.Printf("[WS] Zlib-stream compression detected for %s", finalRequest.Host)
			}
			InspectWS(clientTLS, clientReader, serverTLS, serverReader, proxyHostPort, isCompressed, tamperer)
			return
		}

		finalResponse := resp
		if tampered, err := tamperer.Response(finalRequest, resp); err != nil {
			log.Printf("An error occured while tampering the response: %v - forwarding original", err)
		} else {
			finalResponse = tampered
		}

		httpio.Encode(finalResponse)
		if err = finalResponse.Write(clientTLS); err != nil {
			log.Printf("Error writing response to client: %v", err)
			closeResp()
			return
		}

		closeResp()
	}
}

// isWebsocketUpgrade reports whether `req` is requesting a WebSocket upgrade
// per RFC 6455 (both the Connection and Upgrade headers must be present).
func isWebsocketUpgrade(req *http.Request) bool {
	return strings.Contains(strings.ToLower(req.Header.Get("Connection")), "upgrade") &&
		strings.EqualFold(req.Header.Get("Upgrade"), "websocket")
}
