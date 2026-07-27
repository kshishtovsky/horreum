---
name: go-zero-copy
description: Zero-copy patterns for Go — unsafe.Slice, unsafe.Add, no append to mmap'd memory.
version: "1.0.0"
tags: [go, unsafe, zero-copy, mmap]
---

# Go Zero-Copy Patterns

## Core Rule

Return slices backed by mmap'd memory. No copying, no heap allocation.

## unsafe.Slice

```go
// SAFETY: p points into mmap'd region; len is validated against region bounds.
// The returned slice header lives on the stack; the backing store is the mmap.
b := unsafe.Slice((*byte)(unsafe.Add(base, offset)), length)
```

## unsafe.Add

```go
// SAFETY: base is the mmap'd region base pointer; offset is validated.
p := unsafe.Add(base, offset)
```

## Banned Patterns

| Pattern | Why banned | Replacement |
|---|---|---|
| `append(mmapSlice, ...)` | Redirects into Go heap allocator | `copy(dst[off:], src)` |
| `reflect.SliceHeader` | Deprecated, causes escape (issue #72732) | `unsafe.Slice` |
| `&slice[0]` passed to escaping function | Pointer escapes to heap | Keep slice local; copy via `unsafe.Slice` |
| `interface{}` on hot path | Boxing allocates | Monomorphic types |

## Escape Rules

- Slice returned by `unsafe.Slice` over mmap'd memory does NOT escape to heap — the backing store is outside GC.
- The slice header (24 bytes) lives on stack if not captured by interface or closure.
- Verify with `go build -gcflags="-m -l"` — expect no "escapes to heap" for the hot path.

## SAFETY Comment Template

Every `unsafe` usage MUST have a comment:
```
// SAFETY: <pointer> points into mmap'd region <region>.
// <offset> is validated against region bounds [0, region.size).
// The returned slice does not escape — backing store is mmap, not heap.
```
