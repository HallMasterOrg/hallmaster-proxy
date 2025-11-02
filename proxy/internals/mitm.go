package internals

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"hallmasterorg/hallmaster-proxy/internals/certs"
	"log"
	"net"
	"net/http"
)

type HttpConnHandler func(client net.Conn, req *http.Request, transport *http.Transport)
type HttpsConnHandler func(client *tls.Conn, hs Handshaker, proxyHostname string)
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

type MITMProxy struct {
	hostname string
	port string

	certs *certs.MITMProxyCerts

	httpConnHandler  HttpConnHandler
	httpsConnHandler HttpsConnHandler

	transport *http.Transport
}

func NewMITMProxy(hostname string, port string, certs *certs.MITMProxyCerts, httpsConnHandler HttpsConnHandler) (*MITMProxy, error) {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		Proxy:           http.ProxyFromEnvironment,
	}

	return &MITMProxy{
		hostname: hostname,
		port: port,
		certs: certs,
		httpsConnHandler: httpsConnHandler,
		transport: tr,
	}, nil
}

func (p *MITMProxy) Listen() {
	ln, err := net.Listen("tcp", "0.0.0.0:" + p.port)
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
		go p.handleHTTPConn(conn)
	}
}

func (p *MITMProxy) handleHTTPConn(client net.Conn) {
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
        p.handleDirectTLS(bConn, "discord.com")
        return
    }

	req, err := http.ReadRequest(br)
	if err != nil {
		log.Printf("Error parsing initial request: %v", err)
		return
	}
	if req.Method == http.MethodConnect {
        p.handleConnect(bConn, req.Host)
        return
    }

	req.URL.Scheme = "http"
	req.URL.Host = req.Host
	req.RequestURI = ""

	log.Printf("Received request for host: %s, method: %s", req.Host, req.Method)
	p.httpConnHandler(bConn, req, p.transport)
}

func (p *MITMProxy) handleConnect(bConn net.Conn, hostname string) {
    fmt.Fprintf(bConn, "HTTP/1.1 200 Connection established\r\n\r\n")

    p.handleDirectTLS(bConn, hostname)
}

func (p *MITMProxy) Handshake(conn net.Conn, host string) (*tls.Conn, error) {
    tlsConfig := &tls.Config{
        GetCertificate: func(chi *tls.ClientHelloInfo) (*tls.Certificate, error) {
            h := chi.ServerName
            if h == "" { h = host }
            return p.certs.GetOrCreateCert(h)
        },
    }
    tlsConn := tls.Server(conn, tlsConfig)
    if err := tlsConn.Handshake(); err != nil {
        return nil, err
    }
    return tlsConn, nil
}

func (p *MITMProxy) handleDirectTLS(bConn net.Conn, hostname string) {
    tlsConn, err := p.Handshake(bConn, hostname)
    if err != nil {
        log.Printf("Initial TLS handshake failed: %v", err)
        return
    }

	p.httpsConnHandler(tlsConn, p, p.hostname + ":" + p.port)
}
