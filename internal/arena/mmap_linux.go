//go:build linux

package arena

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// mmapRegion allocates an anonymous private region (MAP_ANON|MAP_PRIVATE).
// Used for non-persistent arenas.
func mmapRegion(size uint64) ([]byte, error) {
	// SAFETY: MAP_ANON|MAP_PRIVATE creates a private anonymous mapping.
	// No file backing — memory is zero-filled on first access.
	data, err := unix.Mmap(-1, 0, int(size), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_ANON|unix.MAP_PRIVATE)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// mmapFileRegion opens (or creates) a file at path and maps it with
// MAP_SHARED.  If create is true and the file does not exist, it is
// created and sized to size bytes.  The returned slice is backed by
// the file; writes are visible across processes and survive crashes.
func mmapFileRegion(path string, size uint64, create bool) ([]byte, error) {
	if create {
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
		if err != nil {
			return nil, fmt.Errorf("persist: open %s: %w", path, err)
		}
		defer f.Close()
		// Resize the file to size bytes.  If the file already exists
		// and is larger, leave it; if smaller, extend.
		st, err := f.Stat()
		if err != nil {
			return nil, fmt.Errorf("persist: stat %s: %w", path, err)
		}
		if st.Size() < int64(size) {
			if err := f.Truncate(int64(size)); err != nil {
				return nil, fmt.Errorf("persist: truncate %s: %w", path, err)
			}
		}
	} else {
		// Open existing.
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("persist: stat %s: %w", path, err)
		}
	}

	fd, err := unix.Open(path, unix.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("persist: unix.Open %s: %w", path, err)
	}
	// SAFETY: MAP_SHARED with PROT_READ|PROT_WRITE on a file-backed
	// fd; size must match the file size at mmap time.  unix.Mmap
	// returns a slice whose backing store is the file contents.
	data, err := unix.Mmap(fd, 0, int(size), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("persist: unix.Mmap %s: %w", path, err)
	}
	_ = unix.Close(fd)
	return data, nil
}

// munmapRegion releases a mmap'd region.
func munmapRegion(data []byte) error {
	// SAFETY: data was returned by unix.Mmap and has not been sub-sliced
	// in a way that changes the base pointer.
	return unix.Munmap(data)
}

// syncRegion flushes dirty pages. For MAP_ANON this is a no-op;
// for MAP_SHARED it writes to the backing file.
func syncRegion(data []byte) error {
	// SAFETY: MS_SYNC flushes dirty pages to backing store.
	return unix.Msync(data, unix.MS_SYNC)
}

// syncRegionAsync schedules dirty pages to be flushed. Faster than
// MS_SYNC but does not block until the write completes.
func syncRegionAsync(data []byte) error {
	return unix.Msync(data, unix.MS_ASYNC)
}

// isAligned checks if a pointer is aligned to the given boundary.
func isAligned(ptr uintptr, align uintptr) bool {
	return ptr%align == 0
}

// hugepageAlign returns an aligned sub-slice if regionSize >= 512 MiB.
func hugepageAlign(data []byte, regionSize uint64) []byte {
	if regionSize < 512<<20 {
		return data
	}
	// SAFETY: &data[0] is the mmap'd base pointer.
	// We compute the offset needed to align to 2 MiB.
	base := uintptr(unsafe.Pointer(&data[0]))
	const hugepageSize = 2 << 20 // 2 MiB
	offset := hugepageSize - (base % hugepageSize)
	if offset == hugepageSize {
		offset = 0
	}
	// Return aligned sub-slice.
	return data[offset : offset+uintptr(regionSize)]
}
