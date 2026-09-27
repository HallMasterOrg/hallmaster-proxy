package handlers_test

import (
	"bufio"
	"bytes"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"

	"hallmasterorg/hallmaster-proxy/internals/handlers"
	"hallmasterorg/hallmaster-proxy/internals/internaltest"
	"hallmasterorg/hallmaster-proxy/internals/tamper"
)

// wsHarness wires InspectWS over two net.Pipe pairs:
//
//	bot  <-> client/clientBr (proxy reads from clientBr, writes to client)
//	dish <-> server/serverBr (proxy reads from serverBr, writes to server)
//
// The "bot" end is what the benchmark writes outbound frames into and
// reads inbound frames from; the "dish" end is what the bench impersonates
// as Discord. InspectWS runs in a goroutine until both halves close.
type wsHarness struct {
	bot, dish     net.Conn
	inspectorDone chan struct{}
}

func newWSHarness(tb testing.TB, tamp tamper.Tamperer) *wsHarness {
	tb.Helper()

	botClient, botProxy := net.Pipe()
	dishProxy, dishServer := net.Pipe()

	clientBr := bufio.NewReader(botProxy)
	serverBr := bufio.NewReader(dishProxy)

	done := make(chan struct{})
	go func() {
		defer close(done)
		handlers.InspectWS(
			slog.New(slog.NewTextHandler(io.Discard, nil)),
			botProxy, clientBr,
			dishProxy, serverBr,
			"gateway.discord.gg",
			false,
			tamp,
		)
	}()

	return &wsHarness{bot: botClient, dish: dishServer, inspectorDone: done}
}

func (h *wsHarness) close() {
	_ = h.bot.Close()
	_ = h.dish.Close()
	<-h.inspectorDone
}

// BenchmarkWSRelay_Uncompressed_1KB measures the cost of relaying a
// single 1 KB text frame in each direction (bot->discord then
// discord->bot). One iteration covers both halves; the LatencyTamperer
// pairs WSOutgoing -> WSIncoming durations.
func BenchmarkWSRelay_Uncompressed_1KB(b *testing.B) {
	payload := bytes.Repeat([]byte("a"), 1024)

	tamp := internaltest.NewLatencyTamperer(b.N * 2)
	h := newWSHarness(b, tamp)
	defer h.close()

	// Reader for inbound bot-side frames; writer for outbound bot-side.
	botReader := bufio.NewReader(h.bot)
	dishReader := bufio.NewReader(h.dish)

	// Pump goroutines: dish echoes whatever it receives back. This keeps
	// both directions exercised per iteration without ordering games.
	echoDone := make(chan struct{})
	go func() {
		defer close(echoDone)
		for {
			frame, err := ws.ReadFrame(dishReader)
			if err != nil {
				return
			}
			data := frame.Payload
			if frame.Header.Masked {
				ws.Cipher(data, frame.Header.Mask, 0)
			}
			if err := wsutil.WriteServerMessage(h.dish, ws.OpText, data); err != nil {
				return
			}
		}
	}()

	before := internaltest.SnapshotRuntime()
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if err := wsutil.WriteClientMessage(h.bot, ws.OpText, payload); err != nil {
			b.Fatalf("write client at i=%d: %v", i, err)
		}
		frame, err := ws.ReadFrame(botReader)
		if err != nil {
			b.Fatalf("read frame at i=%d: %v", i, err)
		}
		if len(frame.Payload) != len(payload) {
			b.Fatalf("payload len mismatch at i=%d: got %d", i, len(frame.Payload))
		}
	}

	b.StopTimer()
	after := internaltest.SnapshotRuntime()
	diff := before.Sub(after)

	// Pair WSOutgoing (bot->discord) with WSIncoming (discord->bot) — one
	// proxy round trip per iter.
	durations := tamp.Pairs(internaltest.KindWSOutgoing, internaltest.KindWSIncoming)
	p50, p95, p99 := internaltest.Percentiles(durations)

	b.ReportMetric(float64(p50.Nanoseconds()), "p50-ns/op")
	b.ReportMetric(float64(p95.Nanoseconds()), "p95-ns/op")
	b.ReportMetric(float64(p99.Nanoseconds()), "p99-ns/op")
	b.ReportMetric(float64(diff.HeapAllocBytes)/float64(b.N), "heap-delta-B/op")
	b.ReportMetric(float64(diff.Goroutines), "goroutine-delta")
	b.ReportMetric(float64(tamp.Dropped.Load()), "dropped-samples")

	// Drain the echo goroutine before close().
	_ = h.bot.Close()
	<-echoDone
}

// BenchmarkWSRelay_Burst sends a burst of N frames bot->discord without
// waiting for echoes, then drains the responses. Measures throughput
// under back-pressure rather than per-frame latency.
func BenchmarkWSRelay_Burst(b *testing.B) {
	const burst = 256
	payload := bytes.Repeat([]byte("b"), 256)

	h := newWSHarness(b, tamper.Nop{})
	defer h.close()

	botReader := bufio.NewReader(h.bot)
	dishReader := bufio.NewReader(h.dish)

	echoDone := make(chan struct{})
	go func() {
		defer close(echoDone)
		for {
			frame, err := ws.ReadFrame(dishReader)
			if err != nil {
				return
			}
			data := frame.Payload
			if frame.Header.Masked {
				ws.Cipher(data, frame.Header.Mask, 0)
			}
			if err := wsutil.WriteServerMessage(h.dish, ws.OpText, data); err != nil {
				return
			}
		}
	}()

	before := internaltest.SnapshotRuntime()
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Two goroutines: writer fires `burst` frames, reader drains them.
		// Without this the net.Pipe back-pressure deadlocks the bench.
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for k := 0; k < burst; k++ {
				if err := wsutil.WriteClientMessage(h.bot, ws.OpText, payload); err != nil {
					b.Errorf("write at k=%d: %v", k, err)
					return
				}
			}
		}()
		go func() {
			defer wg.Done()
			for k := 0; k < burst; k++ {
				if _, err := ws.ReadFrame(botReader); err != nil {
					b.Errorf("read at k=%d: %v", k, err)
					return
				}
			}
		}()
		wg.Wait()
	}

	b.StopTimer()
	after := internaltest.SnapshotRuntime()
	diff := before.Sub(after)
	b.ReportMetric(float64(diff.HeapAllocBytes)/float64(b.N), "heap-delta-B/op")
	b.ReportMetric(float64(burst), "frames/op")
	b.ReportMetric(float64(diff.Goroutines), "goroutine-delta")

	_ = h.bot.Close()
	<-echoDone
}
