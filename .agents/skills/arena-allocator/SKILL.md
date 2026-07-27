---
name: arena-allocator
description: Bump allocator with segregated freelist for mmap'd arenas. Size classes, alignment, zero heap allocs.
version: "1.0.0"
tags: [go, arena, allocator, bump, freelist]
---

# Arena Allocator

## Design

Two-phase allocation:
1. **Freelist** — check segregated free lists by size class first.
2. **Bump** — atomically advance offset pointer if freelist empty.

## Size Classes

12 classes, each power-of-two aligned to 8 bytes:

| Class | Size |
|---|---|
| 0 | 8 |
| 1 | 16 |
| 2 | 32 |
| 3 | 64 |
| 4 | 128 |
| 5 | 256 |
| 6 | 512 |
| 7 | 1024 |
| 8 | 2048 |
| 9 | 4096 |
| 10 | 8192 |
| 11 | >8192 (bump only) |

## Alignment

All offsets are 8-byte aligned. The bump pointer is always advanced to the next 8-byte boundary after each allocation.

## Freelist Node Layout

Each free block in mmap'd memory stores:
```
offset 0: next offset (uint32)
offset 4: size (uint32)
offset 8: unused (padding to 8 bytes)
```

The freelist head is stored in the Allocator struct (in Go memory), pointing into mmap'd memory.

## Concurrency

- Bump pointer: `atomic.Uint64` — lock-free CAS loop.
- Freelist: `sync.Mutex` — brief hold, bounded by size class.
- Hot path (bump) never takes a lock.

## Region Layout

```
[0 ... dataOffset)     = usable data space
```

No header in mmap'd memory — all metadata lives in Go structs.

## Anti-Patterns

- `append()` to mmap'd slice — breaks zero-copy.
- Growing region via MREMAP — causes fragmentation. Rotate to new region.
- Storing `*byte` in Handle — pointer escapes to heap. Use offset + region ID.
