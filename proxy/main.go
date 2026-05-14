package main

import (
	"hallmasterorg/hallmaster-proxy/internals"
	"hallmasterorg/hallmaster-proxy/internals/certs"
	"hallmasterorg/hallmaster-proxy/internals/config"
	"hallmasterorg/hallmaster-proxy/internals/dnsbypass"
	"hallmasterorg/hallmaster-proxy/internals/handlers"
	"hallmasterorg/hallmaster-proxy/internals/healthz"
	"hallmasterorg/hallmaster-proxy/internals/tamper"
	"log"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	c, err := certs.New(cfg.CACertPath, cfg.CAKeyPath)
	if err != nil {
		log.Fatalf("failed to init TLS certs: %v", err)
	}

	resolver := dnsbypass.NewExternalResolver(cfg.DNSServer, cfg.DNSTimeout)

	healthz.ListenAndServe(cfg.HealthPort)

	p := internals.NewMITMProxy(cfg, c, tamper.Logging{}, resolver)
	p.Listen(handlers.HttpsHandler)
}
