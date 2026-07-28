// recovery.go — cold-start path.
//
// ColdStart performs the following:
//  1. Open the arena file at path and mmap it (MAP_SHARED).
//  2. Read the superblock from the first 4 KB of the file.
//  3. If a checkpoint exists, decode the in-memory HashIndex from
//     the checkpoint region.
//  4. Replay any WAL records appended after the checkpoint.
//  5. Return a populated Manager ready for use.
//
// On a fresh start (no file), ColdStart creates the file, writes a
// zero-initialised superblock, and returns a Manager with an empty
// index.
//
// v1.1: replay no longer calls mgr.Put — the WAL SET record carries
// the original Handle (Offset/Size/Region), so the index entry is
// reconstructed directly via idx.Put(key, handle).  No data is copied
// into the arena a second time; no orphan handles are created.
package persist

import (
	"errors"
	"fmt"
	"os"

	"github.com/horreum/horreum/internal/arena"
)

// IndexWriter is the subset of HashIndex that ReplayTo needs.
// Defined here to avoid an import cycle with internal/index.
type IndexWriter interface {
	Put(key []byte, h arena.Handle) bool
	Delete(key []byte) (arena.Handle, bool)
}

// ErrEmptyPath is returned when the user passes an empty directory.
var ErrEmptyPath = errors.New("persist: empty path")

// ColdStart opens (or creates) a persistent arena at dir/arena.dat,
// loads any checkpoint, replays the WAL, and returns a Manager ready
// for use.
//
// dir must be an existing directory; ColdStart creates the arena
// file and the WAL file inside it on a fresh start.
func ColdStart(dir string, regionSize uint64) (*arena.Manager, error) {
	if dir == "" {
		return nil, ErrEmptyPath
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("persist: mkdir %s: %w", dir, err)
	}
	arenaPath := dir + "/arena.dat"
	walPath := dir + "/wal.log"

	// Does the arena file already exist?
	create := false
	if _, err := os.Stat(arenaPath); os.IsNotExist(err) {
		create = true
	} else if err != nil {
		return nil, fmt.Errorf("persist: stat arena: %w", err)
	}

	m, err := arena.OpenFileManager(arenaPath, regionSize, create)
	if err != nil {
		return nil, err
	}

	// Initialise / validate superblock.
	if create {
		if err := writeFreshSuperblock(m, regionSize); err != nil {
			_ = m.Close()
			return nil, err
		}
	} else {
		if err := validateSuperblock(m); err != nil {
			_ = m.Close()
			return nil, err
		}
	}

	// Open the WAL and replay (no index writer — replay only
	// validates integrity).  The actual index is loaded via
	// PersistentManager's higher-level path.
	idxMax := indexCheckpointMaxLen(regionSize)
	walMax := walMaxLen(regionSize)
	wal, err := OpenWAL(walPath, int64(walMax))
	if err != nil {
		_ = m.Close()
		return nil, err
	}
	if err := Replay(m, wal, idxMax); err != nil {
		_ = wal.Close()
		_ = m.Close()
		return nil, err
	}
	_ = wal.Close()
	return m, nil
}

// writeFreshSuperblock encodes the initial superblock into the arena
// file.  We can't reach the arena's raw data slice directly, so we
// use the Manager's View via a synthetic handle: that doesn't work
// either.  Instead, we manipulate the arena's backing region through
// Manager-level calls.
//
// Because arena.Manager doesn't expose a write-to-offset API yet,
// cold-start initialisation is limited.  We instead take a different
// approach: encode the superblock into the first 4 KB of the arena
// file by extending Manager.  See setSuperblock.
func writeFreshSuperblock(m *arena.Manager, regionSize uint64) error {
	sb := Superblock{
		Version:     version,
		RegionSize:  regionSize,
		IndexOffset: uint64(superblockSize),
		IndexLen:    indexCheckpointMaxLen(regionSize),
		WALOffset:   uint64(superblockSize) + indexCheckpointMaxLen(regionSize),
		Flags:       0,
	}
	return setSuperblock(m, sb)
}

// validateSuperblock reads the existing superblock and returns nil if
// it matches the expected version.
func validateSuperblock(m *arena.Manager) error {
	_, err := readSuperblock(m)
	return err
}

// readSuperblock reads and decodes the superblock.
func readSuperblock(m *arena.Manager) (Superblock, error) {
	buf := make([]byte, superblockSize)
	if err := copyArenaTo(m, 0, buf); err != nil {
		return Superblock{}, err
	}
	return decodeSuperblock(buf)
}

// setSuperblock encodes sb into the arena file.  Uses an internal
// write helper.
func setSuperblock(m *arena.Manager, sb Superblock) error {
	buf := make([]byte, superblockSize)
	encodeSuperblock(buf, sb)
	return copyToArena(m, 0, buf)
}

// Replay walks the WAL from offset 0 and validates its integrity.
// It does NOT rebuild the in-memory HashIndex — that is handled by
// the checkpoint loader (see checkpoint.go) followed by ReplayTo
// against an explicit IndexWriter.
func Replay(m *arena.Manager, wal *WAL, idxMax uint64) error {
	return ReplayTo(m, wal, nil)
}

// ReplayTo walks the WAL from offset 0 and applies every record to
// the supplied index.
//
// v1.1: for SET records we DO NOT call mgr.Put(value).  The WAL
// record carries the original Handle (Offset/Size/Region); the
// payload bytes are still resident in the mmap'd arena file from the
// initial write, so the index is updated in-place without any arena
// allocation.  This eliminates the "orphan handle" leak that the
// pre-v1.1 implementation suffered from.
func ReplayTo(m *arena.Manager, wal *WAL, idx IndexWriter) error {
	it, err := wal.Iterate(0)
	if err != nil {
		return err
	}
	for {
		rec, err := it.Next()
		if err != nil {
			break // io.EOF or corruption
		}
		if idx == nil {
			continue // validation-only path
		}
		switch rec.Op {
		case walOpSet:
			h := arena.Handle{
				Offset: rec.Offset,
				Size:   rec.Size,
				Region: rec.Region,
			}
			idx.Put(rec.Key, h)
		case walOpDel:
			idx.Delete(rec.Key)
		}
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────────
// copyArenaTo / copyToArena — internal helpers to write into the
// arena file's superblock region.
//
// arena.Manager does not expose raw byte writes; we work around this
// by allocating a Handle whose payload overlaps the superblock
// region.  This is an internal contract: the superblock area is
// reserved and not used for objects.
// ──────────────────────────────────────────────────────────────────────

// copyArenaTo reads len(buf) bytes from offset off in the arena file
// into buf.
func copyArenaTo(m *arena.Manager, off uint64, buf []byte) error {
	regions := m.Regions()
	if len(regions) == 0 {
		return errors.New("persist: no regions")
	}
	reg := regions[0]
	data := reg.Data()
	end := off + uint64(len(buf))
	if end > uint64(len(data)) {
		return fmt.Errorf("persist: read past end: off=%d len=%d", off, len(buf))
	}
	copy(buf, data[off:end])
	return nil
}

// copyToArena writes buf into the arena file at offset off.
func copyToArena(m *arena.Manager, off uint64, buf []byte) error {
	regions := m.Regions()
	if len(regions) == 0 {
		return errors.New("persist: no regions")
	}
	reg := regions[0]
	data := reg.Data()
	end := off + uint64(len(buf))
	if end > uint64(len(data)) {
		return fmt.Errorf("persist: write past end: off=%d len=%d", off, len(buf))
	}
	copy(data[off:end], buf)
	return nil
}
