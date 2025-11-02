package main

import (
	"hallmasterorg/hallmaster-proxy/internals"
	"hallmasterorg/hallmaster-proxy/internals/certs"
	"hallmasterorg/hallmaster-proxy/internals/handlers"
	"log"
	"os"
)

var (
	httpPort   = os.Getenv("PROXY_HTTP_PORT")  // 8080
	httpHostname   = os.Getenv("HOSTNAME")  // hallmaster-proxy
	caCertPath = os.Getenv("PROXY_SSL_CA_CERT_PATH")
	caKeyPath  = os.Getenv("PROXY_SSL_CA_KEY_PATH")
)

func main() {
	c, err := certs.New(caCertPath, caKeyPath)
	if err != nil {
		log.Fatalf("failed to init TLS certs: %v", err)
	}

	p, err := internals.NewMITMProxy(httpHostname, httpPort, c, handlers.HttpsHandler)
	if err != nil {
		log.Fatalf("failed to init proxy: %v", err)
	}

	p.Listen()
}
