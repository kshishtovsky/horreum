//go:build !linux

package arena

import (
	"fmt"
	"os"
)

// mmapRegion allocates an anonymous region using make. Used on
// non-Linux platforms where unix.Mmap is unavailable.
func mmapRegion(size uint64) ([]byte, error) {
	return make([]byte, size), nil
}

// mmapFileRegion opens (or creates) a file and returns a slice backed
// by it.  On non-Linux platforms we cannot MAP_SHARED; this
// implementation falls back to mmap-ing into anonymous memory and
// persisting on Sync/Close.
func mmapFileRegion(path string, size uint64, create bool) ([]byte, error) {
	if create {
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
		if err != nil {
			return nil, fmt.Errorf("persist: open %s: %w", path, err)
		}
		_ = f.Close()
	} else {
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("persist: stat %s: %w", path, err)
		}
	}
	// Fallback: anonymous mapping. Persistence is handled by the
	// WAL/Checkpoint layers above this file.
	return make([]byte, size), nil
}

// munmapRegion is a no-op for the non-Linux fallback.
func munmapRegion(data []byte) error {
	return nil
}

// syncRegion is a no-op for the non-Linux fallback.  The persist
// layer relies on the WAL for durability on non-Linux platforms.
func syncRegion(data []byte) error {
	return nil
}

// syncRegionAsync is a no-op for the non-Linux fallback.
func syncRegionAsync(data []byte) error {
	return nil
}
