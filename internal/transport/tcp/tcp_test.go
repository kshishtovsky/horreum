package tcp

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/horreum/horreum/internal/proto"
	"github.com/horreum/horreum/internal/transport/api"
)

type mockCache struct {
	store map[string][]byte
}

func (m *mockCache) Set(key, value []byte, ttlSeconds uint32) ([]byte, error) {
	m.store[string(key)] = append([]byte(nil), value...)
	return value, nil
}

func (m *mockCache) Get(key []byte) ([]byte, error) {
	val, ok := m.store[string(key)]
	if !ok {
		return nil, api.ErrNotFound
	}
	return val, nil
}

func (m *mockCache) Delete(key []byte) error {
	delete(m.store, string(key))
	return nil
}

func (m *mockCache) DeleteExpired(limit int) error         { return nil }
func (m *mockCache) CAS(k, ev, nv []byte) ([]byte, bool, error) { return nil, true, nil }
func (m *mockCache) Incr(k []byte, d int64) (int64, error) { return d, nil }
func (m *mockCache) Close() error                          { return nil }

type mockRouter struct {
	cache *mockCache
}

func (m *mockRouter) CacheFor(key []byte) api.CacheService { return m.cache }
func (m *mockRouter) CacheIndexFor(key []byte) int         { return 0 }
func (m *mockRouter) ShardCount() int                      { return 1 }

func TestTCPTransport(t *testing.T) {
	// 1. Missing router
	_, err := NewTransport("127.0.0.1:0", nil, nil)
	if !errors.Is(err, api.ErrNoBackend) {
		t.Fatalf("expected ErrNoBackend, got %v", err)
	}

	mc := &mockCache{store: make(map[string][]byte)}
	mr := &mockRouter{cache: mc}

	tr, err := NewTransport("127.0.0.1:0", mr, nil)
	if err != nil {
		t.Fatalf("NewTransport failed: %v", err)
	}

	go func() {
		_ = tr.ListenAndServe()
	}()

	conn, err := net.Dial("tcp", tr.Addr().String())
	if err != nil {
		t.Fatalf("net.Dial failed: %v", err)
	}
	defer conn.Close()

	// Perform SET operation via proto
	req := proto.EncodeRequest(nil, proto.OpSet, []byte("key1"), []byte("value1"))
	if _, err = conn.Write(req); err != nil {
		t.Fatalf("Write SET failed: %v", err)
	}

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("Read SET response failed: %v", err)
	}

	var parser proto.Parser
	frames, err := parser.Feed(buf[:n])
	if err != nil || len(frames) == 0 {
		t.Fatalf("Parse response failed: %v", err)
	}
	if frames[0].Op != proto.OpSet || frames[0].IsError() {
		t.Errorf("expected successful SET response")
	}

	// Perform GET operation
	reqGet := proto.EncodeRequest(nil, proto.OpGet, []byte("key1"), nil)
	if _, err = conn.Write(reqGet); err != nil {
		t.Fatalf("Write GET failed: %v", err)
	}
	n, err = conn.Read(buf)
	if err != nil {
		t.Fatalf("Read GET response failed: %v", err)
	}
	parser.Reset()
	frames, err = parser.Feed(buf[:n])
	if err != nil || len(frames) == 0 {
		t.Fatalf("Parse GET response failed: %v", err)
	}
	if frames[0].Op != proto.OpGet || string(frames[0].Value) != "value1" {
		t.Errorf("expected GET value 'value1', got %q", string(frames[0].Value))
	}

	// Perform GET for missing key
	reqGetMissing := proto.EncodeRequest(nil, proto.OpGet, []byte("missing"), nil)
	if _, err = conn.Write(reqGetMissing); err != nil {
		t.Fatalf("Write GET missing failed: %v", err)
	}
	n, err = conn.Read(buf)
	if err != nil {
		t.Fatalf("Read GET missing response failed: %v", err)
	}
	parser.Reset()
	frames, err = parser.Feed(buf[:n])
	if err != nil || len(frames) == 0 {
		t.Fatalf("Parse GET missing response failed: %v", err)
	}
	if !frames[0].IsError() {
		t.Errorf("expected GET missing key to return error flag")
	}

	// Perform DEL operation
	reqDel := proto.EncodeRequest(nil, proto.OpDel, []byte("key1"), nil)
	if _, err = conn.Write(reqDel); err != nil {
		t.Fatalf("Write DEL failed: %v", err)
	}
	n, err = conn.Read(buf)
	if err != nil {
		t.Fatalf("Read DEL response failed: %v", err)
	}
	parser.Reset()
	frames, err = parser.Feed(buf[:n])
	if err != nil || len(frames) == 0 {
		t.Fatalf("Parse DEL response failed: %v", err)
	}
	if frames[0].Op != proto.OpDel {
		t.Errorf("expected OpDel response")
	}

	// Shutdown transport
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := tr.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown failed: %v", err)
	}
}

