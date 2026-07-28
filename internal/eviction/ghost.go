// Package eviction provides a power-of-two open-addressed hash table for 4-byte fingerprints.
package eviction

import (
	"crypto/sha256"
	"encoding/binary"
	"sync/atomic"
)

// bucket is one slot in the ghost hash table.
// fp is atomic to avoid torn reads on weakly-ordered platforms.
type bucket struct {
	fp    atomic.Uint32
	alive atomic.Bool
}

// GhostIndex is a power-of-two open-addressed hash table of 4-byte fingerprints.
type GhostIndex struct {
	cap  uint64
	mask uint64
	tabs []bucket
}

func NewGhostIndex(capacity uint64) *GhostIndex {
	c := uint64(1)
	for c < capacity {
		c <<= 1
	}
	return &GhostIndex{cap: c, mask: c - 1, tabs: make([]bucket, c)}
}

func (g *GhostIndex) Capacity() uint64 { return g.cap }

func (g *GhostIndex) probe(fp uint32) uint64 {
	return uint64(fp) & g.mask
}

// fingerprintOf returns a 4-byte fingerprint for the given key using SHA-256.
func fingerprintOf(key []byte) uint32 {
	h := sha256.Sum256(key)
	return binary.BigEndian.Uint32(h[:4])
}

// Look returns true if the fingerprint is present and alive.
func (g *GhostIndex) Look(fp uint32) bool {
	start := g.probe(fp)
	for i := uint64(0); i < g.cap; i++ {
		idx := (start + i) & g.mask
		b := &g.tabs[idx]
		if !b.alive.Load() {
			return false
		}
		if b.fp.Load() == fp {
			return true
		}
	}
	return false
}

// Insert records the fingerprint as alive. Returns true if a matching
// entry was already present (collision), false otherwise.
func (g *GhostIndex) Insert(fp uint32) bool {
	start := g.probe(fp)
	for i := uint64(0); i < g.cap; i++ {
		idx := (start + i) & g.mask
		b := &g.tabs[idx]
		if !b.alive.Load() {
			// Write fp FIRST, then mark alive so concurrent Look
			// either sees the old state (no match) or the new state (match).
			b.fp.Store(fp)
			b.alive.Store(true)
			return false
		}
		if b.fp.Load() == fp {
			return true
		}
	}
	g.tabs[start].fp.Store(fp)
	g.tabs[start].alive.Store(true)
	return false
}

// Remove marks the fingerprint as dead (tombstone).
func (g *GhostIndex) Remove(fp uint32) bool {
	start := g.probe(fp)
	for i := uint64(0); i < g.cap; i++ {
		idx := (start + i) & g.mask
		b := &g.tabs[idx]
		if !b.alive.Load() {
			return false
		}
		if b.fp.Load() == fp {
			b.alive.Store(false)
			return true
		}
	}
	return false
}

// Clear marks all entries as dead.
func (g *GhostIndex) Clear() {
	for i := range g.tabs {
		g.tabs[i].alive.Store(false)
	}
}

// Stats returns (live, total) counts.
func (g *GhostIndex) Stats() (live, total uint64) {
	for i := range g.tabs {
		if g.tabs[i].alive.Load() {
			live++
		}
		total++
	}
	return live, total
}
