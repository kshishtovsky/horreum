package ds

import (
	"bytes"
	"testing"
)

func TestHash(t *testing.T) {
	var raw []byte
	var updated bool

	// HSet field1 = val1
	raw, updated = HSet(raw, []byte("name"), []byte("Alice"))
	if !updated {
		t.Fatal("expected new field update to be true")
	}

	// HGet field1
	val, ok := HGet(raw, []byte("name"))
	if !ok || !bytes.Equal(val, []byte("Alice")) {
		t.Fatalf("expected Alice, got %s", string(val))
	}

	// Update existing field
	raw, updated = HSet(raw, []byte("name"), []byte("Bob"))
	if updated {
		t.Fatal("expected existing field update to be false")
	}
	val, ok = HGet(raw, []byte("name"))
	if !ok || !bytes.Equal(val, []byte("Bob")) {
		t.Fatalf("expected Bob, got %s", string(val))
	}

	// HSet field2 = val2
	raw, _ = HSet(raw, []byte("age"), []byte("30"))
	fields, values := HGetAll(raw)
	if len(fields) != 2 || len(values) != 2 {
		t.Fatalf("expected 2 fields/values, got %d", len(fields))
	}

	// HDel
	raw, deleted := HDel(raw, []byte("name"))
	if !deleted {
		t.Fatal("expected HDel to be true")
	}
	_, ok = HGet(raw, []byte("name"))
	if ok {
		t.Fatal("expected name field to be deleted")
	}
}

func TestList(t *testing.T) {
	var raw []byte

	raw = LPush(raw, []byte("b"))
	raw = LPush(raw, []byte("a"))
	raw = RPush(raw, []byte("c"))

	if LLen(raw) != 3 {
		t.Fatalf("expected length 3, got %d", LLen(raw))
	}

	// Pop left: "a"
	var popped []byte
	var ok bool
	raw, popped, ok = LPop(raw)
	if !ok || !bytes.Equal(popped, []byte("a")) {
		t.Fatalf("expected a, got %s", string(popped))
	}

	// Pop right: "c"
	raw, popped, ok = RPop(raw)
	if !ok || !bytes.Equal(popped, []byte("c")) {
		t.Fatalf("expected c, got %s", string(popped))
	}

	// Pop left: "b"
	raw, popped, ok = LPop(raw)
	if !ok || !bytes.Equal(popped, []byte("b")) {
		t.Fatalf("expected b, got %s", string(popped))
	}

	if LLen(raw) != 0 {
		t.Fatalf("expected empty list, got %d", LLen(raw))
	}
}

func TestSet(t *testing.T) {
	var raw []byte
	var added bool

	raw, added = SAdd(raw, []byte("item1"))
	if !added {
		t.Fatal("expected added true")
	}
	raw, added = SAdd(raw, []byte("item1"))
	if added {
		t.Fatal("expected duplicate add false")
	}
	raw, _ = SAdd(raw, []byte("item2"))

	if !SIsMember(raw, []byte("item1")) || !SIsMember(raw, []byte("item2")) {
		t.Fatal("expected members to exist")
	}
	if SIsMember(raw, []byte("item3")) {
		t.Fatal("expected item3 to not exist")
	}

	members := SMembers(raw)
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(members))
	}

	var removed bool
	raw, removed = SRem(raw, []byte("item1"))
	if !removed {
		t.Fatal("expected SRem true")
	}
	if SIsMember(raw, []byte("item1")) {
		t.Fatal("expected item1 to be removed")
	}
}
