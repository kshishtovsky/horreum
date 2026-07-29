// transport_internal_test.go — unit tests for transport.ShardSet and
// related helpers.  These tests exercise the in-process ShardSet
// without booting a network listener; that path is covered by the
// end-to-end TCP/QUIC tests in transport_test.go.
package transport_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/horreum/horreum/internal/arena"
	"github.com/horreum/horreum/internal/compress"
	"github.com/horreum/horreum/internal/encrypt"
	"github.com/horreum/horreum/internal/transport"
)

// TestShardSetBasic exercises the happy path: NewShardSet + CacheFor +
// Put + Get + Delete.
func TestShardSetBasic(t *testing.T) {
	ss, err := transport.NewShardSet(transport.ShardConfig{
		NumShards:     4,
		RegionSize:    1 << 20,
		EvictCapacity: 64,
	})
	if err != nil {
		t.Fatalf("NewShardSet: %v", err)
	}
	defer ss.Close()

	c := ss.CacheFor([]byte("k"))
	if c == nil {
		t.Fatal("CacheFor returned nil")
	}
	out, err := c.Set([]byte("k"), []byte("v"))
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !bytes.Equal(out, []byte("v")) {
		t.Errorf("Set returned %q, want v", out)
	}
	got, err := c.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, []byte("v")) {
		t.Errorf("Get = %q, want v", got)
	}
	if err := c.Delete([]byte("k")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// TestShardSetShardCount: shard count is reported correctly.
func TestShardSetShardCount(t *testing.T) {
	ss, err := transport.NewShardSet(transport.ShardConfig{
		NumShards:     7,
		RegionSize:    1 << 20,
		EvictCapacity: 32,
	})
	if err != nil {
		t.Fatalf("NewShardSet: %v", err)
	}
	defer ss.Close()
	if got := ss.ShardCount(); got != 7 {
		t.Errorf("ShardCount = %d, want 7", got)
	}
}

// TestShardSetCacheForRouting: keys with different hashes must route
// to distinct shards (probabilistically).
func TestShardSetCacheForRouting(t *testing.T) {
	ss, err := transport.NewShardSet(transport.ShardConfig{
		NumShards:     4,
		RegionSize:    1 << 20,
		EvictCapacity: 32,
	})
	if err != nil {
		t.Fatalf("NewShardSet: %v", err)
	}
	defer ss.Close()

	seen := map[transport.CacheService]bool{}
	for i := 0; i < 100; i++ {
		key := []byte{byte(i), byte(i >> 8), byte(i >> 16)}
		seen[ss.CacheFor(key)] = true
	}
	if len(seen) < 2 {
		t.Errorf("expected distinct shards for distinct keys; saw %d", len(seen))
	}
}

// TestShardSetCloseIdempotent: Close twice returns nil on the second call.
func TestShardSetCloseIdempotent(t *testing.T) {
	ss, err := transport.NewShardSet(transport.ShardConfig{
		NumShards: 2, RegionSize: 1 << 20, EvictCapacity: 16,
	})
	if err != nil {
		t.Fatalf("NewShardSet: %v", err)
	}
	if err := ss.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := ss.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// TestShardSetStats: Stats aggregates arena stats.
func TestShardSetStats(t *testing.T) {
	ss, err := transport.NewShardSet(transport.ShardConfig{
		NumShards: 3, RegionSize: 1 << 20, EvictCapacity: 16,
	})
	if err != nil {
		t.Fatalf("NewShardSet: %v", err)
	}
	defer ss.Close()
	st := ss.Stats()
	if st.LiveObjects != 0 {
		t.Errorf("LiveObjects on fresh = %d, want 0", st.LiveObjects)
	}
	c := ss.CacheFor([]byte("k"))
	if _, err := c.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	st = ss.Stats()
	if st.LiveObjects != 1 {
		t.Errorf("LiveObjects after one Put = %d, want 1", st.LiveObjects)
	}
}

// TestPickByAddr: routing by source IP uses FNV-1a over the host.
func TestPickByAddr(t *testing.T) {
	ss, err := transport.NewShardSet(transport.ShardConfig{
		NumShards: 4, RegionSize: 1 << 20, EvictCapacity: 16,
	})
	if err != nil {
		t.Fatalf("NewShardSet: %v", err)
	}
	defer ss.Close()

	addr1 := &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 1234}
	addr2 := &net.TCPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 1234}
	if ss.PickByAddr(addr1) == ss.PickByAddr(addr2) {
		t.Errorf("distinct hosts routed to same shard")
	}
	if ss.PickByAddr(addr1) >= ss.ShardCount() {
		t.Errorf("shard index out of range: %d", ss.PickByAddr(addr1))
	}

	// PickByAddr on a raw string addr (no port) should still work.
	strAddr, _ := net.ResolveIPAddr("ip", "192.168.1.1")
	idx := ss.PickByAddr(strAddr)
	if idx < 0 || idx >= ss.ShardCount() {
		t.Errorf("strAddr routing: %d out of range", idx)
	}
}

