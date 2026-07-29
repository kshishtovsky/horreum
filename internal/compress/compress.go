// Package compress provides optional value compression for the cache.
//
// Two implementations are provided:
//
//   - Noop: pass-through, zero overhead.  Default when compression is
//     disabled.
//   - LZ4: pure-Go LZ4-block codec.  ~2 GB/s compress, ~4 GB/s
//     decompress.  No external dependencies.
//
// Compression is transparent to the arena: the arena stores the
// compressed bytes; the transport layer compresses on Set and
// decompresses on Get.  A meta bit (arena.CompressedFlag) marks
// compressed entries so Get knows whether to decompress.
//
// Original (uncompressed) size is stored as a 4-byte little-endian
// prefix in the compressed payload.  This adds 4 bytes of overhead
// per compressed value but avoids changing the Handle layout.
package compress

import (
	"encoding/binary"
	"sync"
)

// Compressor defines the optional value compression interface.
//
// Implementations must be safe for concurrent use.
type Compressor interface {
	// Compress compresses src and returns the result.  The returned
	// slice is obtained from the internal pool; the caller must not
	// retain it across operations (the transport copies it into the
	// arena immediately).
	//
	// If src is smaller than the minimum threshold or incompressible,
	// Compress returns (nil, false) — the caller should store the
	// original value uncompressed.
	Compress(src []byte) ([]byte, bool)

	// Decompress decompresses src into a pooled buffer and returns
	// the result.  The caller must call PutBuf when done with the
	// returned slice.
	Decompress(src []byte) ([]byte, error)

	// PutBuf returns a buffer obtained from Decompress back to the
	// pool.  Passing nil is safe (no-op).
	PutBuf(buf []byte)

	// Name returns the algorithm name for logging ("lz4", "none").
	Name() string
}

// sizePrefix is the number of bytes used to store the original
// (uncompressed) size at the start of the compressed payload.
const sizePrefix = 4

// encodeSizePrefix writes the original size as a 4-byte LE prefix.
func encodeSizePrefix(dst []byte, originalSize uint32) {
	binary.LittleEndian.PutUint32(dst[:sizePrefix], originalSize)
}

// decodeSizePrefix reads the 4-byte LE original-size prefix.
func decodeSizePrefix(src []byte) uint32 {
	return binary.LittleEndian.Uint32(src[:sizePrefix])
}

// ──────────────────────────────────────────────────────────────────
// Noop — pass-through compressor.
// ──────────────────────────────────────────────────────────────────

// Noop is a pass-through compressor that never compresses.  It is
// the default when compression is disabled (--compression=none).
type Noop struct{}

// Compress always returns (nil, false) — no compression.
func (Noop) Compress([]byte) ([]byte, bool) { return nil, false }

// Decompress should never be called on a Noop compressor (values
// stored with Noop are not marked compressed).  Returns src as-is
// for safety.
func (Noop) Decompress(src []byte) ([]byte, error) { return src, nil }

// PutBuf is a no-op.
func (Noop) PutBuf([]byte) {}

// Name returns "none".
func (Noop) Name() string { return "none" }

// ──────────────────────────────────────────────────────────────────
// Buffer pool — size-class based.
// ──────────────────────────────────────────────────────────────────

// bufPool is a size-class-based sync.Pool set for compress/decompress
// scratch buffers.  Classes: 4K, 16K, 64K, 256K, 1M.
type bufPool struct {
	pools [5]sync.Pool
}

// poolClasses are the upper bounds for each size class.
var poolClasses = [5]int{
	4 << 10,   // 4 KiB
	16 << 10,  // 16 KiB
	64 << 10,  // 64 KiB
	256 << 10, // 256 KiB
	1 << 20,   // 1 MiB
}

// classFor returns the pool index for a buffer of size n.
// Returns -1 if n exceeds all classes (caller must allocate directly).
func classFor(n int) int {
	for i, c := range poolClasses {
		if n <= c {
			return i
		}
	}
	return -1
}

// get returns a byte slice of at least n bytes from the pool.
func (p *bufPool) get(n int) []byte {
	cls := classFor(n)
	if cls < 0 {
		return make([]byte, n)
	}
	if v := p.pools[cls].Get(); v != nil {
		buf := v.([]byte)
		if cap(buf) >= n {
			return buf[:n]
		}
	}
	return make([]byte, poolClasses[cls])[:n]
}

// put returns buf to the appropriate pool.  Oversized buffers that
// don't fit any class are dropped (GC will collect them).
func (p *bufPool) put(buf []byte) {
	cls := classFor(cap(buf))
	if cls < 0 {
		return
	}
	//nolint:staticcheck // sync.Pool accepts interface{}
	p.pools[cls].Put(buf[:cap(buf)])
}
