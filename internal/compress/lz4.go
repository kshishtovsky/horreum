package compress

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// LZ4 algorithm constants.
const (
	// minMatch is the minimum match length (LZ4 spec).
	minMatch = 4
	// maxInputSize caps single-block compression at 2 GiB - 1.
	maxInputSize = 0x7E000000
	// hashLog is log2 of the hash table size (64K entries × 4 = 256 KB).
	hashLog = 16
	// hashTableSize is 1 << hashLog.
	hashTableSize = 1 << hashLog
	// hashShift is 32 - hashLog.
	hashShift = 32 - hashLog
	// mfLimit is the last position at which a match search starts.
	// Must leave room for the last 5 literals (LZ4 spec: lastLiterals=5).
	lastLiterals = 5
	// winSize is the maximum backwards distance for a match (64 KB).
	winSize = 1 << 16
)

// Errors.
var (
	ErrCorrupt  = errors.New("lz4: corrupt input")
	ErrTooLarge = errors.New("lz4: input too large")
)

// LZ4 is a pure-Go LZ4-block compressor.
//
// It stores the original size as a 4-byte LE prefix so the
// decompressor knows the output buffer size.  MinSize controls the
// minimum payload length to attempt compression; shorter values are
// left uncompressed.
type LZ4 struct {
	MinSize int // minimum payload size to compress (default 64)
	pool    bufPool
}

// NewLZ4 creates an LZ4 compressor with the given minimum size threshold.
func NewLZ4(minSize int) *LZ4 {
	if minSize <= 0 {
		minSize = 64
	}
	return &LZ4{MinSize: minSize}
}

// Name returns "lz4".
func (c *LZ4) Name() string { return "lz4" }

// PutBuf returns a decompression buffer to the pool.
func (c *LZ4) PutBuf(buf []byte) {
	if buf != nil {
		c.pool.put(buf)
	}
}

// Compress compresses src using LZ4-block format.  Returns
// (compressed, true) on success, or (nil, false) if the value is too
// small, too large, or incompressible (compressed >= original).
//
// Layout of returned slice: [originalSize:4 LE][lz4 block data].
func (c *LZ4) Compress(src []byte) ([]byte, bool) {
	n := len(src)
	if n < c.MinSize || n > maxInputSize {
		return nil, false
	}

	// Worst-case LZ4 output = input + input/255 + 16.
	maxOut := sizePrefix + n + n/255 + 16
	dst := c.pool.get(maxOut)

	encodeSizePrefix(dst, uint32(n))
	compLen := compressBlock(dst[sizePrefix:sizePrefix], src)

	if compLen <= 0 || sizePrefix+compLen >= n {
		// Incompressible — compressed output ≥ original.
		c.pool.put(dst)
		return nil, false
	}

	result := dst[:sizePrefix+compLen]
	return result, true
}

// Decompress decompresses an LZ4-block payload.  The first 4 bytes
// encode the original size (LE).
func (c *LZ4) Decompress(src []byte) ([]byte, error) {
	if len(src) < sizePrefix {
		return nil, ErrCorrupt
	}
	origSize := int(decodeSizePrefix(src))
	if origSize < 0 || origSize > maxInputSize {
		return nil, ErrTooLarge
	}
	dst := c.pool.get(origSize)
	n, err := decompressBlock(dst[:origSize], src[sizePrefix:])
	if err != nil {
		c.pool.put(dst)
		return nil, err
	}
	if n != origSize {
		c.pool.put(dst)
		return nil, fmt.Errorf("lz4: decompressed %d bytes, expected %d", n, origSize)
	}
	return dst[:origSize], nil
}

// ──────────────────────────────────────────────────────────────────
// LZ4-block encoder
// ──────────────────────────────────────────────────────────────────

// hashU32 computes the hash for a 4-byte window.
func hashU32(v uint32) uint32 {
	return (v * 2654435761) >> hashShift
}

// read32 reads a little-endian uint32 from p.
func read32(p []byte) uint32 {
	return binary.LittleEndian.Uint32(p)
}

