// Package eviction implements S3-FIFO — a three-zone LRU replacement policy.
//
// S (Small): ~10% capacity. New keys land here. One-hit-wonders get evicted
//
//	quickly to the ghost index.
//
// M (Main):  ~90% capacity. Promoted-from-S keys and "in ghost" keys land
//
//	here directly. Frequently-used keys survive via reinsertion.
//
// G (Ghost): size == M. Stores 4-byte fingerprints of keys evicted from S.
//
//	Re-insertion of a ghosted key sends it to M directly.
//
// Frequency counter (2 bits, 0..3) lives in Handle.Meta.
// Queue tag (bits 2-3) also lives in Handle.Meta.
// Both are accessed via arena.Manager atomics.
//
// Touch is lock-free (atomic reads + arena atomic IncFreq).
// Add and Delete take a mutex.
package eviction

import (
	"sync"

	"github.com/kshishtovsky/horreum/internal/arena"
)

// smallPct is the percentage of total capacity reserved for the S queue.
const smallPct = 10

// Eviction is the S3-FIFO policy.
type Eviction struct {
	mgr *arena.Manager
	mu  sync.Mutex
	sq  *RingBuffer
	mq  *RingBuffer
	gh  *GhostIndex
}

// New creates an S3-FIFO with the given total capacity.
// S queue is ~10% of capacity, M queue is the rest.
// Ghost index size equals the M queue size.
func New(mgr *arena.Manager, capacity uint64) *Eviction {
	if capacity == 0 {
		panic("capacity must be > 0")
	}
	sCap := capacity * smallPct / 100
	if sCap == 0 {
		sCap = 1
	}
	mCap := capacity - sCap
	if mCap == 0 {
		mCap = 1
	}
	return &Eviction{
		mgr: mgr,
		sq:  NewRingBuffer(sCap),
		mq:  NewRingBuffer(mCap),
		gh:  NewGhostIndex(mCap),
	}
}

// Capacity returns the total capacity (S + M).
func (e *Eviction) Capacity() uint64 {
	return e.sq.Capacity() + e.mq.Capacity()
}

// SCapacity returns the S queue capacity.
func (e *Eviction) SCapacity() uint64 {
	return e.sq.Capacity()
}

// MCapacity returns the M queue capacity.
func (e *Eviction) MCapacity() uint64 {
	return e.mq.Capacity()
}

// Add inserts a new key. If the key's fingerprint is in the ghost,
// the handle goes directly to M; otherwise to S.  Returns evicted handles
// (caller must pass them to mgr.Free).
//
// Holds e.mu; not lock-free.
func (e *Eviction) Add(key []byte, h arena.Handle) []arena.Handle {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.gh.Look(fingerprintOf(key)) {
		// Re-insert: send to M.
		if !e.mq.Push(h) {
			return e.evictM()
		}
		e.mgr.SetFreq(h, 0)
		e.mgr.OrMetaBits(h, arena.QueueTagM)
		return nil
	}

	// Fresh key: send to S.
	if !e.sq.Push(h) {
		return e.evictS(key)
	}
	e.mgr.SetFreq(h, 0)
	e.mgr.OrMetaBits(h, arena.QueueTagS)
	return nil
}

// evictS handles S-queue overflow.  Tail is popped.  If freq >= 2 the
// handle is promoted to M (freq reset to 0).  Otherwise the fingerprint
// is recorded in the ghost and the handle is returned as evicted.
func (e *Eviction) evictS(key []byte) []arena.Handle {
	h, ok := e.sq.Pop()
	if !ok {
		return nil
	}

	if freq := e.mgr.GetFreq(h); freq >= 2 {
		// Promote to M.
		e.mgr.ClearMetaBits(h, arena.QueueTagMask)
		if !e.mq.Push(h) {
			evicted := e.evictM()
			if !e.mq.Push(h) {
				// Truly out of room — drop this handle too.
				e.mgr.SetFreq(h, 0)
				return append(evicted, h)
			}
			e.mgr.SetFreq(h, 0)
			e.mgr.OrMetaBits(h, arena.QueueTagM)
			return evicted
		}
		e.mgr.SetFreq(h, 0)
		e.mgr.OrMetaBits(h, arena.QueueTagM)
		return nil
	}

	// Move fingerprint to ghost; return handle for freeing.
	e.gh.Insert(fingerprintOf(key))
	return []arena.Handle{h}
}

// evictM handles M-queue overflow.  Tail is popped.  If freq >= 1 the
// handle is reinserted at the tail of M with freq -= 1.  Otherwise it is
// returned as evicted (no ghost insertion for M evictions).
func (e *Eviction) evictM() []arena.Handle {
	h, ok := e.mq.Pop()
	if !ok {
		return nil
	}

	freq := e.mgr.GetFreq(h)
	if freq >= 1 {
		// Reinsert, decrement freq.
		e.mq.Push(h)
		e.mgr.SetFreq(h, freq-1)
		return nil
	}

	// freq == 0 → evict.
	e.mgr.ClearMetaBits(h, arena.QueueTagMask)
	return []arena.Handle{h}
}

// Touch records a cache hit.  Lock-free: only atomic reads/writes.
//
// Implementation: read the queue tag from Handle.Meta.  Then walk the
// appropriate queue's slots to find the handle.  When found, increment
// the freq counter via mgr.IncFreq.
func (e *Eviction) Touch(h arena.Handle) {
	meta := e.mgr.GetMeta(h)
	tag := meta & arena.QueueTagMask
	switch tag {
	case arena.QueueTagS:
		e.touchQueue(e.sq, h)
	case arena.QueueTagM:
		e.touchQueue(e.mq, h)
	}
}

// touchQueue walks the queue from index 0 looking for handle h.
// On match, atomically increments freq via mgr.IncFreq.
func (e *Eviction) touchQueue(q *RingBuffer, h arena.Handle) {
	if q.ScanFromHead(h) {
		e.mgr.IncFreq(h)
	}
}

// Delete removes the handle from whichever queue holds it.
// Holds e.mu.
func (e *Eviction) Delete(key []byte, h arena.Handle) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.sq.Remove(h) {
		e.gh.Remove(fingerprintOf(key))
		e.mgr.ClearMetaBits(h, arena.QueueTagMask)
		return
	}
	if e.mq.Remove(h) {
		e.mgr.ClearMetaBits(h, arena.QueueTagMask)
		return
	}
}

// Remove removes the handle from whichever queue holds it without touching the ghost index.
// Useful for background garbage collection of expired handles where the key is no longer available.
// Holds e.mu.
func (e *Eviction) Remove(h arena.Handle) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.sq.Remove(h) {
		e.mgr.ClearMetaBits(h, arena.QueueTagMask)
		return
	}
	if e.mq.Remove(h) {
		e.mgr.ClearMetaBits(h, arena.QueueTagMask)
		return
	}
}

// Stats returns (S size, M size, ghost live count).
func (e *Eviction) Stats() (sSize, mSize, ghostLive uint64) {
	live, _ := e.gh.Stats()
	return e.sq.Size(), e.mq.Size(), live
}
