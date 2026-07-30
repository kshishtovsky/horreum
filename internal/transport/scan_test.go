package transport

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/horreum/horreum/internal/compress"
	"github.com/horreum/horreum/internal/encrypt"
)

func TestScanAndDelPrefix(t *testing.T) {
	cfg := ShardConfig{
		NumShards:     4,
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

	// 1. Populate keys with prefixes "user:" and "order:"
	for i := 0; i < 50; i++ {
		key := []byte(fmt.Sprintf("user:%d", i))
		val := []byte(fmt.Sprintf("val:%d", i))
		cache := ss.CacheFor(key)
		if _, err := cache.Set(key, val, 0); err != nil {
			t.Fatalf("failed to set %s: %v", string(key), err)
		}
	}
	for i := 0; i < 20; i++ {
		key := []byte(fmt.Sprintf("order:%d", i))
		val := []byte(fmt.Sprintf("val:%d", i))
		cache := ss.CacheFor(key)
		if _, err := cache.Set(key, val, 0); err != nil {
			t.Fatalf("failed to set %s: %v", string(key), err)
		}
	}

	// 2. Scan "user:" prefix in batches of 10
	var foundKeys [][]byte
	var cursor uint64
	for {
		keys, nextCursor, err := ss.Scan([]byte("user:"), cursor, 10)
		if err != nil {
			t.Fatalf("scan error: %v", err)
		}
		foundKeys = append(foundKeys, keys...)
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	if len(foundKeys) != 50 {
		t.Fatalf("expected 50 user: keys, got %d", len(foundKeys))
	}
	for _, k := range foundKeys {
		if !bytes.HasPrefix(k, []byte("user:")) {
			t.Fatalf("key %s does not have user: prefix", string(k))
		}
	}

	// 3. DelPrefix "user:"
	deletedCount, err := ss.DelPrefix([]byte("user:"))
	if err != nil {
		t.Fatalf("DelPrefix error: %v", err)
	}
	if deletedCount != 50 {
		t.Fatalf("expected 50 deleted user keys, got %d", deletedCount)
	}

	// 4. Verify "user:" keys are gone, but "order:" keys remain
	for i := 0; i < 50; i++ {
		key := []byte(fmt.Sprintf("user:%d", i))
		cache := ss.CacheFor(key)
		if _, err := cache.Get(key); err == nil {
			t.Fatalf("key %s should have been deleted", string(key))
		}
	}

	for i := 0; i < 20; i++ {
		key := []byte(fmt.Sprintf("order:%d", i))
		cache := ss.CacheFor(key)
		if _, err := cache.Get(key); err != nil {
			t.Fatalf("key %s should still exist", string(key))
		}
	}
}
