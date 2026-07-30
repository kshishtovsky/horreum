package transport

import (
	"bytes"
	"testing"

	"github.com/kshishtovsky/horreum/internal/compress"
	"github.com/kshishtovsky/horreum/internal/encrypt"
)

func TestComplexDataStructures(t *testing.T) {
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

	keyHash := []byte("user:100")

	// 1. Hash Operations (HSet, HGet, HDel, HGetAll)
	cache := ss.CacheFor(keyHash)
	updated, err := cache.HSet(keyHash, []byte("name"), []byte("Alice"))
	if err != nil || !updated {
		t.Fatalf("HSet failed: %v, updated=%v", err, updated)
	}
	_, _ = cache.HSet(keyHash, []byte("email"), []byte("alice@example.com"))

	val, err := cache.HGet(keyHash, []byte("name"))
	if err != nil || !bytes.Equal(val, []byte("Alice")) {
		t.Fatalf("HGet failed: expected Alice, got %s (err=%v)", string(val), err)
	}

	fields, values, err := cache.HGetAll(keyHash)
	if err != nil || len(fields) != 2 || len(values) != 2 {
		t.Fatalf("HGetAll failed: got %d fields, %d values (err=%v)", len(fields), len(values), err)
	}

	deleted, err := cache.HDel(keyHash, []byte("name"))
	if err != nil || !deleted {
		t.Fatalf("HDel failed: %v", err)
	}
	_, err = cache.HGet(keyHash, []byte("name"))
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after HDel, got %v", err)
	}

	// 2. List Operations (LPush, RPush, LPop, RPop, LLen)
	keyList := []byte("queue:tasks")
	cList := ss.CacheFor(keyList)
	l1, _ := cList.LPush(keyList, []byte("task2"))
	l2, _ := cList.LPush(keyList, []byte("task1"))
	l3, _ := cList.RPush(keyList, []byte("task3"))
	if l3 != 3 {
		t.Fatalf("expected list length 3, got %d (l1=%d, l2=%d)", l3, l1, l2)
	}

	poppedHead, err := cList.LPop(keyList)
	if err != nil || !bytes.Equal(poppedHead, []byte("task1")) {
		t.Fatalf("LPop expected task1, got %s", string(poppedHead))
	}

	poppedTail, err := cList.RPop(keyList)
	if err != nil || !bytes.Equal(poppedTail, []byte("task3")) {
		t.Fatalf("RPop expected task3, got %s", string(poppedTail))
	}

	remainingLen, _ := cList.LLen(keyList)
	if remainingLen != 1 {
		t.Fatalf("expected 1 remaining task, got %d", remainingLen)
	}

	// 3. Set Operations (SAdd, SRem, SIsMember, SMembers)
	keySet := []byte("tags:golang")
	cSet := ss.CacheFor(keySet)
	added, err := cSet.SAdd(keySet, []byte("high-perf"))
	if err != nil || !added {
		t.Fatalf("SAdd failed: %v", err)
	}
	_, _ = cSet.SAdd(keySet, []byte("zero-alloc"))

	isMember, err := cSet.SIsMember(keySet, []byte("high-perf"))
	if err != nil || !isMember {
		t.Fatalf("SIsMember failed: %v", err)
	}

	members, err := cSet.SMembers(keySet)
	if err != nil || len(members) != 2 {
		t.Fatalf("SMembers failed: got %d members", len(members))
	}

	removed, err := cSet.SRem(keySet, []byte("high-perf"))
	if err != nil || !removed {
		t.Fatalf("SRem failed: %v", err)
	}
	isMember, _ = cSet.SIsMember(keySet, []byte("high-perf"))
	if isMember {
		t.Fatalf("high-perf should no longer be a set member")
	}
}
