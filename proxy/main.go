package main

import (
	"hallmasterorg/hallmaster-proxy/internals"
	"hallmasterorg/hallmaster-proxy/internals/certs"
	"hallmasterorg/hallmaster-proxy/internals/config"
	"hallmasterorg/hallmaster-proxy/internals/dnsbypass"
	"hallmasterorg/hallmaster-proxy/internals/handlers"
	"hallmasterorg/hallmaster-proxy/internals/healthz"
	"hallmasterorg/hallmaster-proxy/internals/tamper"
	"log/slog"
	"os"
)

// stressOverrides is set by main_stress.go when built with `-tags stress`.
// It swaps in the measurement Tamperer and points upstream at robojs-mock.
var stressOverrides func(*internals.HandlerDeps)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("load config", "err", err)
		os.Exit(1)
	}

	c, err := certs.New(cfg.CACertPath, cfg.CAKeyPath)
	if err != nil {
		logger.Error("init TLS certs", "err", err)
		os.Exit(1)
	}

	resolver := dnsbypass.NewExternalResolver(cfg.DNSServer, cfg.DNSTimeout)

	p := internals.NewMITMProxy(cfg, c)
	healthz.ListenAndServe(cfg.HealthPort, logger, p.Ready)

	deps := internals.HandlerDeps{
		Cfg:           cfg,
		Tamperer:      tamper.Logging{Logger: logger, LogBodies: cfg.LogBodies},
		Resolver:      resolver,
		Handshaker:    p,
		ProxyHostPort: cfg.Hostname + ":" + cfg.Port,
		CleanHostname: cfg.Hostname,
		Logger:        logger,
	}
	if stressOverrides != nil {
		stressOverrides(&deps)
	}
	p.Listen(deps, handlers.HttpsHandler)
}
