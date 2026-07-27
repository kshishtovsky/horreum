---
name: mmap-linux
description: Linux mmap for Go arenas — MAP_ANON, MAP_PRIVATE, hugepage alignment, Munmap, Msync, Madvise.
version: "1.0.0"
tags: [go, mmap, linux, memory]
---

# mmap on Linux

## Setup

```go
import "golang.org/x/sys/unix"

// SAFETY: MAP_ANON|MAP_PRIVATE creates a private anonymous mapping.
// No file backing — memory is zero-filled on first access.
data, err := unix.Mmap(-1, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_ANON|unix.PROT_PRIVATE)
```

## Flags

| Flag | Purpose |
|---|---|
| `MAP_ANON` | Anonymous mapping (no file) |
| `MAP_PRIVATE` | Private copy-on-write |
| `PROT_READ\|PROT_WRITE` | Read-write access |

## Hugepage Alignment

For hugepage-ready regions, align base address to 2 MiB:

```go
// Allocate extra for alignment
data, _ := unix.Mmap(-1, 0, size+align, ...)
// Calculate aligned offset
offset := align - (uintptr(unsafe.Pointer(&data[0])) % align)
aligned := data[offset : offset+uint(size)]
```

Or use `unix.Mmap` with a hint address (best-effort).

## Cleanup

```go
unix.Munmap(data)  // Returns memory to OS
```

## Msync

```go
// SAFETY: MS_SYNC flushes dirty pages to backing store.
// For MAP_ANON, this is a no-op but good practice.
unix.Msync(data, unix.MS_SYNC)
```

## Madvise

```go
// SAFETY: MADV_SEQUENTIAL hints sequential access pattern.
unix.Madvise(data, unix.MADV_SEQUENTIAL)

// MADV_HUGEPAGE only for regions >= 512 MiB
if regionSize >= 512<<20 {
    unix.Madvise(data, unix.MADV_HUGEPAGE)
}
```

## Build Tag

```go
//go:build linux
```

## Non-Linux Fallback

```go
//go:build !linux
// make([]byte, size) — heap-allocated, for dev only.
```
