package arena

import (
	"sync"
	"sync/atomic"
)

const (
	alignBytes     = 8
	numSizeClasses = 12
)

// sizeClasses defines the 12 size classes: 8, 16, 32, 64, 128, 256,
// 512, 1024, 2048, 4096, 8192, and >8192 (bump only).
var sizeClasses = [numSizeClasses]uint32{
	8, 16, 32, 64, 128, 256, 512, 1024, 2048, 4096, 8192, 0,
}

// freeNode is a Go-allocated linked list node for the freelist.
// These live in Go memory, not in the mmap — only the offsets point
// into the mmap.
type freeNode struct {
	offset uint32
	next   *freeNode
}

// Allocator manages bump allocation and segregated freelists within
// a single mmap'd region. All offsets are relative to the region start.
// The bump pointer is atomic for lock-free allocation on the hot path.
// The freelists use a mutex but are only consulted when blocks are reused.
type Allocator struct {
	offset atomic.Uint64
	size   uint32
	mu     sync.Mutex
	fl     [numSizeClasses]*freeNode
}

// Init initializes the allocator for a region of the given size.
func (a *Allocator) Init(size uint32) {
	a.size = size
	a.offset.Store(0)
	for i := range a.fl {
		a.fl[i] = nil
	}
}

// sizeClass returns the index into the sizeClasses array for the given size.
func sizeClass(n uint32) int {
	if n <= 8 {
		return 0
	}
	if n <= 16 {
		return 1
	}
	if n <= 32 {
		return 2
	}
	if n <= 64 {
		return 3
	}
	if n <= 128 {
		return 4
	}
	if n <= 256 {
		return 5
	}
	if n <= 512 {
		return 6
	}
	if n <= 1024 {
		return 7
	}
	if n <= 2048 {
		return 8
	}
	if n <= 4096 {
		return 9
	}
	if n <= 8192 {
		return 10
	}
	return 11
}

// classSize returns the size for a given class index.
func classSize(cls int) uint32 {
	if cls < numSizeClasses-1 {
		return sizeClasses[cls]
	}
	return 0
}

// Alloc returns an offset for a block of at least n bytes.
// Returns ErrArenaFull if the region is exhausted.
func (a *Allocator) Alloc(n uint32) (uint32, error) {
	if n == 0 {
		n = 1
	}

	cls := sizeClass(n)

	// Try freelist first (only for concrete size classes).
	if cls < numSizeClasses-1 {
		a.mu.Lock()
		head := a.fl[cls]
		if head != nil {
			off := head.offset
			a.fl[cls] = head.next
			a.mu.Unlock()
			return off, nil
		}
		a.mu.Unlock()
	}

	// Bump allocation with CAS loop.
	var allocSize uint32
	if cls < numSizeClasses-1 {
		allocSize = classSize(cls)
	} else {
		allocSize = (n + alignBytes - 1) &^ (alignBytes - 1)
	}

	for {
		cur := a.offset.Load()
		aligned := (cur + alignBytes - 1) &^ (alignBytes - 1)
		newOff := aligned + uint64(allocSize)
		if newOff > uint64(a.size) {
			return 0, ErrArenaFull
		}
		if a.offset.CompareAndSwap(cur, newOff) {
			return uint32(aligned), nil
		}
	}
}

// Free returns a block to the freelist.
func (a *Allocator) Free(offset, size uint32) {
	cls := sizeClass(size)
	if cls >= numSizeClasses-1 {
		// Large blocks are not freelisted — they stay as dead bump space.
		return
	}
	a.mu.Lock()
	a.fl[cls] = &freeNode{offset: offset, next: a.fl[cls]}
	a.mu.Unlock()
}

// Stats returns allocator utilization.
func (a *Allocator) Stats() Stats {
	off := a.offset.Load()
	var flCount uint64
	for i := 0; i < numSizeClasses; i++ {
		a.mu.Lock()
		for n := a.fl[i]; n != nil; n = n.next {
			flCount++
		}
		a.mu.Unlock()
	}
	return Stats{
		UsedBytes:   off,
		FreeBytes:   uint64(a.size) - off,
		FreelistLen: flCount,
	}
}
