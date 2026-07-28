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
// meta is a parallel slice of atomic counters indexed by offset/alignBytes.
// It is allocated lazily on first Put to the region.
type region struct {
	data  []byte
	alloc Allocator
	meta  []atomic.Int32
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

func (m *Manager) loadRegions() []*region {
	return *m.regions.Load()
}

func NewManager(regionSize uint64, persistent bool) (*Manager, error) {
	m := &Manager{size: regionSize, anon: !persistent}
	r, err := newRegion(regionSize)
	if err != nil {
		return nil, err
	}
	r.meta = make([]atomic.Int32, uint32(len(r.data))/alignBytes)
	regions := []*region{r}
	m.regions.Store(&regions)
	m.current.Store(r)
	m.currentIdx.Store(0)
	return m, nil
}

// EnsureMeta lazily allocates the meta slice for the given region.
// Idempotent. Acquires m.mu briefly.
func (m *Manager) EnsureMeta(regionIdx uint8) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureMetaLocked(uint32(regionIdx))
}

// ensureMetaLocked allocates reg.meta if absent. Caller MUST hold m.mu.
func (m *Manager) ensureMetaLocked(regionIdx uint32) {
	regions := m.loadRegions()
	if int(regionIdx) >= len(regions) {
		return
	}
	reg := regions[regionIdx]
	if reg.meta != nil {
		return
	}
	reg.meta = make([]atomic.Int32, uint32(len(reg.data))/alignBytes)
}

func (m *Manager) Put(value []byte) (Handle, error) {
	n := uint32(len(value))
	if n > MaxObjectSize {
		return Handle{}, ErrSizeTooLarge
	}
	idx := m.currentIdx.Load()
	reg := m.current.Load()
	offset, err := reg.alloc.Alloc(n)
	if err == nil {
		copy(reg.data[offset:offset+n], value)
		m.liveObjs.Add(1)
		m.EnsureMeta(uint8(idx))
		reg.meta[offset/alignBytes].Store(0)
		return Handle{Offset: offset, Size: n, Region: uint8(idx)}, nil
	}
	return m.putSlow(value, n)
}

func (m *Manager) putSlow(value []byte, n uint32) (Handle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	reg := m.current.Load()
	offset, err := reg.alloc.Alloc(n)
	if err == nil {
		idx := m.currentIdx.Load()
		copy(reg.data[offset:offset+n], value)
		m.liveObjs.Add(1)
		m.ensureMetaLocked(idx)
		reg.meta[offset/alignBytes].Store(0)
		return Handle{Offset: offset, Size: n, Region: uint8(idx)}, nil
	}
	old := m.loadRegions()
	newIdx := uint32(len(old))
	if newIdx >= 256 {
		return Handle{}, ErrArenaFull
	}
	r, err := newRegion(m.size)
	if err != nil {
		return Handle{}, err
	}
	r.meta = make([]atomic.Int32, uint32(len(r.data))/alignBytes)
	newRegions := make([]*region, len(old)+1)
	copy(newRegions, old)
	newRegions[len(old)] = r
	m.regions.Store(&newRegions)
	m.current.Store(r)
	m.currentIdx.Store(newIdx)
	offset, err = r.alloc.Alloc(n)
	if err != nil {
		return Handle{}, err
	}
	copy(r.data[offset:offset+n], value)
	m.liveObjs.Add(1)
	r.meta[offset/alignBytes].Store(0)
	return Handle{Offset: offset, Size: n, Region: uint8(newIdx)}, nil
}

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
	return unsafeView(reg.data, h.Offset, h.Size), nil
}

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
	m.liveObjs.Add(^uint64(0))
	return nil
}

func (m *Manager) Stats() Stats {
	var s Stats
	s.LiveObjects = m.liveObjs.Load()
	for _, reg := range m.loadRegions() {
		rs := reg.alloc.Stats()
		s.UsedBytes += rs.UsedBytes
		s.FreeBytes += rs.FreeBytes
		s.FreelistLen += rs.FreelistLen
	}
	totalFree := s.FreeBytes
	if totalFree > 0 {
		var bumpTail uint64
		for _, reg := range m.loadRegions() {
			bumpTail += uint64(reg.alloc.size) - reg.alloc.offset.Load()
		}
		s.Fragmentation = 1.0 - float64(bumpTail)/float64(totalFree)
	}
	return s
}

func (m *Manager) Sync() error {
	for _, reg := range m.loadRegions() {
		if err := syncRegion(reg.data); err != nil {
			return err
		}
	}
	return nil
}

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

