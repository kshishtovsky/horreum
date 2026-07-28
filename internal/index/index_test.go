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

// TestSnapshotEmpty verifies Snapshot returns nil/empty on a fresh index.
func TestSnapshotEmpty(t *testing.T) {
	h := New(8)
	snap := h.Snapshot()
	if len(snap) != 0 {
		t.Errorf("empty Snapshot: got %v entries, want 0", len(snap))
	}
}

// TestSnapshotAll verifies Snapshot returns exactly count entries, all live.
func TestSnapshotAll(t *testing.T) {
	h := New(16)
	const n = 50
	for i := 0; i < n; i++ {
		key := []byte(fmt.Sprintf("k%d", i))
		hd := arena.Handle{Offset: uint32(i), Size: 8, Region: 0}
		h.Put(key, hd)
	}
	snap := h.Snapshot()
	if len(snap) != n {
		t.Fatalf("Snapshot len = %d, want %d", len(snap), n)
	}
	seen := make(map[string]bool, n)
	for _, se := range snap {
		seen[string(se.Key)] = true
	}
	for i := 0; i < n; i++ {
		k := fmt.Sprintf("k%d", i)
		if !seen[k] {
			t.Errorf("Snapshot missing key %q", k)
		}
	}
}

// TestSnapshotAfterDelete verifies deleted entries are excluded.
func TestSnapshotAfterDelete(t *testing.T) {
	h := New(16)
	for i := 0; i < 10; i++ {
		key := []byte(fmt.Sprintf("k%d", i))
		hd := arena.Handle{Offset: uint32(i), Size: 1, Region: 0}
		h.Put(key, hd)
	}
	h.Delete([]byte("k3"))
	h.Delete([]byte("k7"))
	snap := h.Snapshot()
	if len(snap) != 8 {
		t.Fatalf("Snapshot len after deletes = %d, want 8", len(snap))
	}
	for _, se := range snap {
		k := string(se.Key)
		if k == "k3" || k == "k7" {
			t.Errorf("Snapshot contains deleted key %q", k)
		}
	}
}

// TestAddInsert verifies Add inserts without grow and returns true.
func TestAddInsert(t *testing.T) {
	h := NewForCount(32)
	if !h.Add([]byte("a"), arena.Handle{Offset: 10, Size: 5, Region: 0}) {
		t.Fatal("Add returned false")
	}
	if h.Count() != 1 {
		t.Errorf("Count after Add = %d, want 1", h.Count())
	}
	got, ok := h.Get([]byte("a"))
	if !ok {
		t.Fatal("Get returned false")
	}
	if got.Offset != 10 {
		t.Errorf("Get.Offset = %d, want 10", got.Offset)
	}
}

// TestAddDuplicate verifies Add returns false on a duplicate key.
func TestAddDuplicate(t *testing.T) {
	h := NewForCount(64)
	if !h.Add([]byte("dup-key"), arena.Handle{Offset: 1, Size: 5, Region: 0}) {
		t.Fatal("first Add returned false")
	}
	if h.Add([]byte("dup-key"), arena.Handle{}) {
		t.Error("second Add of duplicate returned true; want false")
	}
	if h.Count() != 1 {
		t.Errorf("Count after duplicate Add = %d, want 1", h.Count())
	}
}

// TestSnapshotRoundTrip verifies Snapshot → fresh HashIndex → Add reproduces
// the original index contents.
func TestSnapshotRoundTrip(t *testing.T) {
	src := New(16)
	const n = 100
	want := make(map[string]arena.Handle, n)
	for i := 0; i < n; i++ {
		key := []byte(fmt.Sprintf("rk%d", i))
		hd := arena.Handle{Offset: uint32(i * 32), Size: 16, Region: 0}
		src.Put(key, hd)
		want[string(key)] = hd
	}

	snap := src.Snapshot()
	if len(snap) != n {
		t.Fatalf("Snapshot len = %d, want %d", len(snap), n)
	}

	dst := NewForCount(n)
	for _, se := range snap {
		if !dst.Add(se.Key, se.Handle) {
			t.Errorf("Add(%q) returned false", string(se.Key))
		}
	}

	if dst.Count() != n {
		t.Errorf("dst.Count = %d, want %d", dst.Count(), n)
	}
	for k, hd := range want {
		got, ok := dst.Get([]byte(k))
		if !ok {
			t.Errorf("dst.Get(%q) = false", k)
			continue
		}
		if got != hd {
			t.Errorf("dst.Get(%q) = %v, want %v", k, got, hd)
		}
	}
}

// TestNewForCountSizes verifies NewForCount returns a power-of-two mask
// at least 2x the requested count.
func TestNewForCountSizes(t *testing.T) {
	for _, n := range []int{0, 1, 8, 17, 100, 1000} {
		h := NewForCount(n)
		// mask+1 must be a power of two >= max(8, 2n).
		cap := h.mask + 1
		if cap&(cap-1) != 0 {
			t.Errorf("NewForCount(%d): cap=%d is not a power of two", n, cap)
		}
		want := uint64(8)
		if 2*uint64(n) > want {
			want = 2 * uint64(n)
		}
		if cap < want {
			t.Errorf("NewForCount(%d): cap=%d, want >= %d", n, cap, want)
		}
	}
}

func BenchmarkIndexGet(b *testing.B) {
	h := New(1024)
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
