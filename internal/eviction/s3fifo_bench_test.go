// Benchmarks for S3-FIFO eviction.
//
// Acceptance gates from the task spec:
//   - BenchmarkTouch → 0 allocs/op.
//   - BenchmarkAdd → allocations only on grow of the evicted slice.
//
// Run via:
//
//	go test -bench=. -benchmem ./internal/eviction/...
package eviction_test

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/kshishtovsky/horreum/internal/arena"
	"github.com/kshishtovsky/horreum/internal/eviction"
)

// benchSetup prepares an arena + eviction pair for benchmarks.
func benchSetup(b *testing.B) (*arena.Manager, *eviction.Eviction) {
	b.Helper()
	mgr, err := arena.NewManager(1<<26, false)
	if err != nil {
		b.Fatalf("NewManager: %v", err)
	}
	b.Cleanup(func() { _ = mgr.Close() })
	ev := eviction.New(mgr, 1024)
	return mgr, ev
}

// BenchmarkTouch: cache-hit hot path.  Must be 0 allocs/op.
func BenchmarkTouch(b *testing.B) {
	mgr, ev := benchSetup(b)
	h, err := mgr.Put([]byte("payload"))
	if err != nil {
		b.Fatalf("Put: %v", err)
	}
	if err := ev.Add([]byte("hot"), h); err != nil {
		// Add returns []arena.Handle which we ignore here.
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		ev.Touch(h)
	}
}

// BenchmarkAdd: SET path.  Allocations only on grow of evicted slice.
func BenchmarkAdd(b *testing.B) {
	mgr, ev := benchSetup(b)

	// Pre-fill so evictions occur, exercising the slow path.
	const prefill = 2048
	for i := 0; i < prefill; i++ {
		h, _ := mgr.Put([]byte("v"))
		ev.Add([]byte(fmt.Sprintf("seed-%d", i)), h)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h, _ := mgr.Put([]byte("v"))
		key := []byte(fmt.Sprintf("k-%d", i))
		ev.Add(key, h)
	}
}

// BenchmarkAdd_NoEvict: SET path with no evictions (cache large enough).
func BenchmarkAdd_NoEvict(b *testing.B) {
	mgr, _ := benchSetup(b)
	ev := eviction.New(mgr, 1<<16) // 64 Ki slots — no evictions expected

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h, _ := mgr.Put([]byte("v"))
		key := []byte(fmt.Sprintf("k-%d", i))
		ev.Add(key, h)
	}
}

// BenchmarkDelete: explicit DEL.  O(N) on ring buffer, slow path.
func BenchmarkDelete(b *testing.B) {
	mgr, ev := benchSetup(b)
	const n = 1024
	keys := make([][]byte, n)
	handles := make([]arena.Handle, n)
	for i := 0; i < n; i++ {
		keys[i] = []byte(fmt.Sprintf("k-%d", i))
		handles[i], _ = mgr.Put([]byte("v"))
		ev.Add(keys[i], handles[i])
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		ev.Delete(keys[i%n], handles[i%n])
		// re-insert so the cache stays populated
		h, _ := mgr.Put([]byte("v"))
		ev.Add(keys[i%n], h)
		handles[i%n] = h
	}
}

// BenchmarkTouchParallel: concurrent Touch (lock-free hot path).
func BenchmarkTouchParallel(b *testing.B) {
	mgr, ev := benchSetup(b)
	const n = 64
	handles := make([]arena.Handle, n)
	for i := 0; i < n; i++ {
		h, _ := mgr.Put([]byte("v"))
		ev.Add([]byte(fmt.Sprintf("k-%d", i)), h)
		handles[i] = h
	}
	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		rng := rand.New(rand.NewSource(1))
		for pb.Next() {
			ev.Touch(handles[rng.Intn(n)])
		}
	})
}

// BenchmarkAddParallel: concurrent Add (slow path with mutex).
func BenchmarkAddParallel(b *testing.B) {
	mgr, ev := benchSetup(b)
	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			h, _ := mgr.Put([]byte("v"))
			ev.Add([]byte(fmt.Sprintf("k-%d", i)), h)
			i++
		}
	})
}
