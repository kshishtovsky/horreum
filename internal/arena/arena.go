// Package arena provides a contiguous mmapped arena with manual offset
// management for zero-allocation blob storage.
package arena

import (
	"errors"
	"sync"
	"sync/atomic"
)

// MaxObjectSize is the maximum payload size for a single Put.
const MaxObjectSize = 64 << 20 // 64 MiB

var (
	// ErrArenaFull is returned when all regions are exhausted and a new
	// region cannot be allocated (e.g. OS limit).
	ErrArenaFull = errors.New("horreum: arena full")

	// ErrOffsetInvalid is returned when a Handle references an out-of-bounds offset.
	ErrOffsetInvalid = errors.New("horreum: invalid offset")

	// ErrSizeTooLarge is returned when a value exceeds MaxObjectSize.
	ErrSizeTooLarge = errors.New("horreum: value exceeds max object size")
)

// Handle is a 12-byte value type that references a blob in the arena.
// It contains no pointers — safe to store in index entries without
// triggering GC tracing.
type Handle struct {
	Offset uint32 // offset within region
	Size   uint32 // payload length
	Meta   uint16 // reserved for eviction (freq bits, queue tag)
	Region uint8  // region id
	_      [1]byte
}

// Stats reports arena utilization.
type Stats struct {
	UsedBytes, FreeBytes uint64
	LiveObjects          uint64
	FreelistLen          uint64
	Fragmentation        float64 // 1 - maxContiguousFree/totalFree
}

// region is a single mmap'd allocation with its own allocator.
type region struct {
	data []byte
	alloc Allocator
}

// newRegion creates a mmap'd region and initializes its allocator.
func newRegion(size uint64) (*region, error) {
	data, err := mmapRegion(size)
	if err != nil {
		return nil, err
	}
	r := &region{data: data}
	r.alloc.Init(uint32(size))
	return r, nil
}

// Manager owns one or more mmap'd regions and routes Put/View/Free
// to the correct region via the Handle.
type Manager struct {
	mu         sync.Mutex
	regions    atomic.Pointer[[]*region]
	current    atomic.Pointer[region]
	currentIdx atomic.Uint32
	size       uint64
	anon       bool
	liveObjs   atomic.Uint64
}

// loadRegions returns the current immutable snapshot of the regions slice.
func (m *Manager) loadRegions() []*region {
	return *m.regions.Load()
}

// NewManager creates an arena Manager with the given region size.
// If persistent is true, the mmap is file-backed (future use);
// otherwise MAP_ANON is used.
func NewManager(regionSize uint64, persistent bool) (*Manager, error) {
	m := &Manager{
		size: regionSize,
		anon: !persistent,
	}
	r, err := newRegion(regionSize)
	if err != nil {
		return nil, err
	}
	regions := []*region{r}
	m.regions.Store(&regions)
	m.current.Store(r)
	m.currentIdx.Store(0)
	return m, nil
}

// Put stores value in the arena and returns a Handle.
// Zero heap allocations on the hot path.
func (m *Manager) Put(value []byte) (Handle, error) {
	n := uint32(len(value))
	if n > MaxObjectSize {
		return Handle{}, ErrSizeTooLarge
	}

	// Fast path: try current region without taking manager lock.
	// SAFETY: current is an atomic pointer; Load() returns a consistent snapshot.
	idx := m.currentIdx.Load()
	reg := m.current.Load()
	offset, err := reg.alloc.Alloc(n)
	if err == nil {
		// SAFETY: reg.data[offset:offset+n] is within mmap'd region bounds.
		// offset was validated by Alloc; n is the requested size.
		copy(reg.data[offset:offset+n], value)
		m.liveObjs.Add(1)
		return Handle{Offset: offset, Size: n, Region: uint8(idx)}, nil
	}

	// Slow path: rotate to a new region.
	return m.putSlow(value, n)
}

