// api/server.go — Server factory: wires a Transport (TCP or QUIC)
// to a ShardRouter (or builds a default in-process one).
package api

import (
	"context"
	"errors"
	"fmt"
	"net"
)

// Server is the public façade.  It owns one Transport and (optionally)
// one ShardRouter.  When Cache is nil, the caller is expected to have
// built a ShardRouter outside NewServer and pass it via Options.
type Server struct {
	transport Transport
	router    ShardRouter
}

// Options configures a Server.
type Options struct {
	// Addr is the listen address (host:port).  Default "127.0.0.1:0".
	Addr string
	// TransportName is "tcp" or "quic".
	TransportName string
	// Router is the ShardRouter.  Required when NewServer is used
	// directly; the higher-level transport.NewServer creates a router
	// automatically.
	Router ShardRouter
	// TLSCertFile / TLSKeyFile are required for QUIC.
	TLSCertFile string
	TLSKeyFile  string
}

// UnsupportedTransportError is returned for unknown transport names.
type UnsupportedTransportError struct{ Name string }

func (e *UnsupportedTransportError) Error() string {
	return "transport: unsupported transport: " + e.Name
}

// ErrInvalidOptions is returned when Options is missing required fields.
var ErrInvalidOptions = errors.New("transport: invalid options")

// TransportFactory is the function shape used by RegisterTransport.
// Each transport sub-package calls RegisterTransport("tcp", NewTransport)
// from an init() function.
type TransportFactory func(opts Options) (Transport, error)

var registry = map[string]TransportFactory{}

// RegisterTransport installs a factory under name.  Called from
// transport sub-package init() functions.
func RegisterTransport(name string, factory TransportFactory) {
	registry[name] = factory
}

// LookupTransport returns the factory for name, if any.
func LookupTransport(name string) (TransportFactory, bool) {
	f, ok := registry[name]
	return f, ok
}

// NewServer constructs a Server.  When opts.Router is nil,
// ErrInvalidOptions is returned.
func NewServer(opts Options) (*Server, error) {
	if opts.Router == nil {
		return nil, fmt.Errorf("%w: router is required", ErrInvalidOptions)
	}
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:0"
	}
	if opts.TransportName == "" {
		opts.TransportName = "tcp"
	}
	factory, ok := LookupTransport(opts.TransportName)
	if !ok {
		return nil, &UnsupportedTransportError{Name: opts.TransportName}
	}
	t, err := factory(opts)
	if err != nil {
		return nil, err
	}
	return &Server{transport: t, router: opts.Router}, nil
}

// ListenAndServe blocks until Shutdown is called or the transport
// returns an error.
func (s *Server) ListenAndServe() error { return s.transport.ListenAndServe() }

// Shutdown stops the transport, bounded by ctx.
func (s *Server) Shutdown(ctx context.Context) error { return s.transport.Shutdown(ctx) }

// Addr returns the bound address.
func (s *Server) Addr() net.Addr { return s.transport.Addr() }

// Router returns the ShardRouter wired to this server.
func (s *Server) Router() ShardRouter { return s.router }