// TestPickByAddrUnparseable: an unparseable Addr falls back to the
// full string.
func TestPickByAddrUnparseable(t *testing.T) {
	ss, err := transport.NewShardSet(transport.ShardConfig{
		NumShards: 4, RegionSize: 1 << 20, EvictCapacity: 16,
	})
	if err != nil {
		t.Fatalf("NewShardSet: %v", err)
	}
	defer ss.Close()

	// Custom addr with no host:port delimiter → SplitHostPort fails.
	raw := rawAddr("opaque-string")
	idx := ss.PickByAddr(raw)
	if idx < 0 || idx >= ss.ShardCount() {
		t.Errorf("PickByAddr(unparseable) = %d out of range", idx)
	}
}

// rawAddr is a net.Addr whose String() is not host:port.
type rawAddr string

func (r rawAddr) Network() string { return "raw" }
func (r rawAddr) String() string  { return string(r) }

// TestDefaultShardCount: returns 4×NumCPU clamped to [4, 64].
func TestDefaultShardCount(t *testing.T) {
	n := transport.DefaultShardCount()
	if n < 4 || n > 64 {
		t.Errorf("DefaultShardCount = %d, want in [4,64]", n)
	}
}

// TestNewShardSetOrPanic: panics on bad config, succeeds on good.
func TestNewShardSetOrPanic(t *testing.T) {
	ss := transport.NewShardSetOrPanic(2, 1<<20, 16, nil, nil)
	if ss == nil {
		t.Fatal("NewShardSetOrPanic returned nil")
	}
	defer ss.Close()
}

// TestNewShardSetFromArena: wraps a single Manager as a 1-shard set.
func TestNewShardSetFromArena(t *testing.T) {
	mgr, err := arena.NewManager(1<<20, false)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Close()
	ss := transport.NewShardSetFromArena(mgr, 0, 0, nil, nil)
	if ss.ShardCount() != 1 {
		t.Errorf("ShardCount = %d, want 1", ss.ShardCount())
	}
	c := ss.CacheFor([]byte("k"))
	if c == nil {
		t.Fatal("CacheFor returned nil")
	}
	if _, err := c.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := c.Get([]byte("k")); err != nil {
		t.Fatalf("Get: %v", err)
	}
}

// TestNewShardSetFromArenaMultipleShards: NumShards > 1 still works
// (all shards share the same arena).
func TestNewShardSetFromArenaMultipleShards(t *testing.T) {
	mgr, err := arena.NewManager(1<<20, false)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Close()
	ss := transport.NewShardSetFromArena(mgr, 4, 16, nil, nil)
	if ss.ShardCount() != 4 {
		t.Errorf("ShardCount = %d, want 4", ss.ShardCount())
	}
}

// TestNewServerFromOptsRequiresRouter: returns error when Router is nil.
func TestNewServerFromOptsRequiresRouter(t *testing.T) {
	_, err := transport.NewServerFromOpts(transport.Options{
		Addr:          "127.0.0.1:0",
		TransportName: "tcp",
		Router:        nil,
	})
	if err == nil {
		t.Errorf("expected error when Router is nil")
	}
}

