package healthz

import (
	"log"
	"net/http"
)

// ListenAndServe starts a tiny in-process HTTP server on 127.0.0.1:<port>
// that responds 200 OK to GET /healthz. It runs in a goroutine and never
// returns; failures are fatal because a missing healthcheck would make the
// container appear permanently unhealthy.
func ListenAndServe(port string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	addr := "127.0.0.1:" + port
	go func() {
		log.Printf("healthz server listening on %s", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Fatalf("healthz: %v", err)
		}
	}()
}
