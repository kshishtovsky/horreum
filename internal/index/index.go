// Package index provides a simple open-addressing hash index for
// benchmarking the arena. Not intended for production use.
package index

import (
	"bytes"

	"github.com/kshishtovsky/horreum/internal/arena"
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
			kCopy := make([]byte, len(key))
			copy(kCopy, key)
			h.buckets[idx] = Entry{Hash: hash, KeyLen: uint16(len(key)), Handle: hd, ExpiresAt: expiresAt}
			h.keys[idx] = kCopy
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

func (h *HashIndex) deleteAt(i uint64) {
	h.keys[i] = nil
	h.buckets[i] = Entry{}
	h.count--

	gap := i
	curr := (i + 1) & h.mask
	for h.keys[curr] != nil {
		desired := h.buckets[curr].Hash & h.mask
		if (curr > gap && (desired <= gap || desired > curr)) ||
			(curr < gap && (desired <= gap && desired > curr)) {
			h.keys[gap] = h.keys[curr]
			h.buckets[gap] = h.buckets[curr]

			h.keys[curr] = nil
			h.buckets[curr] = Entry{}
			gap = curr
		}
		curr = (curr + 1) & h.mask
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
			h.deleteAt(idx)
			return hd, true
		}
		idx = (idx + 1) & h.mask
	}
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
			h.deleteAt(idx)
		}
	}
	return freed
}

// ScanPrefix iterates buckets starting from cursor and returns up to limit keys matching prefix.
func (h *HashIndex) ScanPrefix(prefix []byte, cursor uint64, limit int, now uint32) ([][]byte, uint64) {
	if h.count == 0 || limit <= 0 {
		return nil, 0
	}
	cap := uint64(len(h.buckets))
	if cursor >= cap {
		return nil, 0
	}

	var results [][]byte
	idx := cursor

	for idx < cap {
		if h.keys[idx] != nil {
			if h.buckets[idx].ExpiresAt == 0 || now < h.buckets[idx].ExpiresAt {
				if len(prefix) == 0 || bytes.HasPrefix(h.keys[idx], prefix) {
					results = append(results, h.keys[idx])
					if len(results) >= limit {
						next := idx + 1
						if next >= cap {
							next = 0
						}
						return results, next
					}
				}
			}
		}
		idx++
	}

	return results, 0
}

// DeletePrefix deletes all keys matching prefix and returns their arena handles.
func (h *HashIndex) DeletePrefix(prefix []byte, now uint32) []arena.Handle {
	if h.count == 0 {
		return nil
	}
	var freed []arena.Handle
	idx := uint64(0)
	cap := uint64(len(h.buckets))

	for idx < cap {
		if h.keys[idx] != nil {
			if len(prefix) == 0 || bytes.HasPrefix(h.keys[idx], prefix) {
				freed = append(freed, h.buckets[idx].Handle)
				h.deleteAt(idx)
				continue
			}
		}
		idx++
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
