// server.go — top-level Server that creates a default ShardSet when
// the caller does not supply one.  Lives in the parent transport
// package so callers can write transport.NewServer.
package transport

import (
	"context"
	"errors"
	"net"

	"github.com/kshishtovsky/horreum/internal/transport/api"
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
	// Recorder receives per-op metrics observations.
	Recorder api.Recorder
	// Router, if set, replaces the auto-built ShardSet.  Required by
	// NewServerFromOpts (the persistent-mode path).
	Router api.ShardRouter
}

// NewServer constructs a Server.  When Options.Cert is nil for QUIC,
// NewServer loads the certificate from TLSCertFile/TLSKeyFile using
// tls.LoadX509KeyPair.  For TCP TLS is currently not wired; the
// transport runs plaintext and TLS support is a fast follow-up.
func NewServer(opts Options) (*Server, error) {
	if opts.ShardConfig.NumShards == 0 {
		opts.ShardConfig.NumShards = DefaultShardCount()
	}
	set, err := NewShardSet(opts.ShardConfig)
	if err != nil {
		return nil, err
	}
	srv, err := newServerFromAPI(api.Options{
		Addr:          opts.Addr,
		TransportName: opts.TransportName,
		Router:        set,
		TLSCertFile:   opts.TLSCertFile,
		TLSKeyFile:    opts.TLSKeyFile,
		Recorder:      opts.Recorder,
	})
	if err != nil {
		_ = set.Close()
		return nil, err
	}
	return &Server{apiSrv: srv, set: set}, nil
}

// NewServerFromOpts builds a Server from a fully-formed
// transport.Options.  Callers that already have a ShardSet (e.g. the
// horreum binary, which uses PersistentManager) construct this way.
func NewServerFromOpts(opts Options) (*Server, error) {
	if opts.Router == nil {
		return nil, errors.New("transport: Options.Router is required")
	}
	srv, err := newServerFromAPI(api.Options{
		Addr:          opts.Addr,
		TransportName: opts.TransportName,
		Router:        opts.Router,
		TLSCertFile:   opts.TLSCertFile,
		TLSKeyFile:    opts.TLSKeyFile,
		Recorder:      opts.Recorder,
	})
	if err != nil {
		return nil, err
	}
	return &Server{apiSrv: srv, set: nil}, nil
}

func newServerFromAPI(opts api.Options) (*api.Server, error) {
	return api.NewServer(opts)
}

// ListenAndServe blocks until Shutdown is called.
func (s *Server) ListenAndServe() error { return s.apiSrv.ListenAndServe() }

// Shutdown stops the listener and tears down the shard set.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.apiSrv.Shutdown(ctx)
	if s.set != nil {
		_ = s.set.Close()
	}
	return err
}

// Addr returns the bound address.
func (s *Server) Addr() net.Addr { return s.apiSrv.Addr() }

// ShardSet returns the underlying ShardSet, or nil if the server
// uses a caller-supplied router.
func (s *Server) ShardSet() *ShardSet { return s.set }