func TestShardIndexFor(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	idx := shardIndexFor(c1, 4)
	if idx < 0 || idx >= 4 {
		t.Errorf("unexpected shard index: %d", idx)
	}
}

func TestHandleFrameUnknownOp(t *testing.T) {
	mc := &mockCache{store: make(map[string][]byte)}
	mr := &mockRouter{cache: mc}
	var buf []byte
	fr := &proto.Frame{Op: 99, Key: []byte("test")}
	if handleFrame(mr, fr, &buf, noopRecorder{}, 0) {
		t.Error("expected handleFrame to return false for unknown Op")
	}
}

type errorCache struct {
	mockCache
}

func (e *errorCache) Set(key, value []byte, ttlSeconds uint32) ([]byte, error) {
	return nil, errors.New("set error")
}
func (e *errorCache) Get(key []byte) ([]byte, error)        { return nil, errors.New("get error") }
func (e *errorCache) Delete(key []byte) error               { return errors.New("delete error") }
func (e *errorCache) DeleteExpired(limit int) error         { return nil }
func (e *errorCache) CAS(k, ev, nv []byte) ([]byte, bool, error) { return nil, false, errors.New("cas error") }
func (e *errorCache) Incr(k []byte, d int64) (int64, error) { return 0, errors.New("incr error") }
func (e *errorCache) Close() error                          { return nil }

type errorRouter struct {
	cache api.CacheService
}

func (e *errorRouter) CacheFor(key []byte) api.CacheService { return e.cache }
func (e *errorRouter) CacheIndexFor(key []byte) int         { return 0 }
func (e *errorRouter) ShardCount() int                      { return 1 }

func TestHandleFrameSetError(t *testing.T) {
	ec := &errorCache{mockCache: mockCache{store: make(map[string][]byte)}}
	er := &errorRouter{cache: ec}
	var buf []byte
	fr := &proto.Frame{Op: proto.OpSet, Key: []byte("test"), Value: []byte("val")}
	if !handleFrame(er, fr, &buf, noopRecorder{}, 0) {
		t.Error("expected handleFrame to return true on handled set error")
	}
}


func (m *mockCache) Scan(prefix []byte, cursor uint64, count int) ([][]byte, uint64, error) {
	return nil, 0, nil
}
func (m *mockCache) DelPrefix(prefix []byte) (uint64, error) {
	return 0, nil
}


func (m *mockRouter) Scan(prefix []byte, cursor uint64, count int) ([][]byte, uint64, error) {
	return nil, 0, nil
}
func (m *mockRouter) DelPrefix(prefix []byte) (uint64, error) {
	return 0, nil
}


func (e *errorCache) Scan(prefix []byte, cursor uint64, count int) ([][]byte, uint64, error) { return nil, 0, nil }
func (e *errorCache) DelPrefix(prefix []byte) (uint64, error) { return 0, nil }
func (e *errorRouter) Scan(prefix []byte, cursor uint64, count int) ([][]byte, uint64, error) { return nil, 0, nil }
func (e *errorRouter) DelPrefix(prefix []byte) (uint64, error) { return 0, nil }
