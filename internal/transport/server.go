// server.go — top-level Server that creates a default ShardSet when
// the caller does not supply one.  Lives in the parent transport
// package so callers can write transport.NewServer.
package transport

import (
	"context"
	"net"

	"github.com/horreum/horreum/internal/transport/api"
)

// Server is a wrapper around api.Server that owns a default ShardSet.
type Server struct {
	apiSrv *api.Server
	set    *ShardSet
}

// Options configures a Server.
type Options struct {
	Addr          string
	TransportName string // "tcp" or "quic"
	ShardConfig   ShardConfig
	TLSCertFile   string
	TLSKeyFile    string
	// Cert is an optional pre-loaded TLS certificate; if non-nil it
	// takes precedence over TLSCertFile/TLSKeyFile.  QUIC requires
	// either Cert or (TLSCertFile + TLSKeyFile).
	Cert *tlsCertShim
}

// NewServer constructs a Server.  When Options.Cert is nil for QUIC,
// NewServer loads the certificate from TLSCertFile/TLSKeyFile using
// tls.LoadX509KeyPair.  For TCP TLS is currently not wired (v1.0.0
// runs plaintext; TLS support is a fast follow-up).
func NewServer(opts Options) (*Server, error) {
	if opts.ShardConfig.NumShards == 0 {
		opts.ShardConfig.NumShards = DefaultShardCount()
	}
	set, err := NewShardSet(opts.ShardConfig)
	if err != nil {
		return nil, err
	}
	srv, err := api.NewServer(api.Options{
		Addr:          opts.Addr,
		TransportName: opts.TransportName,
		Router:        set,
		TLSCertFile:   opts.TLSCertFile,
		TLSKeyFile:    opts.TLSKeyFile,
	})
	if err != nil {
		_ = set.Close()
		return nil, err
	}
	return &Server{apiSrv: srv, set: set}, nil
}

// ListenAndServe blocks until Shutdown is called.
func (s *Server) ListenAndServe() error { return s.apiSrv.ListenAndServe() }

// Shutdown stops the listener and tears down the shard set.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.apiSrv.Shutdown(ctx)
	_ = s.set.Close()
	return err
}

// Addr returns the bound address.
func (s *Server) Addr() net.Addr { return s.apiSrv.Addr() }

// ShardSet returns the underlying ShardSet.  Exposed so tests can
// inspect eviction state directly.
func (s *Server) ShardSet() *ShardSet { return s.set }
