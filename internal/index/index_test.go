package index

import (
	"fmt"
	"testing"

	"github.com/horreum/horreum/internal/arena"
)

func TestPutGet(t *testing.T) {
	h := New(16)
	hd := arena.Handle{Offset: 100, Size: 50, Region: 0}
	h.Put([]byte("key1"), hd)
	got, ok := h.Get([]byte("key1"))
	if !ok {
		t.Fatal("Get returned false")
	}
	if got != hd {
		t.Errorf("Get = %v, want %v", got, hd)
	}
}

func TestGetMissing(t *testing.T) {
	h := New(16)
	_, ok := h.Get([]byte("missing"))
	if ok {
		t.Error("Get(missing) returned true")
	}
}

func TestPutUpdate(t *testing.T) {
	h := New(16)
	hd1 := arena.Handle{Offset: 100, Size: 50, Region: 0}
	hd2 := arena.Handle{Offset: 200, Size: 60, Region: 0}
	h.Put([]byte("key1"), hd1)
	evicted := h.Put([]byte("key1"), hd2)
	if !evicted {
		t.Error("expected evicted=true on update")
	}
	got, _ := h.Get([]byte("key1"))
	if got != hd2 {
		t.Errorf("Get = %v, want %v", got, hd2)
	}
}

func TestDelete(t *testing.T) {
	h := New(16)
	hd := arena.Handle{Offset: 100, Size: 50, Region: 0}
	h.Put([]byte("key1"), hd)
	deleted, ok := h.Delete([]byte("key1"))
	if !ok {
		t.Fatal("Delete returned false")
	}
	if deleted != hd {
		t.Errorf("Delete = %v, want %v", deleted, hd)
	}
	_, ok = h.Get([]byte("key1"))
	if ok {
		t.Error("Get after Delete returned true")
	}
}

func TestDeleteMissing(t *testing.T) {
	h := New(16)
	_, ok := h.Delete([]byte("missing"))
	if ok {
		t.Error("Delete(missing) returned true")
	}
}

func TestGrow(t *testing.T) {
	h := New(4) // Small initial capacity.
	for i := 0; i < 100; i++ {
		key := []byte(fmt.Sprintf("key-%d", i))
		hd := arena.Handle{Offset: uint32(i * 10), Size: 10, Region: 0}
		h.Put(key, hd)
	}
	for i := 0; i < 100; i++ {
		key := []byte(fmt.Sprintf("key-%d", i))
		got, ok := h.Get(key)
		if !ok {
			t.Errorf("Get(key-%d) = false", i)
		}
		want := arena.Handle{Offset: uint32(i * 10), Size: 10, Region: 0}
		if got != want {
			t.Errorf("Get(key-%d) = %v, want %v", i, got, want)
		}
	}
}

func TestOverwrite(t *testing.T) {
	h := New(16)
	for i := 0; i < 50; i++ {
		hd := arena.Handle{Offset: uint32(i), Size: 1, Region: 0}
		h.Put([]byte("same-key"), hd)
	}
	got, ok := h.Get([]byte("same-key"))
	if !ok {
		t.Fatal("Get returned false")
	}
	if got.Offset != 49 {
		t.Errorf("Get.Offset = %d, want 49", got.Offset)
	}
}

func BenchmarkIndexGet(b *testing.B) {
	h := New(1024)
	// Pre-allocate keys to measure only Get's allocation behavior.
	keys := make([][]byte, 1000)
	for i := 0; i < 1000; i++ {
		keys[i] = []byte(fmt.Sprintf("key-%d", i))
		hd := arena.Handle{Offset: uint32(i * 10), Size: 10, Region: 0}
		h.Put(keys[i], hd)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h.Get(keys[i%1000])
	}
}

func BenchmarkIndexPut(b *testing.B) {
	h := New(b.N + 1)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		key := []byte(fmt.Sprintf("key-%d", i))
		hd := arena.Handle{Offset: uint32(i), Size: 10, Region: 0}
		h.Put(key, hd)
	}
}
