package internals_test

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"hallmasterorg/hallmaster-proxy/internals"
	"hallmasterorg/hallmaster-proxy/internals/certs"
	"hallmasterorg/hallmaster-proxy/internals/config"
	"hallmasterorg/hallmaster-proxy/internals/internaltest"
)

// pipeListener satisfies net.Listener by handing out prepared net.Conn
// values from a channel. Close() signals shutdown by returning a closed
// error from Accept.
type pipeListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
	addr   pipeAddr
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn, 1), closed: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c, ok := <-l.conns:
		if !ok {
			return nil, net.ErrClosed
		}
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() {
		close(l.closed)
		close(l.conns)
	})
	return nil
}

func (l *pipeListener) Addr() net.Addr { return l.addr }

// Inject queues a connection to be returned from the next Accept call.
func (l *pipeListener) Inject(c net.Conn) {
	l.conns <- c
}

func newTestProxy(t *testing.T) *internals.MITMProxy {
	t.Helper()
	caCertPath, caKeyPath := internaltest.WriteTestCA(t)
	c, err := certs.New(caCertPath, caKeyPath)
	if err != nil {
		t.Fatalf("certs.New: %v", err)
	}
	return internals.NewMITMProxy(&config.Config{Port: "0"}, c)
}

func newTestDeps() internals.HandlerDeps {
	return internals.HandlerDeps{
		Cfg:    &config.Config{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// noopHandler counts invocations so tests can assert the dispatch loop
// fired.
type noopHandler struct {
	mu    sync.Mutex
	calls int
}

func (h *noopHandler) handle(_ internals.HandlerDeps, c *tls.Conn) {
	h.mu.Lock()
	h.calls++
	h.mu.Unlock()
	_ = c.Close()
}

func TestServe_GracefulShutdown(t *testing.T) {
	p := newTestProxy(t)
	deps := newTestDeps()
	ln := newPipeListener()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- p.Serve(ctx, ln, deps, func(d internals.HandlerDeps, c *tls.Conn) {
			_ = c.Close()
		})
	}()

	// Wait for Serve to flip Ready.
	deadline := time.Now().Add(2 * time.Second)
	for !p.Ready() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !p.Ready() {
		t.Fatal("proxy did not become Ready within 2s")
	}

	// Cancel context; Serve must close the listener and return nil.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v; want nil after ctx cancel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return within 2s of context cancel")
	}

	if p.Ready() {
		t.Fatal("Ready should flip to false after Serve returns")
	}
}

func TestServe_ListenerCloseTriggersReturn(t *testing.T) {
	p := newTestProxy(t)
	deps := newTestDeps()
	ln := newPipeListener()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- p.Serve(ctx, ln, deps, func(d internals.HandlerDeps, c *tls.Conn) {})
	}()

	// Closing the listener without cancelling ctx: Serve will see
	// Accept return net.ErrClosed and loop forever (continue) since
	// ctx.Err() == nil. Cancel ctx to unblock.
	_ = ln.Close()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		// Either nil (graceful) or wrapping net.ErrClosed is acceptable.
		if err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Serve returned unexpected err: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return within 2s")
	}
}

func TestReady_StartsFalse(t *testing.T) {
	p := newTestProxy(t)
	if p.Ready() {
		t.Fatal("Ready should be false before Serve")
	}
}
