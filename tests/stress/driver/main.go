// Package main is the stress driver. It measures the proxy's added
// latency by running every operation over two paths at once and diffing
// them:
//
//	proxy:  driver ──TLS──▶ hallmaster-proxy ──TLS──▶ robojs-mock TLS front ─▶ mock
//	direct: driver ──TLS─────────────────────────────▶ robojs-mock TLS front ─▶ mock
//
// Both paths pay one TLS hop into the mock, so proxy_ns − direct_ns is the
// cost of the proxy alone. Nothing ever reaches the real Discord: the
// stress build of the proxy resolves every Discord host to the mock.
//
// Gateway scenarios: the driver acts as a bot shard with one gateway
// connection per path on the same mock session, injects MESSAGE_CREATE
// events through the mock's control API (which fans out to every
// connection), and timestamps each arrival.
//
// REST scenarios: each op does GET /api/v10/gateway/bot over both paths.
//
// CLI: ./driver <scenario-name>
//
//	env:
//	  STRESS_MOCK_URL      mock control API, plain HTTP (default http://hallmaster-robojs-mock:3000)
//	  STRESS_DIRECT_URL    mock TLS front, the no-proxy baseline (default https://hallmaster-robojs-mock:3443)
//	  STRESS_PROXY_URL     Discord origin as the bot sees it; DNS-aliased to the proxy (default https://discord.com)
//	  STRESS_CA_CERT       Root CA the proxy signs leaves with (default /ca.crt)
//	  STRESS_PROXY_NAME    proxy container name, for sampling + log capture (default hallmaster-proxy)
//	  STRESS_RESULTS_DIR   output directory (default /results)
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type env struct {
	mockURL, directURL, proxyURL string
	proxyTLS, directTLS          *tls.Config
}

