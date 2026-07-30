// Package index provides a simple open-addressing hash index for
// benchmarking the arena. Not intended for production use.
package index

import (
	"github.com/horreum/horreum/internal/arena"
)

// Entry is a 24-byte key-value pair stored in the hash table.
type Entry struct {
	Hash   uint64
	KeyLen    uint16
	Handle    arena.Handle // 12 bytes: Offset, Size, Meta, Region, _
	ExpiresAt uint32       // Unix timestamp in seconds; 0 means no expiration
}

// HashIndex is an open-addressing hash table mapping byte-slice keys
// to arena Handles. Uses power-of-two bucket count with linear probing.
type HashIndex struct {
	buckets []Entry
	keys       [][]byte
	count      int
	mask       uint64
	scanCursor uint64
}

// New creates a HashIndex with the given initial bucket count
// (rounded up to next power of two).
func New(initialBuckets int) *HashIndex {
	cap := nextPowerOfTwo(uint64(initialBuckets))
	return &HashIndex{
		buckets: make([]Entry, cap),
		keys:    make([][]byte, cap),
		mask:    uint64(cap) - 1,
	}
}

// Put inserts or updates a key-handle pair. Returns true if an
// existing entry was evicted.
func (h *HashIndex) Put(key []byte, hd arena.Handle, expiresAt uint32) bool {
	if h.count*4 >= len(h.buckets)*3 {
		h.grow()
	}
	hash := fnv64(key)
	idx := hash & h.mask
	for {
		if h.keys[idx] == nil {
			// Empty slot — insert.
			h.buckets[idx] = Entry{Hash: hash, KeyLen: uint16(len(key)), Handle: hd, ExpiresAt: expiresAt}
			h.keys[idx] = key
			h.count++
			return false
		}
		if h.buckets[idx].Hash == hash && h.buckets[idx].KeyLen == uint16(len(key)) && bytesEqual(h.keys[idx], key) {
			// Update existing.
			h.buckets[idx].Handle = hd
			h.buckets[idx].ExpiresAt = expiresAt
			h.keys[idx] = key
			return true
		}
		idx = (idx + 1) & h.mask
	}
}

// Get retrieves the Handle for a key. Returns (handle, true) if found and not expired.
func (h *HashIndex) Get(key []byte, now uint32) (arena.Handle, bool) {
	hash := fnv64(key)
	idx := hash & h.mask
	for {
		if h.keys[idx] == nil {
			return arena.Handle{}, false
		}
		if h.buckets[idx].Hash == hash && h.buckets[idx].KeyLen == uint16(len(key)) && bytesEqual(h.keys[idx], key) {
			if h.buckets[idx].ExpiresAt != 0 && now >= h.buckets[idx].ExpiresAt {
				return arena.Handle{}, false
			}
			return h.buckets[idx].Handle, true
		}
		idx = (idx + 1) & h.mask
	}
}

// Delete removes a key. Returns (handle, true) if found and removed.
func (h *HashIndex) Delete(key []byte) (arena.Handle, bool) {
	hash := fnv64(key)
	idx := hash & h.mask
	for {
		if h.keys[idx] == nil {
			return arena.Handle{}, false
		}
		if h.buckets[idx].Hash == hash && h.buckets[idx].KeyLen == uint16(len(key)) && bytesEqual(h.keys[idx], key) {
			hd := h.buckets[idx].Handle
			h.keys[idx] = nil
			h.buckets[idx] = Entry{}
			h.count--
			// Rehash following entries in the cluster.
			h.rehashFrom((idx + 1) & h.mask)
			return hd, true
		}
		idx = (idx + 1) & h.mask
	}
}

func (h *HashIndex) rehashFrom(start uint64) {
	idx := start
	for {
		if h.keys[idx] == nil {
			return
		}
		entry := h.buckets[idx]
		key := h.keys[idx]
		desired := entry.Hash & h.mask
		// Check if this entry can be moved to fill a gap.
		if canMove(desired, idx, start, h.mask) {
			// Find the gap.
			gap := findGap(desired, idx, h.mask, h.keys)
			if gap != idx {
				h.buckets[gap] = entry
				h.keys[gap] = key
				h.keys[idx] = nil
				h.buckets[idx] = Entry{}
				continue // Rehash from the vacated slot.
			}
		}
		idx = (idx + 1) & h.mask
	}
}

