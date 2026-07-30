// Package queue provides a fixed-capacity ring buffer for S3-FIFO.
//
// The RingBuffer stores arena.Handle values plus a per-slot tombstone flag.
// It is designed for the S3-FIFO S and M queues: O(1) Push/Pop on a
// pre-allocated array (no linked-list nodes), with a lock-free Touch path
// that operates only on the head slot.
package eviction

import (
	"sync/atomic"

	"github.com/kshishtovsky/horreum/internal/arena"
)

// noNext marks a slot with no successor in the inline linked list.
const noNext uint32 = 0xFFFFFFFF

// slot is one entry in the ring buffer.
type slot struct {
	h     arena.Handle
	next  uint32 // reserved; set to noNext on Push
	alive atomic.Bool
}

// RingBuffer is a power-of-two FIFO with O(1) Push/Pop.
type RingBuffer struct {
	cap  uint64
	mask uint64
	head atomic.Uint64
	tail atomic.Uint64
	data []slot
}

func NewRingBuffer(capacity uint64) *RingBuffer {
	if capacity == 0 {
		capacity = 1
	}
	c := uint64(1)
	for c < capacity {
		c <<= 1
	}
	return &RingBuffer{
		cap:  c,
		mask: c - 1,
		data: make([]slot, c),
	}
}

func (rb *RingBuffer) Capacity() uint64 { return rb.cap }

func (rb *RingBuffer) Full() bool {
	return rb.tail.Load()-rb.head.Load() >= rb.cap
}

func (rb *RingBuffer) Empty() bool {
	return rb.tail.Load() == rb.head.Load()
}

func (rb *RingBuffer) Size() uint64 {
	return rb.tail.Load() - rb.head.Load()
}

func (rb *RingBuffer) Push(h arena.Handle) bool {
	if rb.Full() {
		return false
	}
	tail := rb.tail.Load()
	s := &rb.data[tail&rb.mask]
	s.h = h
	s.next = noNext
	s.alive.Store(true)
	rb.tail.Add(1)
	return true
}

func (rb *RingBuffer) Pop() (arena.Handle, bool) {
	if rb.Empty() {
		return arena.Handle{}, false
	}
	head := rb.head.Load()
	s := &rb.data[head&rb.mask]
	h := s.h
	s.alive.Store(false)
	rb.head.Add(1)
	return h, true
}

func (rb *RingBuffer) Peek() (arena.Handle, bool) {
	if rb.Empty() {
		return arena.Handle{}, false
	}
	s := &rb.data[rb.head.Load()&rb.mask]
	return s.h, true
}

// ScanFromHead walks slots starting at index 0 (head position), looking for h.
func (rb *RingBuffer) ScanFromHead(h arena.Handle) bool {
	for i := uint64(0); i < rb.cap; i++ {
		s := &rb.data[i&rb.mask]
		if s.alive.Load() && s.h == h {
			return true
		}
	}
	return false
}

func (rb *RingBuffer) Remove(h arena.Handle) bool {
	for i := uint64(0); i < rb.cap; i++ {
		s := &rb.data[i&rb.mask]
		if s.alive.Load() && s.h == h {
			s.alive.Store(false)
			return true
		}
	}
	return false
}

func (rb *RingBuffer) ResetFreq(mgr *arena.Manager, h arena.Handle) {
	for i := uint64(0); i < rb.cap; i++ {
		s := &rb.data[i&rb.mask]
		if s.alive.Load() && s.h == h {
			mgr.SetFreq(h, 0)
			return
		}
	}
}

func (rb *RingBuffer) Clear() {
	for i := range rb.data {
		rb.data[i].alive.Store(false)
	}
	rb.head.Store(0)
	rb.tail.Store(0)
}