func main() {
	if len(os.Args) < 2 {
		fatalf("usage: %s <scenario-name>", os.Args[0])
	}
	scn, ok := Scenarios[os.Args[1]]
	if !ok {
		fatalf("unknown scenario %q; known: %v", os.Args[1], scenarioNames())
	}
	scn.Name = os.Args[1]

	caPEM, err := os.ReadFile(envOr("STRESS_CA_CERT", "/ca.crt"))
	if err != nil {
		fatalf("read CA: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		fatalf("CA file holds no PEM certificate")
	}
	e := env{
		mockURL:   envOr("STRESS_MOCK_URL", "http://hallmaster-robojs-mock:3000"),
		directURL: envOr("STRESS_DIRECT_URL", "https://hallmaster-robojs-mock:3443"),
		proxyURL:  envOr("STRESS_PROXY_URL", "https://discord.com"),
		proxyTLS:  &tls.Config{RootCAs: pool},
		// The TLS front's cert is throwaway self-signed.
		directTLS: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // local mock only
	}
	proxyName := envOr("STRESS_PROXY_NAME", "hallmaster-proxy")
	runDir := fmt.Sprintf("%s/%s_%s", envOr("STRESS_RESULTS_DIR", "/results"), scn.Name, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		fatalf("mkdir results: %v", err)
	}
	logf("scenario=%s mode=%s rate=%d duration=%s total=%d concurrency=%d", scn.Name, scn.Mode, scn.RatePerSec, scn.Duration, scn.Total, scn.Concurrency)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	statsCSV, statsFile, err := openCSV(runDir + "/stats.csv")
	if err != nil {
		fatalf("open stats.csv: %v", err)
	}
	defer statsFile.Close()
	smp := newSampler(proxyName, statsCSV)
	samplerCtx, samplerCancel := context.WithCancel(ctx)
	samplerDone := make(chan struct{})
	go func() {
		defer close(samplerDone)
		smp.run(samplerCtx)
	}()

	logFrom := time.Now()
	rec, err := runScenario(ctx, scn, e)
	samplerCancel()
	<-samplerDone
	if err != nil {
		fatalf("%v", err)
	}

	// Capture before returning: run-stress.sh tears the stack down once
	// the driver exits, and the logs go with the container.
	logPath := runDir + "/proxy.log"
	if err := captureProxyLogs(context.Background(), proxyName, logFrom, logPath); err != nil {
		logf("warn: captureProxyLogs: %v", err)
	}
	if err := rec.writeCSV(runDir+"/latencies.csv", scn.Mode); err != nil {
		logf("warn: latencies.csv: %v", err)
	}
	writeSummary(runDir+"/summary.md", scn, rec, countStressRecords(logPath))
	logf("done; results in %s", runDir)
}

// recorder holds one slot per op. Times are ns since start (monotonic);
// 0 means "never happened". Slots are written from many goroutines, read
// once after the run.
type recorder struct {
	start          time.Time
	sent           []atomic.Int64
	direct, proxy  []atomic.Int64 // gateway: arrival time; REST: round-trip
	errs           []atomic.Pointer[string]
	started, ended time.Duration
	opErrors       atomic.Int64
}

func newRecorder(n int) *recorder {
	return &recorder{
		start:  time.Now(),
		sent:   make([]atomic.Int64, n),
		direct: make([]atomic.Int64, n),
		proxy:  make([]atomic.Int64, n),
		errs:   make([]atomic.Pointer[string], n),
	}
}

func (r *recorder) now() int64 { return int64(time.Since(r.start)) }

func (r *recorder) fail(i int, err error) {
	s := err.Error()
	r.errs[i].Store(&s)
	r.opErrors.Add(1)
}

// latency returns the per-op latency on one path, or 0 if it never
// completed. Gateway slots hold arrival times, REST slots round-trips.
func (r *recorder) latency(mode string, slot []atomic.Int64, i int) int64 {
	v := slot[i].Load()
	if v == 0 || mode == modeREST {
		return v
	}
	return v - r.sent[i].Load()
}

func runScenario(ctx context.Context, scn Scenario, e env) (*recorder, error) {
	control := &http.Client{Timeout: 30 * time.Second}
	sessionID, token, err := createSession(ctx, control, e.mockURL, scn)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	logf("session %s", sessionID)
	// The mock drops MESSAGE_CREATE above 10/s by default.
	if err := disableLoopProtection(ctx, control, e.mockURL, sessionID); err != nil {
		return nil, fmt.Errorf("disable loop protection: %w", err)
	}

	target := computeTarget(scn)
	rec := newRecorder(target)
	proxyHTTP := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: e.proxyTLS, MaxIdleConnsPerHost: max(scn.Concurrency, 1)}}
	directHTTP := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: e.directTLS, MaxIdleConnsPerHost: max(scn.Concurrency, 1)}}

	var op func(i int)
	switch scn.Mode {
	case modeGateway:
		// Like discord.js: ask /gateway/bot (through the proxy) where to connect.
		gwURL, err := gatewayURL(ctx, proxyHTTP, e.proxyURL, token)
		if err != nil {
			return nil, fmt.Errorf("gateway/bot via proxy: %w", err)
		}
		gwCtx, gwCancel := context.WithCancel(ctx)
		defer gwCancel()
		query := "/?v=10&encoding=json"
		// The mock fans out in connection order, so whichever connects
		// first gets each event a few µs earlier. Direct goes first: the
		// bias then inflates the measured overhead rather than hiding it.
		direct := "wss://" + strings.TrimPrefix(e.directURL, "https://") + query
		if err := connectGateway(gwCtx, direct, token, e.directTLS, func(i int) { store(rec.direct, i, rec.now()) }); err != nil {
			return nil, fmt.Errorf("direct gateway: %w", err)
		}
		if err := connectGateway(gwCtx, gwURL+query, token, e.proxyTLS, func(i int) { store(rec.proxy, i, rec.now()) }); err != nil {
			return nil, fmt.Errorf("proxy gateway: %w", err)
		}
		logf("gateway connected via proxy (%s) and direct (%s)", gwURL, direct)
		op = func(i int) {
			rec.sent[i].Store(rec.now())
			if err := dispatch(ctx, control, e.mockURL, sessionID, scn, i); err != nil {
				rec.fail(i, err)
			}
		}
	case modeREST:
		op = func(i int) {
			// Alternate which path goes first so neither always hits a
			// warmer mock.
			paths := []struct {
				c    *http.Client
				base string
				slot []atomic.Int64
			}{{directHTTP, e.directURL, rec.direct}, {proxyHTTP, e.proxyURL, rec.proxy}}
			if i%2 == 1 {
				paths[0], paths[1] = paths[1], paths[0]
			}
			rec.sent[i].Store(rec.now())
			for _, p := range paths {
				t0 := time.Now()
				if _, err := gatewayURL(ctx, p.c, p.base, token); err != nil {
					rec.fail(i, err)
					continue
				}
				p.slot[i].Store(int64(time.Since(t0)))
			}
		}
	}

	rec.started = time.Since(rec.start)
	if target == 0 { // idle
		select {
		case <-ctx.Done():
		case <-time.After(scn.Duration):
		}
		rec.ended = time.Since(rec.start)
		return rec, nil
	}
	runOps(ctx, scn, target, op)
	rec.ended = time.Since(rec.start)

	if scn.Mode == modeGateway {
		// Let in-flight events land before calling anything lost.
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) && !rec.allArrived() {
			time.Sleep(100 * time.Millisecond)
		}
	}
	return rec, nil
}

