// checkpoint.go — HashIndex checkpoint serialisation.
//
// The checkpoint is written into the [IndexOffset .. IndexOffset+IndexLen)
// region of the arena file.  Its on-disk layout (v1):
//
//	┌──────────┬──────────┬──────────┬──────────┐
//	│ magic(4) │ version  │ count(4) │ entries  │
//	│ "IDX\0"  │ (u32=1)  │          │ ...      │
//	└──────────┴──────────┴──────────┴──────────┘
//
// Each entry (24-byte fixed header + inline key bytes):
//
//	┌──────────┬──────────┬──────────┬──────────┬──────────┬──────────┬──────────┬──────────┐
//	│ hash(8)  │ keyLen(2)│ hOff(4)  │ hSize(4) │ hReg(1)  │ hMeta(1) │ key[0..3]│ key>4..  │
//	└──────────┴──────────┴──────────┴──────────┴──────────┴──────────┴──────────┴──────────┘
//
//	bytes  0..7   = hash (u64 LE)
//	bytes  8..9   = key length (u16 LE)
//	bytes 10..13  = handle.Offset (u32 LE)
//	bytes 14..17  = handle.Size   (u32 LE)
//	bytes 18      = handle.Region (u8)
//	bytes 19      = handle.Meta   (u8, currently zero — reserved for freq bits)
//	bytes 20..23  = first 4 bytes of key inline (zero-padded if shorter)
//	bytes 24..    = remaining key bytes if keyLen > 4
//
// v1 inline strategy: keys are written immediately after the fixed 20-byte
// header.  This is simple and matches the spec ("v1: inline").
//
// Crash safety:
//   - The superblock's CheckpointCount field is updated LAST and fsync'd
//     (MS_SYNC).  A partial checkpoint is detected either by a missing
//     "IDX\0" magic (fresh region) or by a truncated body.
package persist

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/horreum/horreum/internal/arena"
	"github.com/horreum/horreum/internal/index"
)

// Checkpoint magic.
var checkpointMagic = [4]byte{'I', 'D', 'X', 0}

// Checkpoint format version.
const checkpointVersion uint32 = 1

// Checkpoint fixed header size.
const checkpointHeaderSize = 4 + 4 + 4 // magic(4) + version(4) + count(4)

// Per-entry fixed prefix size: hash(8) + keyLen(2) + hOff(4) + hSize(4) +
// hReg(1) + hMeta(1) + 4 bytes of inline key = 24 bytes.
const checkpointEntryFixedSize = 8 + 2 + 4 + 4 + 1 + 1 + 4

// checkpointMaxSize returns the maximum byte size of a checkpoint for
// the configured region size.  Caps at 1 MiB.
func checkpointMaxSize(regionSize uint64) uint64 {
	return indexCheckpointMaxLen(regionSize)
}

// indexCheckpointOffset returns the offset of the HashIndex checkpoint
// region within the arena file (immediately after the superblock).
func indexCheckpointOffset() uint64 {
	return uint64(superblockSize)
}

// encodeCheckpoint serialises snap into buf.  Returns the number of
// bytes written.
func encodeCheckpoint(buf []byte, snap []index.SnapshotEntry) (int, error) {
	if len(buf) < checkpointHeaderSize {
		return 0, errors.New("persist: checkpoint buffer too small")
	}
	copy(buf[0:4], checkpointMagic[:])
	binary.LittleEndian.PutUint32(buf[4:8], checkpointVersion)
	binary.LittleEndian.PutUint32(buf[8:12], uint32(len(snap)))

	pos := checkpointHeaderSize
	for i, se := range snap {
		if len(se.Key) > 0xFFFF {
			return 0, fmt.Errorf("persist: entry %d key too long (%d)", i, len(se.Key))
		}
		// Size: fixed 24 bytes + max(0, keyLen - 4) for overflow.
		entrySize := checkpointEntryFixedSize
		if len(se.Key) > 4 {
			entrySize += len(se.Key) - 4
		}
		if pos+entrySize > len(buf) {
			return 0, fmt.Errorf("persist: checkpoint buffer overflow at entry %d (pos=%d need=%d cap=%d)",
				i, pos, entrySize, len(buf))
		}
		entry := buf[pos : pos+entrySize]
		binary.LittleEndian.PutUint64(entry[0:8], se.Hash)
		binary.LittleEndian.PutUint16(entry[8:10], uint16(len(se.Key)))
		binary.LittleEndian.PutUint32(entry[10:14], se.Handle.Offset)
		binary.LittleEndian.PutUint32(entry[14:18], se.Handle.Size)
		entry[18] = se.Handle.Region
		entry[19] = uint8(se.Handle.Meta)
		// Inline first 4 bytes of key (zero-padded if shorter).
		inline := 4
		if len(se.Key) < 4 {
			inline = len(se.Key)
		}
		copy(entry[20:20+inline], se.Key[:inline])
		for j := inline; j < 4; j++ {
			entry[20+j] = 0
		}
		// Remaining key bytes after the fixed prefix.
		if len(se.Key) > 4 {
			copy(entry[checkpointEntryFixedSize:], se.Key[4:])
		}
		pos += entrySize
	}
	return pos, nil
}

