package healthz

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// pickFreePort asks the kernel for an unused TCP port on localhost.
func pickFreePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return fmt.Sprintf("%d", port)
}

// waitForHealthz polls /healthz until it returns a non-error response or
// the deadline elapses. Returns the final status code.
func waitForHealthz(t *testing.T, port string, deadline time.Duration) int {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		resp, err := http.Get("http://127.0.0.1:" + port + "/healthz")
		if err == nil {
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			return resp.StatusCode
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("healthz never came up on port %s", port)
	return 0
}

func TestHealthz_ReadyReturns200(t *testing.T) {
	port := pickFreePort(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ListenAndServe(port, logger, func() bool { return true })

	if got := waitForHealthz(t, port, 2*time.Second); got != 200 {
		t.Errorf("status: got %d want 200", got)
	}
}

func TestHealthz_NotReadyReturns503(t *testing.T) {
	port := pickFreePort(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ListenAndServe(port, logger, func() bool { return false })

	if got := waitForHealthz(t, port, 2*time.Second); got != 503 {
		t.Errorf("status: got %d want 503", got)
	}
}

func TestHealthz_NilReadyReturns200(t *testing.T) {
	port := pickFreePort(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ListenAndServe(port, logger, nil)

	if got := waitForHealthz(t, port, 2*time.Second); got != 200 {
		t.Errorf("status with nil ready: got %d want 200", got)
	}
}

func TestHealthz_ReadyTransitions(t *testing.T) {
	port := pickFreePort(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var ready atomic.Bool
	ListenAndServe(port, logger, ready.Load)

	if got := waitForHealthz(t, port, 2*time.Second); got != 503 {
		t.Errorf("pre-flip: got %d want 503", got)
	}

	ready.Store(true)

	resp, err := http.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		t.Fatalf("get after flip: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("post-flip: got %d want 200", resp.StatusCode)
	}
}