func store(slot []atomic.Int64, i int, v int64) {
	if i >= 0 && i < len(slot) {
		slot[i].CompareAndSwap(0, v)
	}
}

func (r *recorder) allArrived() bool {
	for i := range r.sent {
		if r.errs[i].Load() == nil && (r.direct[i].Load() == 0 || r.proxy[i].Load() == 0) {
			return false
		}
	}
	return true
}

// runOps calls op(0..target-1) from scn.Concurrency workers, paced at
// scn.RatePerSec (0 = as fast as possible) and capped at Duration+5s.
func runOps(ctx context.Context, scn Scenario, target int, op func(int)) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if scn.Duration > 0 {
		time.AfterFunc(scn.Duration+5*time.Second, cancel)
	}
	work := make(chan int, 1024)
	go func() {
		defer close(work)
		var tick <-chan time.Time
		if scn.RatePerSec > 0 {
			t := time.NewTicker(time.Second / time.Duration(scn.RatePerSec))
			defer t.Stop()
			tick = t.C
		}
		for i := range target {
			if tick != nil {
				select {
				case <-ctx.Done():
					return
				case <-tick:
				}
			}
			select {
			case <-ctx.Done():
				return
			case work <- i:
			}
		}
	}()
	var wg sync.WaitGroup
	for range max(scn.Concurrency, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				op(i)
			}
		}()
	}
	wg.Wait()
}

func computeTarget(scn Scenario) int {
	if scn.Total > 0 {
		return scn.Total
	}
	if scn.RatePerSec > 0 {
		return scn.RatePerSec * int(scn.Duration.Seconds())
	}
	return 0
}

// createSession asks robojs-mock for a new session; the token it returns
// is what the "bot" identifies with.
//
//	POST /mock/api/control/sessions {"name":"..."} -> {"session_id":"...","token":"..."}
func createSession(ctx context.Context, c *http.Client, mockURL string, scn Scenario) (id, token string, err error) {
	var out struct {
		SessionID string `json:"session_id"`
		Token     string `json:"token"`
	}
	if err := postJSON(ctx, c, mockURL+"/mock/api/control/sessions", map[string]any{"name": "stress-" + scn.Name}, &out); err != nil {
		return "", "", err
	}
	if out.SessionID == "" || out.Token == "" {
		return "", "", fmt.Errorf("mock returned empty session_id/token")
	}
	return out.SessionID, out.Token, nil
}