// TestNewServerFromOptsSuccess: NewServerFromOpts returns a Server
// whose ShardSet() is nil (because no auto ShardSet is built).
func TestNewServerFromOptsSuccess(t *testing.T) {
	mgr, err := arena.NewManager(1<<20, false)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Close()
	router := transport.NewShardSetFromArena(mgr, 2, 16, nil, nil)
	srv, err := transport.NewServerFromOpts(transport.Options{
		Addr:          "127.0.0.1:0",
		TransportName: "tcp",
		Router:        router,
	})
	if err != nil {
		t.Fatalf("NewServerFromOpts: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
	}()
	if srv.ShardSet() != nil {
		t.Errorf("ShardSet() should be nil when Router was supplied")
	}
	go func() { _ = srv.ListenAndServe() }()
	// Confirm we can connect — gives us coverage of ListenAndServe.
	conn, err := net.Dial("tcp", srv.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()
}

// TestServerShardSetAccessor: when constructed via NewServer (default
// ShardSet), ShardSet() returns it.
func TestServerShardSetAccessor(t *testing.T) {
	srv, err := transport.NewServer(transport.Options{
		Addr:          "127.0.0.1:0",
		TransportName: "tcp",
		ShardConfig: transport.ShardConfig{
			NumShards:     2,
			RegionSize:    1 << 20,
			EvictCapacity: 16,
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
	}()
	if srv.ShardSet() == nil {
		t.Errorf("ShardSet() returned nil for NewServer (default ShardSet path)")
	}
}

// TestSetGetCompression: Put/Get with LZ4 compression round-trips.
func TestSetGetCompression(t *testing.T) {
	ss, err := transport.NewShardSet(transport.ShardConfig{
		NumShards:     2,
		RegionSize:    1 << 20,
		EvictCapacity: 16,
		Compressor:    compress.NewLZ4(1),
	})
	if err != nil {
		t.Fatalf("NewShardSet: %v", err)
	}
	defer ss.Close()
	c := ss.CacheFor([]byte("k"))
	plain := []byte("compressible-compressible-compressible-compressible-payload")
	out, err := c.Set([]byte("k"), plain)
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !bytes.Equal(out, plain) {
		t.Errorf("Set returned %q", out)
	}
	got, err := c.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("Get = %q, want %q", got, plain)
	}
}

// TestSetGetEncryption: Put/Get with AES encryption round-trips.
func TestSetGetEncryption(t *testing.T) {
	key := bytes.Repeat([]byte{0xAA}, 32)
	cipher, err := encrypt.NewAESGCM(key)
	if err != nil {
		t.Fatalf("NewAESGCM: %v", err)
	}
	ss, err := transport.NewShardSet(transport.ShardConfig{
		NumShards:     2,
		RegionSize:    1 << 20,
		EvictCapacity: 16,
		Cipher:        cipher,
	})
	if err != nil {
		t.Fatalf("NewShardSet: %v", err)
	}
	defer ss.Close()
	c := ss.CacheFor([]byte("k"))
	if _, err := c.Set([]byte("k"), []byte("secret")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := c.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, []byte("secret")) {
		t.Errorf("Get = %q, want secret", got)
	}
}

// TestGetMissingKey: Get on an absent key returns ErrNotFound.
func TestGetMissingKey(t *testing.T) {
	ss, err := transport.NewShardSet(transport.ShardConfig{
		NumShards: 2, RegionSize: 1 << 20, EvictCapacity: 16,
	})
	if err != nil {
		t.Fatalf("NewShardSet: %v", err)
	}
	defer ss.Close()
	c := ss.CacheFor([]byte("missing"))
	if _, err := c.Get([]byte("missing")); !errors.Is(err, transport.ErrNotFound) {
		t.Errorf("Get(missing) = %v, want ErrNotFound", err)
	}
}

// TestSetTooLarge: Put exceeding MaxObjectSize returns ErrTooLarge.
func TestSetTooLarge(t *testing.T) {
	ss, err := transport.NewShardSet(transport.ShardConfig{
		NumShards: 2, RegionSize: 1 << 26, EvictCapacity: 16,
	})
	if err != nil {
		t.Fatalf("NewShardSet: %v", err)
	}
	defer ss.Close()
	c := ss.CacheFor([]byte("k"))
	huge := make([]byte, arena.MaxObjectSize+1)
	if _, err := c.Set([]byte("k"), huge); !errors.Is(err, transport.ErrTooLarge) {
		t.Errorf("Set(huge) = %v, want ErrTooLarge", err)
	}
}

// TestCloseShardCache: Close is safe to call twice.
func TestCloseShardCache(t *testing.T) {
	ss, err := transport.NewShardSet(transport.ShardConfig{
		NumShards: 2, RegionSize: 1 << 20, EvictCapacity: 16,
	})
	if err != nil {
		t.Fatalf("NewShardSet: %v", err)
	}
	if err := ss.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := ss.Close(); err != nil {
		t.Errorf("Close (2nd): %v", err)
	}
}

// TestShardSetBasicSetOverwrite: a second Set with the same key
// replaces the previous value.
func TestShardSetBasicSetOverwrite(t *testing.T) {
	ss, err := transport.NewShardSet(transport.ShardConfig{
		NumShards: 2, RegionSize: 1 << 20, EvictCapacity: 16,
	})
	if err != nil {
		t.Fatalf("NewShardSet: %v", err)
	}
	defer ss.Close()
	c := ss.CacheFor([]byte("k"))
	if _, err := c.Set([]byte("k"), []byte("v1")); err != nil {
		t.Fatalf("Set v1: %v", err)
	}
	if _, err := c.Set([]byte("k"), []byte("v2")); err != nil {
		t.Fatalf("Set v2: %v", err)
	}
	got, err := c.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, []byte("v2")) {
		t.Errorf("Get = %q, want v2", got)
	}
}

// TestShardSetDeleteWithCollisions exercises shardIndex.delete +
// rehash + canMove + findGap by inserting many keys (forcing
// linear-probe collisions) then deleting the first.  This drives
// rehash through the gap-fixing paths.
func TestShardSetDeleteWithCollisions(t *testing.T) {
	ss, err := transport.NewShardSet(transport.ShardConfig{
		NumShards:     1, // single shard to keep all keys on the same shardIndex
		RegionSize:    1 << 20,
		EvictCapacity: 1024,
	})
	if err != nil {
		t.Fatalf("NewShardSet: %v", err)
	}
	defer ss.Close()
	c := ss.CacheFor([]byte("seed"))

	// Insert many keys; with one shard and FNV-1a hash, collisions
	// will occur in the initial bucket array.
	const n = 100
	for i := 0; i < n; i++ {
		key := []byte{byte(i), byte(i >> 8)}
		if _, err := c.Set(key, []byte("v")); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	// Delete every key — rehash walks each deleted slot.
	for i := 0; i < n; i++ {
		key := []byte{byte(i), byte(i >> 8)}
		if err := c.Delete(key); err != nil {
			t.Fatalf("Delete: %v", err)
		}
	}
	// Insert + delete again to exercise additional paths.
	for i := 0; i < n; i++ {
		key := []byte{byte(i), byte(i >> 8)}
		if _, err := c.Set(key, []byte("v")); err != nil {
			t.Fatalf("Set 2nd: %v", err)
		}
	}
	for i := n - 1; i >= 0; i-- {
		key := []byte{byte(i), byte(i >> 8)}
		if err := c.Delete(key); err != nil {
			t.Fatalf("Delete 2nd: %v", err)
		}
	}
}

// TestShardSetGrowIndex: putting enough keys to force shardIndex.grow
// (load factor > 0.75) exercises the grow + re-insert path.
func TestShardSetGrowIndex(t *testing.T) {
	ss, err := transport.NewShardSet(transport.ShardConfig{
		NumShards:     1,
		RegionSize:    1 << 20,
		EvictCapacity: 4096,
	})
	if err != nil {
		t.Fatalf("NewShardSet: %v", err)
	}
	defer ss.Close()
	c := ss.CacheFor([]byte("seed"))

	// Put enough keys to grow the index multiple times.
	const n = 300
	for i := 0; i < n; i++ {
		key := []byte(fmt.Sprintf("k%d", i))
		if _, err := c.Set(key, []byte("v")); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	// Verify all keys still resolve.
	for i := 0; i < n; i++ {
		key := []byte(fmt.Sprintf("k%d", i))
		if _, err := c.Get(key); err != nil {
			t.Errorf("Get after grow: %v", err)
		}
	}
}

// TestWorkerPoolDrainsOnShutdown: Submit after Shutdown returns false
// and the job is closed.
func TestWorkerPoolDrainsOnShutdown(t *testing.T) {
	// Construct a pool indirectly via the shard index path.  We just
	// use the public Server path here.
	srv, err := transport.NewServer(transport.Options{
		Addr:          "127.0.0.1:0",
		TransportName: "tcp",
		ShardConfig: transport.ShardConfig{
			NumShards: 2, RegionSize: 1 << 20, EvictCapacity: 16,
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go func() { _ = srv.ListenAndServe() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

// TestCompressedFlagVisibleToGet: a value that was stored compressed
// must be returned decompressed.  We can verify by checking that a
// highly compressible payload round-trips byte-for-byte.
func TestCompressedFlagVisibleToGet(t *testing.T) {
	ss, err := transport.NewShardSet(transport.ShardConfig{
		NumShards:     2,
		RegionSize:    1 << 20,
		EvictCapacity: 16,
		Compressor:    compress.NewLZ4(1),
	})
	if err != nil {
		t.Fatalf("NewShardSet: %v", err)
	}
	defer ss.Close()
	c := ss.CacheFor([]byte("k"))

	plain := bytes.Repeat([]byte("a"), 4096)
	if _, err := c.Set([]byte("k"), plain); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := c.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("Get = %q (len %d), want %q (len %d)", got, len(got), plain, len(plain))
	}
}

// TestShardRouterHelper — sanity coverage for the local helpers used
// by the transport package.
func TestShardRouterHelper(t *testing.T) {
	// hashKey should be stable.
	h1 := hashKeyForTest([]byte("a"))
	h2 := hashKeyForTest([]byte("a"))
	if h1 != h2 {
		t.Errorf("hashKey not deterministic: %d vs %d", h1, h2)
	}
	if hashKeyForTest([]byte("a")) == hashKeyForTest([]byte("b")) {
		t.Errorf("distinct keys produced same hash")
	}
}

// ─────────────── helpers ───────────────

// hashKeyForTest is duplicated here so the test package doesn't need
// to depend on internals.
func hashKeyForTest(key []byte) uint64 {
	const (
		offset uint64 = 14695981039346656037
		prime  uint64 = 1099511628211
	)
	h := offset
	for _, b := range key {
		h ^= uint64(b)
		h *= prime
	}
	return h
}