// compressBlock compresses src into dst (which must have enough
// capacity).  Returns the number of compressed bytes written to dst,
// or 0 if the input cannot be compressed.
//
// dst should be a slice with len=0 and sufficient cap.
func compressBlock(dst, src []byte) int {
	sn := len(src)
	if sn < minMatch+lastLiterals {
		// Too short to compress.
		return 0
	}

	var hashTable [hashTableSize]uint32

	dp := 0 // dst write position
	sp := 0 // src read position
	anchor := 0
	mfLimit := sn - lastLiterals - minMatch

	for sp <= mfLimit {
		// Hash the 4 bytes at sp.
		h := hashU32(read32(src[sp:]))
		ref := int(hashTable[h])
		hashTable[h] = uint32(sp)

		// Check if we have a match.
		if ref == 0 || sp-ref >= winSize || read32(src[ref:]) != read32(src[sp:]) {
			sp++
			continue
		}

		// Emit literals from anchor to sp.
		litLen := sp - anchor
		matchLen := matchLength(src, ref+minMatch, sp+minMatch, sn)

		// Encode token.
		token := byte(0)
		if litLen >= 15 {
			token = 0xF0
		} else {
			token = byte(litLen << 4)
		}
		ml := matchLen // match length minus minMatch
		if ml >= 15 {
			token |= 0x0F
		} else {
			token |= byte(ml)
		}

		// Grow dst as needed.
		dp = appendByte(dst, dp, token)

		// Extra literal length bytes.
		if litLen >= 15 {
			dp = appendLen(dst, dp, litLen-15)
		}

		// Copy literals.
		dp = appendBytes(dst, dp, src[anchor:anchor+litLen])

		// Encode offset (2 bytes LE).
		offset := sp - ref
		dp = appendByte(dst, dp, byte(offset))
		dp = appendByte(dst, dp, byte(offset>>8))

		// Extra match length bytes.
		if ml >= 15 {
			dp = appendLen(dst, dp, ml-15)
		}

		// Advance past the match.
		sp += minMatch + matchLen
		anchor = sp

		// Update hash for positions inside the match to improve
		// compression ratio on repetitive data.
		if sp <= mfLimit {
			hashTable[hashU32(read32(src[sp-2:]))] = uint32(sp - 2)
		}
	}

	// Emit the last literals (the LZ4 spec requires that the block
	// ends with at least lastLiterals literal bytes).
	litLen := sn - anchor
	if litLen == 0 && dp == 0 {
		return 0 // nothing was compressed
	}
	token := byte(0)
	if litLen >= 15 {
		token = 0xF0
	} else {
		token = byte(litLen << 4)
	}
	dp = appendByte(dst, dp, token)
	if litLen >= 15 {
		dp = appendLen(dst, dp, litLen-15)
	}
	dp = appendBytes(dst, dp, src[anchor:anchor+litLen])

	return dp
}

// matchLength returns the number of matching bytes starting from
// ref and cur, up to end.  The first minMatch bytes are assumed to
// already match.
func matchLength(src []byte, ref, cur, end int) int {
	n := 0
	for cur+n < end && ref+n < end && src[ref+n] == src[cur+n] {
		n++
	}
	return n
}

// appendByte writes one byte to dst at position dp, using the
// underlying capacity.  Returns dp+1.
func appendByte(dst []byte, dp int, b byte) int {
	dst = dst[:dp+1]
	dst[dp] = b
	return dp + 1
}

// appendBytes copies src to dst at position dp.  Returns dp+len(src).
func appendBytes(dst []byte, dp int, src []byte) int {
	if len(src) == 0 {
		return dp
	}
	dst = dst[:dp+len(src)]
	copy(dst[dp:], src)
	return dp + len(src)
}

// appendLen writes LZ4-style variable-length encoding: the "extra"
// length after the first 15 is written as a series of 0xFF bytes
// followed by a final byte < 0xFF.
func appendLen(dst []byte, dp int, length int) int {
	for length >= 255 {
		dp = appendByte(dst, dp, 255)
		length -= 255
	}
	dp = appendByte(dst, dp, byte(length))
	return dp
}

// ──────────────────────────────────────────────────────────────────
// LZ4-block decoder
// ──────────────────────────────────────────────────────────────────

// decompressBlock decompresses LZ4-block data from src into dst.
// dst must have exactly the right capacity (= original size).
// Returns the number of bytes written.
func decompressBlock(dst, src []byte) (int, error) {
	sn := len(src)
	dn := len(dst)
	sp := 0 // source position
	dp := 0 // dest position

	for sp < sn {
		// Read token.
		token := src[sp]
		sp++

		// Literal length.
		litLen := int(token >> 4)
		if litLen == 15 {
			for sp < sn {
				extra := int(src[sp])
				sp++
				litLen += extra
				if extra < 255 {
					break
				}
			}
		}

		// Copy literals.
		if litLen > 0 {
			if sp+litLen > sn || dp+litLen > dn {
				return 0, ErrCorrupt
			}
			copy(dst[dp:dp+litLen], src[sp:sp+litLen])
			sp += litLen
			dp += litLen
		}

		// Check if this is the last sequence (no match follows).
		if sp >= sn {
			break
		}

		// Read offset (2 bytes LE).
		if sp+2 > sn {
			return 0, ErrCorrupt
		}
		offset := int(src[sp]) | int(src[sp+1])<<8
		sp += 2
		if offset == 0 {
			return 0, ErrCorrupt
		}

		// Match length.
		matchLen := int(token & 0x0F)
		if matchLen == 15 {
			for sp < sn {
				extra := int(src[sp])
				sp++
				matchLen += extra
				if extra < 255 {
					break
				}
			}
		}
		matchLen += minMatch

		// Copy match (with overlap handling for run-length patterns).
		matchPos := dp - offset
		if matchPos < 0 || dp+matchLen > dn {
			return 0, ErrCorrupt
		}

		// Byte-by-byte copy handles overlapping matches correctly
		// (e.g. offset=1 means repeat the last byte).
		for i := 0; i < matchLen; i++ {
			dst[dp+i] = dst[matchPos+i]
		}
		dp += matchLen
	}

	return dp, nil
}