// disableLoopProtection turns off the mock's 10 MESSAGE_CREATE/s breaker.
// @robojs/server answers unknown routes with an empty 200, so the reply
// is checked to make sure the route really ran.
//
//	POST /mock/api/control/sessions/:id/loop-protection {"enabled":false}
func disableLoopProtection(ctx context.Context, c *http.Client, mockURL, sessionID string) error {
	var out struct {
		Success *bool `json:"success"`
		Enabled *bool `json:"enabled"`
	}
	url := fmt.Sprintf("%s/mock/api/control/sessions/%s/loop-protection", mockURL, sessionID)
	if err := postJSON(ctx, c, url, map[string]any{"enabled": false}, &out); err != nil {
		return err
	}
	if out.Success == nil || out.Enabled == nil || *out.Enabled {
		return fmt.Errorf("unexpected reply (route missing or refused)")
	}
	return nil
}

// dispatch injects one MESSAGE_CREATE into the session. A mentions entry
// with a username takes the mock's raw-dispatch branch: no channel-state
// validation, payload goes straight to every gateway connection.
//
//	POST /mock/api/control/sessions/:id/dispatch {"event":"MESSAGE_CREATE","data":{...}}
func dispatch(ctx context.Context, c *http.Client, mockURL, sessionID string, scn Scenario, i int) error {
	data := map[string]any{
		"id":         fmt.Sprintf("%d", 1_000_000_000_000_000+i),
		"nonce":      fmt.Sprintf("%s%d", noncePrefix, i),
		"channel_id": "100000000000000000",
		"content":    strings.Repeat("a", max(0, scn.PayloadBytes-128)),
		"mentions":   []any{map[string]any{"id": "100000000000000001", "username": "stress-bench"}},
	}
	url := fmt.Sprintf("%s/mock/api/control/sessions/%s/dispatch", mockURL, sessionID)
	var out struct {
		Dispatched int `json:"dispatched"`
	}
	if err := postJSON(ctx, c, url, map[string]any{"event": "MESSAGE_CREATE", "data": data}, &out); err != nil {
		return err
	}
	if out.Dispatched < 2 {
		return fmt.Errorf("mock dispatched to %d connections, want 2", out.Dispatched)
	}
	return nil
}

// gatewayURL does what discord.js does on login: GET /api/v10/gateway/bot.
// The mock builds the URL from the Host header, so through the proxy it
// answers wss://discord.com — which routes the gateway back through the
// proxy too.
func gatewayURL(ctx context.Context, c *http.Client, base, token string) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := getJSON(ctx, c, base+"/api/v10/gateway/bot", token, &out); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", fmt.Errorf("empty url")
	}
	return out.URL, nil
}

func getJSON(ctx context.Context, c *http.Client, url, token string, out any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bot "+token)
	return doJSON(c, req, out)
}

func postJSON(ctx context.Context, c *http.Client, url string, body, out any) error {
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	return doJSON(c, req, out)
}

func doJSON(c *http.Client, req *http.Request, out any) error {
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: status %d: %s", req.Method, req.URL.Path, resp.StatusCode, raw)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: decode %q: %w", req.Method, req.URL.Path, raw, err)
	}
	return nil
}

