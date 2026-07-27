---
name: unsafe-safe
description: Safe usage of unsafe in Go — GC invariants, issue #72732, SAFETY comments, escape analysis.
version: "1.0.0"
tags: [go, unsafe, gc, safety]
---

# Unsafe Safety Rules

## GC Invariants

- Go's GC only traces heap pointers.
- `unsafe.Pointer` to mmap'd memory is NOT traced — the memory is outside GC.
- Slices backed by mmap'd memory do NOT cause heap growth.
- The slice header (ptr, len, cap) lives on stack if not captured.

## Issue #72732

`reflect.SliceHeader` and `reflect.StringHeader` are deprecated. Using them causes the compiler to treat the backing pointer as escaping, even when it doesn't.

**Banned:** `reflect.SliceHeader{Data: uintptr(p), Len: n, Cap: n}`

**Required:** `unsafe.Slice((*byte)(p), n)`

## SAFETY Comment Requirement

Every use of `unsafe.Pointer`, `unsafe.Slice`, `unsafe.Add`, `uintptr` arithmetic MUST have a `// SAFETY:` comment explaining:

1. What pointer is being manipulated.
2. Why it's safe (bounds checked, lifetime guaranteed, etc.).
3. Whether the result escapes to heap.

## Escape Analysis Verification

```bash
go build -gcflags="-m -l" ./internal/arena/...
```

Expected: NO "escapes to heap" for Put, View, or the Allocator methods.

## Common Pitfalls

| Pitfall | Fix |
|---|---|
| `&slice[0]` passed to function that may escape | Keep slice local; copy via unsafe.Slice |
| `append()` to mmap'd slice | Use `copy(dst[off:], value)` |
| Storing `*byte` in a struct field | Store offset + region ID |
| `uintptr` cast that loses GC tracking | Use `unsafe.Pointer` intermediary |
| Copying struct with `sync/atomic` fields | Use pointer receiver; never copy |

## Pointer Rules

1. `unsafe.Pointer` to mmap'd memory is safe — no GC tracing needed.
2. `unsafe.Pointer` → `uintptr` → arithmetic → `unsafe.Pointer` is safe IF the uintptr doesn't escape.
3. Never store `uintptr` in a variable across function calls.