// IncFreq atomically increments the 2-bit frequency counter for the handle.
// Preserves the upper bits (queue tag etc.). Returns the new freq masked
// to 0..3. Lock-free hot path.
func (m *Manager) IncFreq(h Handle) uint16 {
	regions := m.loadRegions()
	if int(h.Region) >= len(regions) {
		return 0
	}
	reg := regions[h.Region]
	if reg.meta == nil {
		return 0
	}
	idx := h.Offset / alignBytes
	if idx >= uint32(len(reg.meta)) {
		return 0
	}
	const freqMask = int32(0x3)
	for {
		cur := reg.meta[idx].Load()
		freq := cur & freqMask
		nxt := (cur &^ freqMask) | ((freq + 1) & freqMask)
		if reg.meta[idx].CompareAndSwap(cur, nxt) {
			return uint16((freq + 1) & freqMask)
		}
	}
}

// GetFreq atomically reads the 2-bit frequency counter for the handle.
func (m *Manager) GetFreq(h Handle) uint16 {
	regions := m.loadRegions()
	if int(h.Region) >= len(regions) {
		return 0
	}
	reg := regions[h.Region]
	if reg.meta == nil {
		return 0
	}
	idx := h.Offset / alignBytes
	if idx >= uint32(len(reg.meta)) {
		return 0
	}
	return uint16(reg.meta[idx].Load() & 0x3)
}

// SetFreq atomically stores the freq counter (0..3) for the handle.
// Preserves the upper bits (queue tag etc.).
func (m *Manager) SetFreq(h Handle, freq uint16) {
	regions := m.loadRegions()
	if int(h.Region) >= len(regions) {
		return
	}
	reg := regions[h.Region]
	if reg.meta == nil {
		return
	}
	idx := h.Offset / alignBytes
	if idx >= uint32(len(reg.meta)) {
		return
	}
	const freqMask = int32(0x3)
	for {
		cur := reg.meta[idx].Load()
		nxt := (cur &^ freqMask) | (int32(freq) & freqMask)
		if nxt == cur || reg.meta[idx].CompareAndSwap(cur, nxt) {
			return
		}
	}
}

// SetMeta atomically stores the full 16-bit meta for a handle.
func (m *Manager) SetMeta(h Handle, meta uint16) {
	regions := m.loadRegions()
	if int(h.Region) >= len(regions) {
		return
	}
	reg := regions[h.Region]
	if reg.meta == nil {
		return
	}
	idx := h.Offset / alignBytes
	if idx >= uint32(len(reg.meta)) {
		return
	}
	reg.meta[idx].Store(int32(meta))
}

// GetMeta atomically reads the full 16-bit meta for a handle.
func (m *Manager) GetMeta(h Handle) uint16 {
	regions := m.loadRegions()
	if int(h.Region) >= len(regions) {
		return 0
	}
	reg := regions[h.Region]
	if reg.meta == nil {
		return 0
	}
	idx := h.Offset / alignBytes
	if idx >= uint32(len(reg.meta)) {
		return 0
	}
	return uint16(reg.meta[idx].Load())
}

// CASMeta performs an atomic compare-and-swap on the meta field.
// Returns true if the swap succeeded.
func (m *Manager) CASMeta(h Handle, oldMeta, newMeta uint16) bool {
	regions := m.loadRegions()
	if int(h.Region) >= len(regions) {
		return false
	}
	reg := regions[h.Region]
	if reg.meta == nil {
		return false
	}
	idx := h.Offset / alignBytes
	if idx >= uint32(len(reg.meta)) {
		return false
	}
	return reg.meta[idx].CompareAndSwap(int32(oldMeta), int32(newMeta))
}

// OrMetaBits atomically ORs the given bits into the meta field via CAS loop.
func (m *Manager) OrMetaBits(h Handle, bits uint16) {
	for {
		current := m.GetMeta(h)
		newVal := current | bits
		if newVal == current {
			return
		}
		if m.CASMeta(h, current, newVal) {
			return
		}
	}
}

// ClearMetaBits atomically clears the given bits from the meta field via CAS loop.
func (m *Manager) ClearMetaBits(h Handle, mask uint16) {
	for {
		current := m.GetMeta(h)
		newVal := current &^ mask
		if newVal == current {
			return
		}
		if m.CASMeta(h, current, newVal) {
			return
		}
	}
}

// Queue tag constants stored in the high bits of Handle.Meta.
// Bits 0-1 are the freq counter (managed by IncFreq/GetFreq/SetFreq).
// Bits 2-3 encode the queue tag: 0 = none, 1 = S, 2 = M.
const (
	QueueTagMask = uint16(0x3 << 2)
	QueueTagNone = uint16(0x0 << 2)
	QueueTagS    = uint16(0x1 << 2)
	QueueTagM    = uint16(0x2 << 2)
)
