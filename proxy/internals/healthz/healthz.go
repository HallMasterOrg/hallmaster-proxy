package healthz

import (
	"log/slog"
	"net/http"
	"os"
)

// ListenAndServe starts a tiny in-process HTTP server on 127.0.0.1:<port>
// that responds to GET /healthz. It runs in a goroutine and never returns;
// failures are fatal because a missing healthcheck would make the
// container appear permanently unhealthy.
//
// The response is 200 OK when `ready` is nil or returns true; otherwise
// 503 Service Unavailable. `logger` receives lifecycle and failure logs;
// pass nil to use slog.Default().
func ListenAndServe(port string, logger *slog.Logger, ready func() bool) {
	if logger == nil {
		logger = slog.Default()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if ready != nil && !ready() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready\n"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	addr := "127.0.0.1:" + port
	go func() {
		logger.Info("healthz listening", "addr", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			logger.Error("healthz", "err", err)
			os.Exit(1)
		}
	}()
}
