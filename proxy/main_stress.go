//go:build stress

package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"hallmasterorg/hallmaster-proxy/internals"
	"hallmasterorg/hallmaster-proxy/internals/tamper"
	"net/http"
	_ "net/http/pprof" // stress build only, loopback listener
	"os"
)

// The stress build differs from production through existing HandlerDeps
// seams:
//   - STRESS_TAMPERER=nop swaps tamper.Logging for tamper.Nop, to measure
//     the proxy without per-frame logging.
//   - Every Discord host resolves to STRESS_UPSTREAM_ADDR (robojs-mock's TLS
//     front) instead of real Discord, so load never reaches discord.com.
//   - Upstream cert verification is off: the mock's cert is self-signed.
//
// It also serves net/http/pprof on 127.0.0.1:6060 (inside the container
// only), e.g. for an execution trace under load:
//
//	docker exec hallmaster-proxy wget -qO /tmp/trace.out \
//	    'http://127.0.0.1:6060/debug/pprof/trace?seconds=10'
func init() {
	stressOverrides = func(d *internals.HandlerDeps) {
		upstream := os.Getenv("STRESS_UPSTREAM_ADDR")
		if upstream == "" {
			d.Logger.Error("STRESS_UPSTREAM_ADDR is required in the stress build")
			os.Exit(1)
		}
		if os.Getenv("STRESS_TAMPERER") == "nop" {
			d.Tamperer = tamper.Nop{}
		}
		d.Resolver = fixedResolver(upstream)
		d.UpstreamTLSConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // stress build only, upstream is a local mock
		go func() {
			d.Logger.Error("pprof server", "err", http.ListenAndServe("127.0.0.1:6060", nil))
		}()
		d.Logger.Info("stress build active", "upstream", upstream, "tamperer", fmt.Sprintf("%T", d.Tamperer))
	}
}

// fixedResolver sends every Discord host to one address.
type fixedResolver string

func (r fixedResolver) Resolve(context.Context, string) (string, error) { return string(r), nil }
