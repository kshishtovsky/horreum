package transport

import (
	"bytes"
	"testing"

	"github.com/horreum/horreum/internal/compress"
	"github.com/horreum/horreum/internal/encrypt"
)

func TestCASAndIncr(t *testing.T) {
	cfg := ShardConfig{
		NumShards:     2,
		RegionSize:    1024 * 1024,
		EvictCapacity: 1000,
		Compressor:    compress.Noop{},
		Cipher:        encrypt.Noop{},
	}
	ss, err := NewShardSet(cfg)
	if err != nil {
		t.Fatalf("failed to create ShardSet: %v", err)
	}
	defer ss.Close()

	key := []byte("counter")
	cache := ss.CacheFor(key)

	// Incr non-existent key
	val, err := cache.Incr(key, 10)
	if err != nil || val != 10 {
		t.Fatalf("Incr failed: val=%d, err=%v", val, err)
	}

	// Incr existing key
	val, err = cache.Incr(key, 5)
	if err != nil || val != 15 {
		t.Fatalf("Incr expected 15, got %d, err=%v", val, err)
	}

	// CAS match
	casKey := []byte("casKey")
	cCache := ss.CacheFor(casKey)
	_, _ = cCache.Set(casKey, []byte("v1"), 0)

	cur, swapped, err := cCache.CAS(casKey, []byte("v1"), []byte("v2"))
	if err != nil || !swapped {
		t.Fatalf("CAS expected true, got swapped=%v, cur=%s, err=%v", swapped, string(cur), err)
	}

	// CAS mismatch
	cur, swapped, err = cCache.CAS(casKey, []byte("v1"), []byte("v3"))
	if err != nil || swapped || !bytes.Equal(cur, []byte("v2")) {
		t.Fatalf("CAS mismatch expected false with cur v2, got swapped=%v, cur=%s, err=%v", swapped, string(cur), err)
	}
}