func (r *recorder) writeCSV(path string, mode string) error {
	w, f, err := openCSV(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_ = w.Write([]string{"op", "direct_ns", "proxy_ns", "overhead_ns", "err"})
	for i := range r.sent {
		d, p := r.latency(mode, r.direct, i), r.latency(mode, r.proxy, i)
		overhead := ""
		if d > 0 && p > 0 {
			overhead = fmt.Sprint(p - d)
		}
		errStr := ""
		if e := r.errs[i].Load(); e != nil {
			errStr = *e
		} else if r.sent[i].Load() == 0 {
			errStr = "not sent (time cap)"
		}
		_ = w.Write([]string{fmt.Sprint(i), zeroBlank(d), zeroBlank(p), overhead, errStr})
	}
	w.Flush()
	return w.Error()
}

func zeroBlank(v int64) string {
	if v == 0 {
		return ""
	}
	return fmt.Sprint(v)
}

// dist summarises one latency column.
type dist struct {
	n                   int
	p50, p95, p99, pMax time.Duration
}

func distOf(vals []int64) dist {
	if len(vals) == 0 {
		return dist{}
	}
	slices.Sort(vals)
	pick := func(p float64) time.Duration { return time.Duration(vals[int(float64(len(vals)-1)*p)]) }
	return dist{len(vals), pick(0.50), pick(0.95), pick(0.99), time.Duration(vals[len(vals)-1])}
}

func writeSummary(path string, scn Scenario, r *recorder, stressRecords int) {
	var direct, proxy, overhead []int64
	for i := range r.sent {
		d, p := r.latency(scn.Mode, r.direct, i), r.latency(scn.Mode, r.proxy, i)
		if d > 0 {
			direct = append(direct, d)
		}
		if p > 0 {
			proxy = append(proxy, p)
		}
		if d > 0 && p > 0 {
			overhead = append(overhead, p-d)
		}
	}
	target, sent := len(r.sent), 0
	for i := range r.sent {
		if r.sent[i].Load() != 0 {
			sent++
		}
	}
	elapsed := (r.ended - r.started).Seconds()

	var b strings.Builder
	fmt.Fprintf(&b, "# Stress run: %s\n\n*%s*\n\n", scn.Name, scn.Description)
	fmt.Fprintf(&b, "- Mode: %s\n- Duration: %.2fs\n- Ops: %d sent of %d planned, op errors: %d\n", scn.Mode, elapsed, sent, target, r.opErrors.Load())
	if sent < target {
		b.WriteString("- ⚠ Not every op was sent before the time cap: the op source (the mock's control API for gateway runs) couldn't keep up with the target rate.\n")
	}
	if sent > 0 && elapsed > 0 {
		fmt.Fprintf(&b, "- Throughput: %.1f ops/s", float64(sent)/elapsed)
		if scn.RatePerSec > 0 {
			fmt.Fprintf(&b, " (target %d/s)", scn.RatePerSec)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "- Proxy `tamper` log records: %d\n\n", stressRecords)
	if target > 0 {
		what := "dispatch → gateway arrival"
		if scn.Mode == modeREST {
			what = "GET /gateway/bot round-trip"
		}
		fmt.Fprintf(&b, "## Latency (%s)\n\n", what)
		b.WriteString("| path | completed | p50 | p95 | p99 | max |\n|---|---|---|---|---|---|\n")
		for _, row := range []struct {
			name string
			d    dist
		}{{"direct", distOf(direct)}, {"via proxy", distOf(proxy)}, {"**proxy overhead**", distOf(overhead)}} {
			fmt.Fprintf(&b, "| %s | %d/%d | %s | %s | %s | %s |\n", row.name, row.d.n, sent, row.d.p50, row.d.p95, row.d.p99, row.d.pMax)
		}
		b.WriteString("\nOverhead is paired per op (proxy − direct). The mock writes to the direct connection first, so overhead includes that small head start; negative values mean the proxied copy still won the race.\n")
	}
	b.WriteString("\n`stats.csv`: 1 Hz samples of the proxy container (cgroup memory, cumulative CPU µs, RSS, threads, FDs, eth0 bytes).\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		logf("write summary: %v", err)
	}
}

func countStressRecords(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	return bytes.Count(raw, []byte(`"msg":"tamper `))
}

func scenarioNames() []string {
	names := make([]string, 0, len(Scenarios))
	for k := range Scenarios {
		names = append(names, k)
	}
	slices.Sort(names)
	return names
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[driver] "+format+"\n", args...)
}

func fatalf(format string, args ...any) {
	logf(format, args...)
	os.Exit(1)
}
