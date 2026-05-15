package handlers

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"hallmasterorg/hallmaster-proxy/internals"
	"hallmasterorg/hallmaster-proxy/internals/httpio"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

const maxConnectDepth = 4

var discordHostSuffixes = []string{"discord.com", "discord.gg", "gateway.discord.gg"}

// isDiscordHost reports whether `hostHeader` (an HTTP Host header, possibly
// "host:port") points at one of the Discord hostnames the proxy is meant to
// intercept. Matches are exact or one-level-deep subdomain ("foo.discord.gg"
// matches; "evil-discord.com.attacker.io" does not).
func isDiscordHost(hostHeader string) bool {
	host := hostHeader
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, s := range discordHostSuffixes {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	return false
}

// HttpsHandler is the entry point passed to internals.MITMProxy.Listen. It
// reads HTTPS requests off `clientTLS`, optionally tampers them via the
// Tamperer in deps, forwards them upstream, then mirrors the response back.
//
// One upstream TLS connection is kept per (client TLS session, originalHost);
// if a subsequent request on the same client session targets a different
// host, the upstream is closed and redialled. Nested CONNECTs are handled
// iteratively, capped at maxConnectDepth.
func HttpsHandler(deps internals.HandlerDeps, clientTLS *tls.Conn) {
	clientReader := bufio.NewReader(clientTLS)
	logger := deps.Logger

	var serverTLS *tls.Conn
	var serverReader *bufio.Reader
	var dialledHost string
	connectDepth := 0

	defer func() {
		if serverTLS != nil {
			serverTLS.Close()
		}
	}()

	cfg := deps.Cfg
	tamperer := deps.Tamperer
	resolver := deps.Resolver
	proxyHostPort := deps.ProxyHostPort
	cleanProxyHost := deps.CleanHostname

	for {
		req, err := http.ReadRequest(clientReader)
		if err != nil {
			if err != io.EOF {
				logger.Warn("read request over TLS", "err", err)
			}
			return
		}

		isRelay := req.Host == proxyHostPort || req.Host == cleanProxyHost
		shouldIntercept := isRelay || isDiscordHost(req.Host)

		if req.Method == http.MethodConnect {
			if connectDepth >= maxConnectDepth {
				logger.Warn("nested CONNECT depth exceeded", "host", req.Host, "depth", connectDepth)
				return
			}
			logger.Info("nested CONNECT, upgrading", "host", req.Host)

			fmt.Fprintf(clientTLS, "HTTP/1.1 200 Connection established\r\n\r\n")

			nestedTLS, err := deps.Handshaker.Handshake(clientTLS, req.Host)
			if err != nil {
				logger.Error("nested TLS handshake", "err", err, "host", req.Host)
				return
			}

			// The new CONNECT targets a fresh host — drop any upstream
			// dialled for the outer session.
			if serverTLS != nil {
				_ = serverTLS.Close()
				serverTLS, serverReader = nil, nil
				dialledHost = ""
			}
			clientTLS = nestedTLS
			clientReader = bufio.NewReader(clientTLS)
			connectDepth++
			continue
		}

		originalHost := req.Host
		targetHost := originalHost
		if isDiscordHost(targetHost) {
			ctx, cancel := context.WithTimeout(req.Context(), cfg.DNSTimeout)
			realAddr, err := resolver.Resolve(ctx, targetHost)
			cancel()
			if err == nil {
				logger.Debug("dns bypass redirect", "host", targetHost, "addr", realAddr)
				targetHost = realAddr
			}
		}

		// One upstream TLS conn per (client session, originalHost). On
		// host change, close and redial — the proxy does not multiplex
		// pipelined requests across hosts on a single client session.
		if serverTLS != nil && originalHost != dialledHost {
			logger.Info("host changed on pipelined session, redialing",
				"from", dialledHost, "to", originalHost)
			_ = serverTLS.Close()
			serverTLS, serverReader = nil, nil
		}

		if serverTLS == nil {
			var upstreamCfg *tls.Config
			if deps.UpstreamTLSConfig != nil {
				upstreamCfg = deps.UpstreamTLSConfig.Clone()
			} else {
				upstreamCfg = &tls.Config{}
			}
			upstreamCfg.ServerName = originalHost
			upstreamCfg.NextProtos = []string{"http/1.1"}

			dialCtx, dialCancel := context.WithTimeout(req.Context(), cfg.UpstreamDialTimeout)
			if deps.DialUpstream != nil {
				serverTLS, err = deps.DialUpstream(dialCtx, "tcp", targetHost, upstreamCfg)
			} else {
				dialer := &net.Dialer{Timeout: cfg.UpstreamDialTimeout}
				serverTLS, err = tls.DialWithDialer(dialer, "tcp", targetHost, upstreamCfg)
			}
			dialCancel()
			if err != nil {
				logger.Error("dial upstream", "err", err, "addr", targetHost)
				return
			}
			serverReader = bufio.NewReader(serverTLS)
			dialledHost = originalHost
		}

		if !shouldIntercept {
			logger.Debug("passthrough", "host", req.Host)

			if err := req.Write(serverTLS); err != nil {
				logger.Error("passthrough write", "err", err, "host", req.Host)
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

		logger.Debug("http req", "method", req.Method, "url", req.URL.String())

		finalRequest := req
		if tampered, err := tamperer.Request(req); err != nil {
			logger.Warn("tamper request", "err", err)
		} else {
			finalRequest = tampered
		}

		if err = finalRequest.Write(serverTLS); err != nil {
			logger.Error("write request to upstream", "err", err)
			return
		}

		resp, err := http.ReadResponse(serverReader, finalRequest)
		if err != nil {
			logger.Error("read response from upstream", "err", err)
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
			logger.Info("ws upgrade", "host", targetHost)
			resp.Write(clientTLS)
			closeResp()
			isCompressed := strings.Contains(finalRequest.URL.RawQuery, "compress=zlib-stream")
			if isCompressed {
				logger.Info("ws zlib-stream compression", "host", finalRequest.Host)
			}
			InspectWS(logger, clientTLS, clientReader, serverTLS, serverReader, proxyHostPort, isCompressed, tamperer)
			return
		}

		decodedBody, derr := httpio.DecodeBody(resp)
		if derr != nil {
			logger.Warn("decode response body for tamperer", "err", derr)
		}

		finalResponse := resp
		if tampered, err := tamperer.Response(finalRequest, resp, decodedBody); err != nil {
			logger.Warn("tamper response", "err", err)
		} else {
			finalResponse = tampered
		}

		httpio.Encode(finalResponse)
		if err = finalResponse.Write(clientTLS); err != nil {
			logger.Error("write response to client", "err", err)
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
