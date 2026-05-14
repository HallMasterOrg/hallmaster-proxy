package internals

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"hallmasterorg/hallmaster-proxy/internals/certs"
	"hallmasterorg/hallmaster-proxy/internals/config"
	"hallmasterorg/hallmaster-proxy/internals/dnsbypass"
	"hallmasterorg/hallmaster-proxy/internals/tamper"
	"log"
	"net"
	"net/http"
)

// Handshaker is the subset of MITMProxy exposed to handlers so they can
// upgrade nested CONNECT tunnels into TLS.
type Handshaker interface {
	Handshake(conn net.Conn, host string) (*tls.Conn, error)
}

type bufferedConn struct {
	*bufio.Reader
	net.Conn
}

func (b *bufferedConn) Read(p []byte) (int, error) {
	return b.Reader.Read(p)
}

// MITMProxy listens for client traffic, terminates TLS using on-the-fly leaf
// certificates signed by its CA, and forwards intercepted traffic to the
// upstream. Handlers receive a Deps view with everything they need.
type MITMProxy struct {
	cfg           *config.Config
	certs         *certs.MITMProxyCerts
	tamperer      tamper.Tamperer
	resolver      dnsbypass.Resolver
	cleanHostname string
	proxyHostPort string
}

// NewMITMProxy constructs a MITMProxy. All collaborators are required: pass
// `tamper.Nop{}` for the default no-op tamperer.
func NewMITMProxy(cfg *config.Config, c *certs.MITMProxyCerts, t tamper.Tamperer, r dnsbypass.Resolver) *MITMProxy {
	return &MITMProxy{
		cfg:           cfg,
		certs:         c,
		tamperer:      t,
		resolver:      r,
		cleanHostname: cfg.Hostname,
		proxyHostPort: cfg.Hostname + ":" + cfg.Port,
	}
}

// Cfg exposes the proxy's resolved configuration to handlers.
func (p *MITMProxy) Cfg() *config.Config { return p.cfg }

// Tamperer returns the configured tamperer.
func (p *MITMProxy) Tamperer() tamper.Tamperer { return p.tamperer }

// Resolver returns the DNS resolver used to bypass the in-container resolver.
func (p *MITMProxy) Resolver() dnsbypass.Resolver { return p.resolver }

// ProxyHostPort returns "<hostname>:<port>" for relay-detection in handlers.
func (p *MITMProxy) ProxyHostPort() string { return p.proxyHostPort }

// CleanHostname returns the proxy hostname without a port suffix.
func (p *MITMProxy) CleanHostname() string { return p.cleanHostname }

// Listen accepts client connections on the configured port and dispatches
// each one to a goroutine.
func (p *MITMProxy) Listen(handler func(p *MITMProxy, client *tls.Conn)) {
	ln, err := net.Listen("tcp", "0.0.0.0:"+p.cfg.Port)
	if err != nil {
		log.Fatalf("HTTP listen: %v", err)
	}
	log.Printf("HTTP server listening on %s", ln.Addr())

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept http: %v", err)
			continue
		}
		go p.handleHTTPConn(conn, handler)
	}
}

func (p *MITMProxy) handleHTTPConn(client net.Conn, handler func(p *MITMProxy, client *tls.Conn)) {
	defer client.Close()

	log.Printf("New client connection from %s", client.RemoteAddr())

	br := bufio.NewReader(client)

	peek, err := br.Peek(1)
	if err != nil {
		log.Printf("Error peeking connection: %v", err)
		return
	}

	bConn := &bufferedConn{Reader: br, Conn: client}

	if peek[0] == 0x16 {
		log.Printf("Detected direct TLS connection")
		p.handleDirectTLS(bConn, "discord.com", handler)
		return
	}

	req, err := http.ReadRequest(br)
	if err != nil {
		log.Printf("Error parsing initial request: %v", err)
		return
	}
	if req.Method == http.MethodConnect {
		p.handleConnect(bConn, req.Host, handler)
		return
	}

	log.Printf("Refusing non-TLS/non-CONNECT request from %s: %s %s", client.RemoteAddr(), req.Method, req.Host)
}

func (p *MITMProxy) handleConnect(bConn net.Conn, hostname string, handler func(p *MITMProxy, client *tls.Conn)) {
	fmt.Fprintf(bConn, "HTTP/1.1 200 Connection established\r\n\r\n")
	p.handleDirectTLS(bConn, hostname, handler)
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

func (p *MITMProxy) handleDirectTLS(bConn net.Conn, hostname string, handler func(p *MITMProxy, client *tls.Conn)) {
	tlsConn, err := p.Handshake(bConn, hostname)
	if err != nil {
		log.Printf("Initial TLS handshake failed: %v", err)
		return
	}
	handler(p, tlsConn)
}
