// Package persist implements persistent storage on top of the arena
// package.  It adds:
//
//   - file-backed mmap (via arena.OpenFileManager)
//   - a fixed-size superblock stored at the start of the arena file
//   - an append-only Write-Ahead Log (WAL) for SET/DEL operations
//   - a checkpoint mechanism that snapshots the in-memory HashIndex
//     into a fixed region of the arena file
//
// Crash safety: SET writes the value to the arena, appends a WAL
// record, and (in durable mode) fsyncs the WAL before returning
// success.  On restart, the cold-start path reads the most recent
// checkpoint, then replays any WAL records appended after it.
package persist

import (
	"encoding/binary"
	"errors"
)

// superblockSize is the reserved area at the start of the arena file.
const superblockSize = 4096

// Superblock layout.  All fields little-endian.
//
// Offset  Size  Field
// ------  ----  ----------------------------------------
//
//	 0       8   magic = "HORREUM\0"
//	 8       4   version
//	12       8   regionSize (bytes)
//	20       8   indexOffset (offset of HashIndex checkpoint)
//	28       8   indexLen (bytes)
//	36       8   walOffset (offset of WAL head, post-checkpoint)
//	44       4   flags (bit 0 = durability enabled)
//	48       8   checkpointCount (entries in last checkpoint)
//	56     ...   reserved (zero-filled)
const (
	magic       = "HORREUM\x00" // 8 bytes including trailing NUL
	magicLen    = 8
	version     = uint32(1)
	flagDurable = uint32(1 << 0)
)

// Superblock is the on-disk metadata header.
type Superblock struct {
	Version         uint32
	RegionSize      uint64
	IndexOffset     uint64
	IndexLen        uint64
	WALOffset       uint64
	Flags           uint32
	CheckpointCount uint64
}

// Errors returned by superblock helpers.
var (
	ErrBadMagic   = errors.New("persist: bad superblock magic")
	ErrBadVersion = errors.New("persist: unsupported superblock version")
)

// EncodeSuperblockForTest is a test-only re-export of encodeSuperblock.
func EncodeSuperblockForTest(buf []byte, sb Superblock) {
	encodeSuperblock(buf, sb)
}

// DecodeSuperblockForTest is a test-only re-export of decodeSuperblock.
func DecodeSuperblockForTest(buf []byte) (Superblock, error) {
	return decodeSuperblock(buf)
}

// encodeSuperblock writes sb into buf (must be ≥ superblockSize bytes).
func encodeSuperblock(buf []byte, sb Superblock) {
	if len(buf) < superblockSize {
		panic("persist: superblock buffer too small")
	}
	// Zero the buffer first so reserved area is clean.
	for i := range buf[:superblockSize] {
		buf[i] = 0
	}
	copy(buf[0:], magic)
	binary.LittleEndian.PutUint32(buf[8:12], sb.Version)
	binary.LittleEndian.PutUint64(buf[12:20], sb.RegionSize)
	binary.LittleEndian.PutUint64(buf[20:28], sb.IndexOffset)
	binary.LittleEndian.PutUint64(buf[28:36], sb.IndexLen)
	binary.LittleEndian.PutUint64(buf[36:44], sb.WALOffset)
	binary.LittleEndian.PutUint32(buf[44:48], sb.Flags)
	binary.LittleEndian.PutUint64(buf[48:56], sb.CheckpointCount)
}

// decodeSuperblock parses buf into sb.  Returns an error if the magic
// or version is invalid.
func decodeSuperblock(buf []byte) (Superblock, error) {
	if len(buf) < superblockSize {
		return Superblock{}, errors.New("persist: superblock buffer too small")
	}
	if string(buf[0:magicLen]) != magic {
		return Superblock{}, ErrBadMagic
	}
	ver := binary.LittleEndian.Uint32(buf[8:12])
	if ver != version {
		return Superblock{}, ErrBadVersion
	}
	return Superblock{
		Version:         ver,
		RegionSize:      binary.LittleEndian.Uint64(buf[12:20]),
		IndexOffset:     binary.LittleEndian.Uint64(buf[20:28]),
		IndexLen:        binary.LittleEndian.Uint64(buf[28:36]),
		WALOffset:       binary.LittleEndian.Uint64(buf[36:44]),
		Flags:           binary.LittleEndian.Uint32(buf[44:48]),
		CheckpointCount: binary.LittleEndian.Uint64(buf[48:56]),
	}, nil
}

// indexCheckpointMaxLen returns the maximum size of the index
// checkpoint area.  Sized as min(regionSize/8, 1 MiB).
func indexCheckpointMaxLen(regionSize uint64) uint64 {
	const oneMiB = uint64(1 << 20)
	v := regionSize / 8
	if v > oneMiB {
		v = oneMiB
	}
	if v < 4096 {
		v = 4096
	}
	return v
}

// walMaxLen returns the maximum WAL size (capped at 64 MiB for v1).
func walMaxLen(regionSize uint64) uint64 {
	const cap = uint64(64 << 20)
	v := regionSize / 4
	if v > cap {
		v = cap
	}
	if v < 4096 {
		v = 4096
	}
	return v
}
