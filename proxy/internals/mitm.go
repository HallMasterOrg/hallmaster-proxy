package internals

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"hallmasterorg/hallmaster-proxy/internals/certs"
	"hallmasterorg/hallmaster-proxy/internals/config"
	"hallmasterorg/hallmaster-proxy/internals/dnsbypass"
	"hallmasterorg/hallmaster-proxy/internals/tamper"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync/atomic"
)

// Handshaker is the subset of MITMProxy exposed to handlers so they can
// upgrade nested CONNECT tunnels into TLS. Handlers depend on this
// interface, never on the concrete *MITMProxy.
type Handshaker interface {
	Handshake(conn net.Conn, host string) (*tls.Conn, error)
}

// HandlerDeps is the value handed to every handler invocation. It is built
// once in main and passed through Listen/Serve so handlers can be tested in
// isolation by constructing a HandlerDeps with stub implementations of
// Tamperer, Resolver, Handshaker, and the optional upstream hooks.
type HandlerDeps struct {
	Cfg           *config.Config
	Tamperer      tamper.Tamperer
	Resolver      dnsbypass.Resolver
	Handshaker    Handshaker
	ProxyHostPort string
	CleanHostname string
	Logger        *slog.Logger

	// UpstreamTLSConfig, when non-nil, is cloned and used as the base TLS
	// config for upstream dials. ServerName + NextProtos are still set per
	// request (so RootCAs / InsecureSkipVerify / MinVersion / cipher
	// preferences travel through unchanged). nil means "default dial"
	// (system trust store, http/1.1 ALPN).
	UpstreamTLSConfig *tls.Config

	// DialUpstream, when non-nil, is used to obtain the upstream TLS
	// connection. nil falls back to tls.DialWithDialer with a net.Dialer
	// honouring cfg.UpstreamDialTimeout. Tests use this to redirect dials
	// to an httptest.NewTLSServer or a net.Pipe-backed fixture.
	DialUpstream func(ctx context.Context, network, addr string, cfg *tls.Config) (*tls.Conn, error)
}

type bufferedConn struct {
	*bufio.Reader
	net.Conn
}

func (b *bufferedConn) Read(p []byte) (int, error) {
	return b.Reader.Read(p)
}

// MITMProxy listens for client traffic and terminates TLS using on-the-fly
// leaf certificates signed by its CA. It is a pure listener + Handshaker;
// per-connection collaborators (tamperer, resolver, hostnames, logger,
// upstream hooks) travel through HandlerDeps.
type MITMProxy struct {
	cfg   *config.Config
	certs *certs.MITMProxyCerts
	ready atomic.Bool
}

// NewMITMProxy constructs a MITMProxy.
func NewMITMProxy(cfg *config.Config, c *certs.MITMProxyCerts) *MITMProxy {
	return &MITMProxy{cfg: cfg, certs: c}
}

// Ready reports whether the proxy is currently accepting connections.
// It flips to true once Serve enters its accept loop and stays true until
// the listener is closed.
func (p *MITMProxy) Ready() bool { return p.ready.Load() }

// Listen opens a TCP listener on 0.0.0.0:<cfg.Port> and serves it forever
// using context.Background. It is the production entry point; tests should
// call Serve directly with their own listener and context.
func (p *MITMProxy) Listen(deps HandlerDeps, handler func(deps HandlerDeps, client *tls.Conn)) {
	ln, err := net.Listen("tcp", "0.0.0.0:"+p.cfg.Port)
	if err != nil {
		deps.Logger.Error("tcp listen", "err", err)
		os.Exit(1)
	}
	deps.Logger.Info("listening", "addr", ln.Addr().String())
	_ = p.Serve(context.Background(), ln, deps, handler)
}

// Serve runs the accept loop on `ln` until `ctx` is cancelled or `ln`
// returns a non-recoverable error. When `ctx` is cancelled, the listener
// is closed and Serve returns nil. In-flight handler goroutines are NOT
// force-cancelled — the proxy does not interrupt active client TLS
// sessions mid-request.
func (p *MITMProxy) Serve(ctx context.Context, ln net.Listener, deps HandlerDeps, handler func(deps HandlerDeps, client *tls.Conn)) error {
	p.ready.Store(true)
	defer p.ready.Store(false)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			deps.Logger.Warn("accept", "err", err)
			continue
		}
		go p.handleHTTPConn(deps, conn, handler)
	}
}

func (p *MITMProxy) handleHTTPConn(deps HandlerDeps, client net.Conn, handler func(deps HandlerDeps, client *tls.Conn)) {
	defer client.Close()

	deps.Logger.Debug("new client connection", "remote", client.RemoteAddr().String())

	br := bufio.NewReader(client)

	peek, err := br.Peek(1)
	if err != nil {
		deps.Logger.Warn("peek client", "err", err)
		return
	}

	bConn := &bufferedConn{Reader: br, Conn: client}

	if peek[0] == 0x16 {
		deps.Logger.Debug("direct TLS connection detected")
		p.handleDirectTLS(deps, bConn, "discord.com", handler)
		return
	}

	req, err := http.ReadRequest(br)
	if err != nil {
		deps.Logger.Warn("parse initial request", "err", err)
		return
	}
	if req.Method == http.MethodConnect {
		p.handleConnect(deps, bConn, req.Host, handler)
		return
	}

	deps.Logger.Warn("refusing non-TLS/non-CONNECT request",
		"remote", client.RemoteAddr().String(), "method", req.Method, "host", req.Host)
}

func (p *MITMProxy) handleConnect(deps HandlerDeps, bConn net.Conn, hostname string, handler func(deps HandlerDeps, client *tls.Conn)) {
	fmt.Fprintf(bConn, "HTTP/1.1 200 Connection established\r\n\r\n")
	p.handleDirectTLS(deps, bConn, hostname, handler)
}

// Handshake terminates TLS for a single CONNECT'd or directly-dialled
// connection, presenting a freshly-minted leaf cert for the requested host.
func (p *MITMProxy) Handshake(conn net.Conn, host string) (*tls.Conn, error) {
	tlsConfig := &tls.Config{
		GetCertificate: func(chi *tls.ClientHelloInfo) (*tls.Certificate, error) {
			h := chi.ServerName
			if h == "" {
				h = host
			}
			return p.certs.GetOrCreateCert(h)
		},
	}
	tlsConn := tls.Server(conn, tlsConfig)
	if err := tlsConn.Handshake(); err != nil {
		return nil, err
	}
	return tlsConn, nil
}

func (p *MITMProxy) handleDirectTLS(deps HandlerDeps, bConn net.Conn, hostname string, handler func(deps HandlerDeps, client *tls.Conn)) {
	tlsConn, err := p.Handshake(bConn, hostname)
	if err != nil {
		deps.Logger.Warn("initial TLS handshake", "err", err, "hostname", hostname)
		return
	}
	handler(deps, tlsConn)
}
