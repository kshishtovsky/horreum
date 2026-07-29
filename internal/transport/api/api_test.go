package api

import (
	"context"
	"errors"
	"net"
	"testing"
)

type mockTransport struct {
	addr net.Addr
}

func (m *mockTransport) ListenAndServe() error           { return nil }
func (m *mockTransport) Shutdown(ctx context.Context) error { return nil }
func (m *mockTransport) Addr() net.Addr                  { return m.addr }

type mockRouter struct{}

func (m *mockRouter) CacheFor(key []byte) CacheService { return nil }
func (m *mockRouter) ShardCount() int                  { return 1 }

func TestServerFactory(t *testing.T) {
	// Register mock transport
	RegisterTransport("mock", func(opts Options) (Transport, error) {
		addr, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:1234")
		return &mockTransport{addr: addr}, nil
	})

	_, ok := LookupTransport("mock")
	if !ok {
		t.Fatal("expected 'mock' transport to be registered")
	}

	// 1. Missing router
	_, err := NewServer(Options{})
	if !errors.Is(err, ErrInvalidOptions) {
		t.Errorf("expected ErrInvalidOptions when router is missing, got %v", err)
	}

	// 2. Unsupported transport
	_, err = NewServer(Options{Router: &mockRouter{}, TransportName: "invalid_name"})
	var unsupp *UnsupportedTransportError
	if !errors.As(err, &unsupp) {
		t.Errorf("expected UnsupportedTransportError, got %v", err)
	} else if unsupp.Error() != "transport: unsupported transport: invalid_name" {
		t.Errorf("unexpected error message: %s", unsupp.Error())
	}

	// 3. Valid setup
	srv, err := NewServer(Options{Router: &mockRouter{}, TransportName: "mock"})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	if srv.Router() == nil {
		t.Error("expected router to be returned")
	}
	if srv.Addr().String() != "127.0.0.1:1234" {
		t.Errorf("unexpected addr: %s", srv.Addr().String())
	}
	if err := srv.ListenAndServe(); err != nil {
		t.Error(err)
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Error(err)
	}
}