// decodeCheckpoint parses buf into a slice of SnapshotEntry.  Returns
// the parsed entries and the byte count consumed.
func decodeCheckpoint(buf []byte) ([]index.SnapshotEntry, int, error) {
	if len(buf) < checkpointHeaderSize {
		return nil, 0, errors.New("persist: checkpoint buffer too small")
	}
	if [4]byte{buf[0], buf[1], buf[2], buf[3]} != checkpointMagic {
		return nil, 0, ErrCheckpointBadMagic
	}
	ver := binary.LittleEndian.Uint32(buf[4:8])
	if ver != checkpointVersion {
		return nil, 0, fmt.Errorf("persist: unsupported checkpoint version %d", ver)
	}
	count := binary.LittleEndian.Uint32(buf[8:12])
	pos := checkpointHeaderSize
	out := make([]index.SnapshotEntry, 0, count)
	for i := uint32(0); i < count; i++ {
		if pos+checkpointEntryFixedSize > len(buf) {
			return nil, 0, fmt.Errorf("persist: checkpoint truncated at entry %d", i)
		}
		entry := buf[pos:]
		hash := binary.LittleEndian.Uint64(entry[0:8])
		keyLen := int(binary.LittleEndian.Uint16(entry[8:10]))
		hOff := binary.LittleEndian.Uint32(entry[10:14])
		hSize := binary.LittleEndian.Uint32(entry[14:18])
		hReg := entry[18]
		hMeta := uint16(entry[19])
		inline := 4
		if keyLen < 4 {
			inline = keyLen
		}
		key := make([]byte, keyLen)
		copy(key[:inline], entry[20:20+inline])
		pos += checkpointEntryFixedSize
		if keyLen > 4 {
			if pos+(keyLen-4) > len(buf) {
				return nil, 0, fmt.Errorf("persist: checkpoint key truncated at entry %d", i)
			}
			copy(key[4:], buf[pos:pos+(keyLen-4)])
			pos += keyLen - 4
		}
		out = append(out, index.SnapshotEntry{
			Entry: index.Entry{
				Hash:   hash,
				KeyLen: uint16(keyLen),
				Handle: arena.Handle{
					Offset: hOff,
					Size:   hSize,
					Region: hReg,
					Meta:   hMeta,
				},
			},
			Key: key,
		})
	}
	return out, pos, nil
}

// ErrCheckpointBadMagic is returned when the checkpoint bytes do not
// start with the expected "IDX\0" magic.  Distinguishes "no
// checkpoint yet" from "checkpoint corruption".
var ErrCheckpointBadMagic = errors.New("persist: checkpoint bad magic")

// writeCheckpoint serialises snap and writes it into the arena file's
// index region (IndexOffset..IndexOffset+IndexLen).
//
// Sequence:
//
//  1. Encode snap into a stack buffer sized to checkpointMaxSize.
//  2. Copy bytes into arena file at indexCheckpointOffset().
//  3. Sync the arena (MS_SYNC).
//  4. Update the superblock's CheckpointCount + WALOffset fields.
//  5. Write + sync the superblock.
//
// If anything fails after step 2, the next LoadIndex will see a
// truncated checkpoint (ErrCheckpointBadMagic on the trailing bytes
// or a count mismatch) and fall back to full WAL replay.
func writeCheckpoint(m *arena.Manager, snap []index.SnapshotEntry, regionSize uint64) (int, error) {
	maxSize := checkpointMaxSize(regionSize)
	buf := make([]byte, maxSize)
	n, err := encodeCheckpoint(buf, snap)
	if err != nil {
		return 0, err
	}
	buf = buf[:n]

	// 1. Write the body.
	if err := copyToArena(m, indexCheckpointOffset(), buf); err != nil {
		return 0, fmt.Errorf("persist: write checkpoint body: %w", err)
	}
	// 2. Sync the body.
	if err := m.Sync(); err != nil {
		return 0, fmt.Errorf("persist: sync after checkpoint body: %w", err)
	}

	// 3. Update superblock: checkpoint count + WAL offset = 0.
	sb, err := readSuperblock(m)
	if err != nil {
		return 0, err
	}
	sb.CheckpointCount = uint64(len(snap))
	sb.WALOffset = 0
	if err := setSuperblock(m, sb); err != nil {
		return 0, err
	}
	// 4. Sync superblock.
	if err := m.Sync(); err != nil {
		return 0, fmt.Errorf("persist: sync after checkpoint superblock: %w", err)
	}
	return n, nil
}

// loadCheckpoint reads the checkpoint body from the arena file and
// returns the deserialised entries.
//
// Returns ErrCheckpointBadMagic if the body's magic is missing or
// wrong (which means "no checkpoint has ever been written").
func loadCheckpoint(m *arena.Manager, regionSize uint64) ([]index.SnapshotEntry, error) {
	maxSize := checkpointMaxSize(regionSize)
	body := make([]byte, maxSize)
	if err := copyArenaTo(m, indexCheckpointOffset(), body); err != nil {
		return nil, fmt.Errorf("persist: read checkpoint body: %w", err)
	}
	if [4]byte{body[0], body[1], body[2], body[3]} != checkpointMagic {
		return nil, ErrCheckpointBadMagic
	}
	entries, _, err := decodeCheckpoint(body)
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// EncodeCheckpointForTest is a test-only re-export of encodeCheckpoint.
func EncodeCheckpointForTest(buf []byte, snap []index.SnapshotEntry) (int, error) {
	return encodeCheckpoint(buf, snap)
}

// DecodeCheckpointForTest is a test-only re-export of decodeCheckpoint.
func DecodeCheckpointForTest(buf []byte) ([]index.SnapshotEntry, int, error) {
	return decodeCheckpoint(buf)
}