func canMove(desired, current, gap, mask uint64) bool {
	if desired <= gap {
		return current >= gap || current < desired
	}
	return current >= gap && current < desired
}

func findGap(desired, current, mask uint64, keys [][]byte) uint64 {
	gap := desired
	for gap != current {
		if keys[gap] == nil {
			return gap
		}
		gap = (gap + 1) & mask
	}
	return current
}

// SnapshotEntry pairs a bucket Entry with its key bytes for serialization.
type SnapshotEntry struct {
	Entry
	Key []byte
}

// Snapshot returns all live entries in arbitrary order. Used by the
// persist layer to write a checkpoint of the HashIndex.
//
// Allocates O(N) where N is the entry count. Not safe for concurrent
// mutation; the caller must serialize access.
func (h *HashIndex) Snapshot() []SnapshotEntry {
	if h.count == 0 || len(h.buckets) == 0 {
		return nil
	}
	snap := make([]SnapshotEntry, 0, h.count)
	for i := range h.buckets {
		if h.keys[i] != nil {
			snap = append(snap, SnapshotEntry{Entry: h.buckets[i], Key: h.keys[i]})
		}
	}
	return snap
}

// Add inserts an entry without growing the table. Used by the persist
// layer when loading from a checkpoint; the caller has already sized
// the HashIndex with New(initialBuckets) to fit the checkpoint.
//
// Caller must guarantee that buckets were never filled above load
// factor 0.5; Add does not enforce this. Returns false if a duplicate
// key is encountered (caller decides whether to abort).
func (h *HashIndex) Add(key []byte, hd arena.Handle, expiresAt uint32) bool {
	hash := fnv64(key)
	idx := hash & h.mask
	for {
		if h.keys[idx] == nil {
			h.buckets[idx] = Entry{Hash: hash, KeyLen: uint16(len(key)), Handle: hd, ExpiresAt: expiresAt}
			h.keys[idx] = key
			h.count++
			return true
		}
		if h.buckets[idx].Hash == hash && h.buckets[idx].KeyLen == uint16(len(key)) && bytesEqual(h.keys[idx], key) {
			// Duplicate — caller should reject; return false.
			return false
		}
		idx = (idx + 1) & h.mask
	}
}

func (h *HashIndex) grow() {
	oldBuckets := h.buckets
	oldKeys := h.keys
	newCap := uint64(len(oldBuckets)) * 2
	h.buckets = make([]Entry, newCap)
	h.keys = make([][]byte, newCap)
	h.mask = newCap - 1
	h.count = 0
	h.scanCursor = 0
	for i, entry := range oldBuckets {
		if oldKeys[i] != nil {
			h.Put(oldKeys[i], entry.Handle, entry.ExpiresAt)
		}
	}
}

// DeleteExpired scans up to 'limit' buckets and deletes expired entries.
func (h *HashIndex) DeleteExpired(now uint32, limit int) []arena.Handle {
	if h.count == 0 || limit <= 0 {
		return nil
	}
	var freed []arena.Handle
	scanned := 0
	for scanned < limit {
		idx := h.scanCursor
		h.scanCursor = (h.scanCursor + 1) & h.mask
		scanned++

		if h.keys[idx] == nil {
			continue
		}
		if h.buckets[idx].ExpiresAt != 0 && now >= h.buckets[idx].ExpiresAt {
			freed = append(freed, h.buckets[idx].Handle)
			h.keys[idx] = nil
			h.buckets[idx] = Entry{}
			h.count--
			h.rehashFrom((idx + 1) & h.mask)
		}
	}
	return freed
}

// NewForCount creates a HashIndex sized to hold at least n entries
// without growing. Round-up to power-of-two with at least 2x headroom.
func NewForCount(n int) *HashIndex {
	cap := nextPowerOfTwo(uint64(n) * 2)
	if cap < 8 {
		cap = 8
	}
	return &HashIndex{
		buckets: make([]Entry, cap),
		keys:    make([][]byte, cap),
		mask:    uint64(cap) - 1,
	}
}

// Count returns the number of live entries.
func (h *HashIndex) Count() int {
	return h.count
}

func fnv64(data []byte) uint64 {
	hash := uint64(14695981039346656037)
	for _, b := range data {
		hash ^= uint64(b)
		hash *= 1099511628211
	}
	return hash
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func nextPowerOfTwo(n uint64) uint64 {
	if n == 0 {
		return 1
	}
	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	n |= n >> 32
	return n + 1
}
