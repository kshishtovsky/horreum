package arena

import "unsafe"

// unsafeView returns a zero-copy slice backed by mmap'd memory.
// The slice header lives on the caller's stack; the backing store
// is the mmap region, which is not traced by the GC.
//
// SAFETY: data is a mmap'd region. offset and size are validated
// by the caller (Manager.View) against region bounds.
// The returned slice does not escape — its backing store is outside
// the Go heap. No heap allocation occurs.
func unsafeView(data []byte, offset, size uint32) []byte {
	// SAFETY: &data[offset] is within the mmap'd region.
	// unsafe.Add advances the pointer by offset bytes.
	// unsafe.Slice creates a slice header on the stack.
	base := unsafe.Pointer(&data[offset])
	return unsafe.Slice((*byte)(base), size)
}
