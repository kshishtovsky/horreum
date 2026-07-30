package quic

import (
	"bytes"
	"crypto/tls"
	"errors"
	"testing"
	"time"

	"github.com/horreum/horreum/internal/proto"
	"github.com/horreum/horreum/internal/transport/api"
)

type mockCache struct{}

func (m *mockCache) Set(key, value []byte) ([]byte, error) { return value, nil }
func (m *mockCache) Get(key []byte) ([]byte, error)        { return nil, api.ErrNotFound }
func (m *mockCache) Delete(key []byte) error               { return nil }
func (m *mockCache) Close() error                          { return nil }

type mockRouter struct{}

func (m *mockRouter) CacheFor(key []byte) api.CacheService { return &mockCache{} }
func (m *mockRouter) CacheIndexFor(key []byte) int         { return 0 }
func (m *mockRouter) ShardCount() int                      { return 1 }

func TestQUICTransportValidation(t *testing.T) {
	// 1. Missing router
	_, err := NewTransport("127.0.0.1:0", nil, nil, nil)
	if !errors.Is(err, api.ErrNoBackend) {
		t.Errorf("expected ErrNoBackend, got %v", err)
	}

	mr := &mockRouter{}

	// 2. Missing certificate
	_, err = NewTransport("127.0.0.1:0", mr, nil, nil)
	if err == nil {
		t.Error("expected error for missing certificate")
	}

	// 3. Register Certificate API
	cert := tls.Certificate{}
	RegisterCertificate("test-cert", &cert)
}

func TestHandleFrameQUIC(t *testing.T) {
	mr := &mockRouter{}
	var buf []byte

	// Unknown opcode
	fr := &proto.Frame{Op: 99, Key: []byte("k")}
	if handleFrameQUIC(mr, fr, &buf, noopRecorder{}, 0) {
		t.Error("expected handleFrameQUIC to return false for unknown Op")
	}

	// Set
	frSet := &proto.Frame{Op: proto.OpSet, Key: []byte("k"), Value: []byte("v")}
	if !handleFrameQUIC(mr, frSet, &buf, noopRecorder{}, 0) {
		t.Error("expected handleFrameQUIC to succeed for Set")
	}

	// Get missing
	frGet := &proto.Frame{Op: proto.OpGet, Key: []byte("k")}
	if !handleFrameQUIC(mr, frGet, &buf, noopRecorder{}, 0) {
		t.Error("expected handleFrameQUIC to handle Get missing")
	}

	// Del
	frDel := &proto.Frame{Op: proto.OpDel, Key: []byte("k")}
	if !handleFrameQUIC(mr, frDel, &buf, noopRecorder{}, 0) {
		t.Error("expected handleFrameQUIC to succeed for Del")
	}
}

// TestNoopRecorderAcceptsCalls ensures noopRecorder's methods are
// callable without panicking.
func TestNoopRecorderAcceptsCalls(t *testing.T) {
	var r noopRecorder
	r.ObserveSet("ok", time.Millisecond)
	r.ObserveGet("err", time.Millisecond)
	r.ObserveDel("ok", time.Millisecond)
}

// TestHandleFrameQUICGetHit covers the Get-hit path (cache returns
// the value).
func TestHandleFrameQUICGetHit(t *testing.T) {
	mr := &hitRouter{}
	var buf []byte
	frGet := &proto.Frame{Op: proto.OpGet, Key: []byte("k")}
	if !handleFrameQUIC(mr, frGet, &buf, noopRecorder{}, 0) {
		t.Error("handleFrameQUIC returned false on Get-hit")
	}
	if !bytes.Contains(buf, []byte("hit-value")) {
		t.Errorf("writeBuf did not contain hit value: %q", buf)
	}
}

// TestNewTransportBadAddress: an invalid UDP address produces an
// error from ResolveUDPAddr.
func TestNewTransportBadAddress(t *testing.T) {
	cert := &tls.Certificate{}
	_, err := NewTransport("not:a:valid:addr", &mockRouter{}, cert, nil)
	if err == nil {
		t.Errorf("expected error for bad address")
	}
}

// TestLoadCertificateReturnsRegistered: loadCertificate finds a
// previously-registered certificate.
func TestLoadCertificateReturnsRegistered(t *testing.T) {
	cert := &tls.Certificate{}
	RegisterCertificate("rt-registered", cert)
	got := loadCertificate("rt-registered")
	if got != cert {
		t.Errorf("loadCertificate = %v, want %v", got, cert)
	}
	if got := loadCertificate("rt-missing"); got != nil {
		t.Errorf("loadCertificate(missing) = %v, want nil", got)
	}
}

// TestLookupTransportQuic: the init() factory is registered.
func TestLookupTransportQuic(t *testing.T) {
	factory, ok := api.LookupTransport("quic")
	if !ok {
		t.Fatal("quic transport factory not registered")
	}
	if factory == nil {
		t.Fatal("quic factory is nil")
	}

	// Constructing through the factory with no certificate returns
	// errNoCert (since the in-memory registry is empty by default).
	_, err := factory(api.Options{Addr: "127.0.0.1:0", Router: &mockRouter{}})
	if !errors.Is(err, errNoCert) {
		t.Errorf("expected errNoCert, got %v", err)
	}
}

// TestErrNoCertIsExported: errNoCert has a stable message.
func TestErrNoCertIsExported(t *testing.T) {
	if errNoCert == nil {
		t.Fatal("errNoCert is nil")
	}
	if errNoCert.Error() == "" {
		t.Errorf("errNoCert has empty message")
	}
}

// hitRouter returns a cache that always hits with "hit-value".
type hitRouter struct{}

func (h *hitRouter) CacheFor(key []byte) api.CacheService { return &hitCache{} }
func (h *hitRouter) CacheIndexFor(key []byte) int         { return 0 }
func (h *hitRouter) ShardCount() int                      { return 1 }

type hitCache struct{}

func (h *hitCache) Set(key, value []byte) ([]byte, error) { return value, nil }
func (h *hitCache) Get(key []byte) ([]byte, error) {
	return []byte("hit-value"), nil
}
func (h *hitCache) Delete(key []byte) error { return nil }
func (h *hitCache) Close() error            { return nil }