func (m *Manager) putSlow(value []byte, n uint32) (Handle, error) {
	m.mu.Lock()
	// Double-check: another goroutine may have rotated already.
	reg := m.current.Load()
	offset, err := reg.alloc.Alloc(n)
	if err == nil {
		idx := m.currentIdx.Load()
		m.mu.Unlock()
		copy(reg.data[offset:offset+n], value)
		m.liveObjs.Add(1)
		return Handle{Offset: offset, Size: n, Region: uint8(idx)}, nil
	}

	// Allocate a new region.
	old := m.loadRegions()
	newIdx := uint32(len(old))
	if newIdx >= 256 {
		m.mu.Unlock()
		return Handle{}, ErrArenaFull
	}
	r, err := newRegion(m.size)
	if err != nil {
		m.mu.Unlock()
		return Handle{}, err
	}
	// Publish new regions slice atomically (copy-on-write).
	newRegions := make([]*region, len(old)+1)
	copy(newRegions, old)
	newRegions[len(old)] = r
	m.regions.Store(&newRegions)
	m.current.Store(r)
	m.currentIdx.Store(newIdx)
	m.mu.Unlock()

	// Allocate from the new region.
	offset, err = r.alloc.Alloc(n)
	if err != nil {
		return Handle{}, err
	}
	copy(r.data[offset:offset+n], value)
	m.liveObjs.Add(1)
	return Handle{Offset: offset, Size: n, Region: uint8(newIdx)}, nil
}

// View returns a zero-copy slice backed by the mmap'd region.
// The slice is valid until Manager.Close().
func (m *Manager) View(h Handle) ([]byte, error) {
	regions := m.loadRegions()
	if int(h.Region) >= len(regions) {
		return nil, ErrOffsetInvalid
	}
	reg := regions[h.Region]
	end := uint64(h.Offset) + uint64(h.Size)
	if end > uint64(len(reg.data)) {
		return nil, ErrOffsetInvalid
	}
	// SAFETY: &reg.data[h.Offset] points into mmap'd memory.
	// h.Size is validated against region bounds.
	// The returned slice header lives on stack; backing store is mmap.
	return unsafeView(reg.data, h.Offset, h.Size), nil
}

// Free marks a Handle's space as reusable.
func (m *Manager) Free(h Handle) error {
	regions := m.loadRegions()
	if int(h.Region) >= len(regions) {
		return ErrOffsetInvalid
	}
	reg := regions[h.Region]
	end := uint64(h.Offset) + uint64(h.Size)
	if end > uint64(len(reg.data)) {
		return ErrOffsetInvalid
	}
	reg.alloc.Free(h.Offset, h.Size)
	m.liveObjs.Add(^uint64(0) - 0) // atomic decrement
	return nil
}

// Stats returns current arena utilization.
func (m *Manager) Stats() Stats {
	var s Stats
	s.LiveObjects = m.liveObjs.Load()
	regions := m.loadRegions()
	for _, reg := range regions {
		rs := reg.alloc.Stats()
		s.UsedBytes += rs.UsedBytes
		s.FreeBytes += rs.FreeBytes
		s.FreelistLen += rs.FreelistLen
	}
	// Fragmentation: 1 - maxContiguousFree / totalFree.
	// For a bump allocator, the bump tail is always the largest
	// contiguous free block. The freelists add non-contiguous free space.
	totalFree := s.FreeBytes
	if totalFree > 0 {
		var bumpTail uint64
		for _, reg := range regions {
			bumpTail += uint64(reg.alloc.size) - reg.alloc.offset.Load()
		}
		s.Fragmentation = 1.0 - float64(bumpTail)/float64(totalFree)
	}
	return s
}

// Sync flushes dirty pages. No-op for MAP_ANON.
func (m *Manager) Sync() error {
	for _, reg := range m.loadRegions() {
		if err := syncRegion(reg.data); err != nil {
			return err
		}
	}
	return nil
}

// Close unmaps all regions and releases resources.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	regions := m.loadRegions()
	var firstErr error
	for _, reg := range regions {
		if err := munmapRegion(reg.data); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	empty := []*region(nil)
	m.regions.Store(&empty)
	return firstErr
}
