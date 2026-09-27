package main

import "time"

const (
	modeGateway = "gateway" // inject MESSAGE_CREATE, time arrival on both gateway connections
	modeREST    = "rest"    // GET /api/v10/gateway/bot over both paths
)

// Scenario is one load profile, addressed by name: `./driver steady_1kmps`.
// Not covered here: compressed gateways (robojs-mock only speaks
// encoding=json without zlib-stream) and leaf-cert churn (see
// BenchmarkLeafIssue_* in proxy/internals/certs).
type Scenario struct {
	Name         string // filled from the map key
	Description  string
	Mode         string
	RatePerSec   int           // 0 = as fast as possible
	Duration     time.Duration // with RatePerSec: ops = rate × duration; also the run's time cap
	Total        int           // fixed op count (burst); overrides rate × duration
	PayloadBytes int           // approx MESSAGE_CREATE size (content is padded)
	Concurrency  int           // parallel op workers
}

var Scenarios = map[string]Scenario{
	"idle": {
		Description: "no traffic for 60s — baseline RSS, goroutine and FD counts",
		Mode:        modeGateway,
		Duration:    60 * time.Second,
	},
	"steady_100mps": {
		Description:  "100 MESSAGE_CREATE/s for 5 min — sustainable-rate sanity check",
		Mode:         modeGateway,
		RatePerSec:   100,
		Duration:     5 * time.Minute,
		PayloadBytes: 256,
		Concurrency:  4,
	},
	"steady_1kmps": {
		Description:  "1000 MESSAGE_CREATE/s for 60s",
		Mode:         modeGateway,
		RatePerSec:   1000,
		Duration:     60 * time.Second,
		PayloadBytes: 256,
		Concurrency:  16,
	},
	"burst_10k": {
		Description:  "10000 MESSAGE_CREATE as fast as possible — tail latency / backpressure",
		Mode:         modeGateway,
		Total:        10000,
		PayloadBytes: 256,
		Concurrency:  32,
	},
	"rest_200rps": {
		Description: "200 REST calls/s for 60s over keep-alive connections",
		Mode:        modeREST,
		RatePerSec:  200,
		Duration:    60 * time.Second,
		Concurrency: 16,
	},
}
