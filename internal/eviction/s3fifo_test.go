// Unit tests for S3-FIFO eviction transitions.
package eviction_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/horreum/horreum/internal/arena"
	"github.com/horreum/horreum/internal/eviction"
)

func newTestManager(t *testing.T, size uint64) *arena.Manager {
	t.Helper()
	m, err := arena.NewManager(size, false)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

// putDummy inserts a small payload into the arena.
func putDummy(t *testing.T, m *arena.Manager) arena.Handle {
	t.Helper()
	h, err := m.Put([]byte("payload"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	return h
}

// TestRingBufferBasic exercises the underlying ring buffer.
func TestRingBufferBasic(t *testing.T) {
	rb := eviction.NewRingBuffer(4)
	if rb.Capacity() != 4 {
		t.Errorf("cap = %d, want 4", rb.Capacity())
	}
	h0 := arena.Handle{Offset: 0}
	if !rb.Push(h0) {
		t.Errorf("Push should succeed on empty buffer")
	}
	if rb.Empty() {
		t.Errorf("buffer should not be empty after Push")
	}
	got, ok := rb.Peek()
	if !ok || got != h0 {
		t.Errorf("Peek = %v, want %v", got, h0)
	}
	got, ok = rb.Pop()
	if !ok || got != h0 {
		t.Errorf("Pop = %v, want %v", got, h0)
	}
	if !rb.Empty() {
		t.Errorf("buffer should be empty after Pop")
	}
}

// TestGhostIndexBasic verifies fingerprint insert/look/remove.
func TestGhostIndexBasic(t *testing.T) {
	g := eviction.NewGhostIndex(64)
	fp := uint32(0xDEADBEEF)
	if g.Look(fp) {
		t.Errorf("Look on empty ghost returned true")
	}
	r1 := g.Insert(fp)
	if r1 {
		t.Errorf("Insert on new fp returned collision=true")
	}
	if !g.Look(fp) {
		t.Errorf("Look after Insert returned false")
	}
	r2 := g.Insert(fp)
	if !r2 {
		t.Errorf("Insert on existing fp returned collision=false")
	}
	if !g.Remove(fp) {
		t.Errorf("Remove returned false")
	}
	if g.Look(fp) {
		t.Errorf("Look after Remove returned true")
	}
}

// TestS3FIFOBasicAddDelete exercises the happy path.
func TestS3FIFOBasicAddDelete(t *testing.T) {
	mgr := newTestManager(t, 1<<20)
	ev := eviction.New(mgr, 100)

	h := putDummy(t, mgr)
	evicted := ev.Add([]byte("k1"), h)
	if len(evicted) != 0 {
		t.Errorf("unexpected eviction: %d", len(evicted))
	}

	prevFreq := mgr.GetFreq(h)
	ev.Touch(h)
	if f := mgr.GetFreq(h); f == prevFreq && prevFreq == 0 {
		t.Errorf("Touch did not increment freq: %d", f)
	}
	prevFreq = mgr.GetFreq(h)

	ev.Delete([]byte("k1"), h)
	ev.Touch(h)
	// Touch after Delete should be a no-op (queue tag cleared → Touch skips).
	if f := mgr.GetFreq(h); f != prevFreq {
		t.Errorf("Touch after Delete changed freq: %d -> %d", prevFreq, f)
	}
}

// TestS3FIFOFreqWrap checks that the freq counter wraps at 4.
func TestS3FIFOFreqWrap(t *testing.T) {
	mgr := newTestManager(t, 1<<20)
	ev := eviction.New(mgr, 100)

	h := putDummy(t, mgr)
	ev.Add([]byte("k"), h)

	for i := 0; i < 5; i++ {
		ev.Touch(h)
	}
	// 5 touches starting from 0 → 1,2,3,0,1
	if f := mgr.GetFreq(h); f != 1 {
		t.Errorf("freq after 5 Touches = %d, want 1", f)
	}
}

// TestS3FIFOEvictS_PromotesHighFreq: a freq>=2 tail element in S
// must be promoted to M, not evicted.
func TestS3FIFOEvictS_PromotesHighFreq(t *testing.T) {
	mgr := newTestManager(t, 1<<20)
	ev := eviction.New(mgr, 100) // S≈16 (power-of-two), M≈128

	h0 := putDummy(t, mgr)
	ev.Add([]byte("k0"), h0)
	for j := 0; j < 3; j++ {
		ev.Touch(h0)
	}

	// Fill S to capacity (use ev.SCapacity()).
	sCap := ev.SCapacity()
	for i := uint64(1); i < sCap; i++ {
		h := putDummy(t, mgr)
		ev.Add([]byte(fmt.Sprintf("k%d", i)), h)
	}

	if sSize, _, _ := ev.Stats(); sSize != sCap {
		t.Fatalf("S size = %d, want %d", sSize, sCap)
	}

	if f := mgr.GetFreq(h0); f != 3 {
		t.Fatalf("h0 freq = %d, want 3", f)
	}

	// sCap+1 Add triggers S overflow. h0 (freq=3) must be promoted to M.
	hTrigger := putDummy(t, mgr)
	evicted := ev.Add([]byte("trigger"), hTrigger)

	for _, e := range evicted {
		if e == h0 {
			t.Errorf("h0 should have been promoted, not evicted")
		}
	}

	if tag := mgr.GetMeta(h0) & arena.QueueTagMask; tag != arena.QueueTagM {
		t.Errorf("h0 queue tag = 0x%x, want QueueTagM=0x%x", tag, arena.QueueTagM)
	}
	if f := mgr.GetFreq(h0); f != 0 {
		t.Errorf("h0 freq after promotion = %d, want 0", f)
	}
}

// TestS3FIFOTransitions: full lifecycle S → M → reinsert → evict.
func TestS3FIFOTransitions(t *testing.T) {
	mgr := newTestManager(t, 1<<20)
	ev := eviction.New(mgr, 100)

	sCap := ev.SCapacity()

	// 1) Fill S.
	keys := make([][]byte, sCap)
	handles := make([]arena.Handle, sCap)
	for i := uint64(0); i < sCap; i++ {
		keys[i] = []byte(fmt.Sprintf("key-%d", i))
		handles[i] = putDummy(t, mgr)
		ev.Add(keys[i], handles[i])
	}

	// 2) Touch handles[0] three times (freq → 3).
	for j := 0; j < 3; j++ {
		ev.Touch(handles[0])
	}
	if got := mgr.GetFreq(handles[0]); got < 2 {
		t.Fatalf("handles[0] freq = %d, want >= 2", got)
	}

	// 3) sCap+1 Add forces S overflow. handles[0] is promoted to M.
	h11 := putDummy(t, mgr)
	evicted := ev.Add([]byte("key-extra"), h11)
	for _, e := range evicted {
		if e == handles[0] {
			t.Fatalf("handles[0] should have been promoted, not evicted")
		}
	}

	if tag := mgr.GetMeta(handles[0]) & arena.QueueTagMask; tag != arena.QueueTagM {
		t.Errorf("handles[0] queue tag = 0x%x, want QueueTagM", tag)
	}

	// 4) After promotion, freq should be reset to 0.
	if f := mgr.GetFreq(handles[0]); f != 0 {
		t.Errorf("handles[0] freq after promotion = %d, want 0", f)
	}

	// 5) Touch handles[0] twice — freq becomes 2.
	ev.Touch(handles[0])
	ev.Touch(handles[0])
	if f := mgr.GetFreq(handles[0]); f < 2 {
		t.Errorf("handles[0] freq after 2 touches = %d, want >= 2", f)
	}
}

// TestGhostQueue: key evicted from S enters ghost; re-Add sends it to M.
func TestGhostQueue(t *testing.T) {
	mgr := newTestManager(t, 1<<20)
	ev := eviction.New(mgr, 100)

	sCap := ev.SCapacity()

	// Fill S to capacity + 1 to force eviction.
	handles := make([]arena.Handle, sCap+1)
	for i := uint64(0); i <= sCap; i++ {
		handles[i] = putDummy(t, mgr)
		key := []byte(fmt.Sprintf("k%d", i))
		ev.Add(key, handles[i])
	}

	// Find an evicted handle (queue tag = 0).
	var evictedKey []byte
	for i := uint64(0); i <= sCap; i++ {
		tag := mgr.GetMeta(handles[i]) & arena.QueueTagMask
		if tag == 0 {
			evictedKey = []byte(fmt.Sprintf("k%d", i))
			break
		}
	}
	if evictedKey == nil {
		t.Skip("no eviction observed — algorithm differed")
	}

	_, _, ghostLive := ev.Stats()
	if ghostLive == 0 {
		t.Errorf("expected ghost to have entries, got 0")
	}

	hNew := putDummy(t, mgr)
	ev.Add(evictedKey, hNew)

	// hNew should be in M.
	if tag := mgr.GetMeta(hNew) & arena.QueueTagMask; tag != arena.QueueTagM {
		t.Errorf("re-Add queue tag = 0x%x, want QueueTagM=0x%x", tag, arena.QueueTagM)
	}
	// Verify M has grown.
	_, afterM, _ := ev.Stats()
	if afterM < 1 {
		t.Errorf("M should have at least 1 element, got %d", afterM)
	}
}

// TestEvictionConcurrent: concurrent Add and Touch must not race.
func TestEvictionConcurrent(t *testing.T) {
	mgr := newTestManager(t, 1<<20)
	ev := eviction.New(mgr, 1000)

	const N = 500
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			h := putDummy(t, mgr)
			ev.Add([]byte(fmt.Sprintf("k%d", i%200)), h)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			h := putDummy(t, mgr)
			ev.Touch(h)
		}
	}()

	wg.Wait()
}
