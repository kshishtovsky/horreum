// http.go — Prometheus exposition over HTTP.
//
// The /metrics endpoint serves text/plain version 0.0.4.  We use
// net/http with a per-request buffer to avoid allocating strings
// repeatedly.
package metrics

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// MetricsServer is a small HTTP server that exposes /metrics.
type MetricsServer struct {
	srv  *http.Server
	ln   net.Listener
	addr string
}

// NewMetricsServer binds to addr and returns a server ready to run.
// Use Start to serve and Shutdown to stop.
func NewMetricsServer(addr string) (*MetricsServer, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if err := WriteText(w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return &MetricsServer{srv: srv, ln: ln, addr: addr}, nil
}

// Addr returns the bound address (useful for tests picking :0).
func (s *MetricsServer) Addr() net.Addr { return s.ln.Addr() }

// Start serves in a background goroutine.  Returns immediately.
func (s *MetricsServer) Start() {
	go func() {
		_ = s.srv.Serve(s.ln)
	}()
}

// Shutdown stops the server gracefully, bounded by ctx.
func (s *MetricsServer) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

// ErrServerClosed is returned by Serve after Shutdown.
var ErrServerClosed = errors.New("metrics: server closed")
