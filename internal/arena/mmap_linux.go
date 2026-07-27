//go:build linux

package arena

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

// mmapRegion allocates a region of the given size using mmap.
// The returned slice is backed by anonymous private memory.
func mmapRegion(size uint64) ([]byte, error) {
	// SAFETY: MAP_ANON|MAP_PRIVATE creates a private anonymous mapping.
	// No file backing — memory is zero-filled on first access.
	data, err := unix.Mmap(-1, 0, int(size), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_ANON|unix.MAP_PRIVATE)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// munmapRegion releases a mmap'd region.
func munmapRegion(data []byte) error {
	// SAFETY: data was returned by unix.Mmap and has not been sub-sliced
	// in a way that changes the base pointer.
	return unix.Munmap(data)
}

// syncRegion flushes dirty pages. For MAP_ANON this is a no-op.
func syncRegion(data []byte) error {
	// SAFETY: MS_SYNC flushes dirty pages to backing store.
	// For MAP_ANON, this is a no-op but good practice.
	return unix.Msync(data, unix.MS_SYNC)
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
