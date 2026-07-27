//go:build !linux

package arena

// mmapRegion allocates a region using make (dev fallback).
func mmapRegion(size uint64) ([]byte, error) {
	return make([]byte, size), nil
}

// munmapRegion is a no-op for the non-Linux fallback.
func munmapRegion(data []byte) error {
	return nil
}

// syncRegion is a no-op for the non-Linux fallback.
func syncRegion(data []byte) error {
	return nil
}
