# Horreum — Agent Instructions

## Build Commands

```bash
go build ./...                    # compile all
go test ./...                     # run all tests
go test -race ./internal/...      # race detector
go test -bench -benchmem ./internal/arena/...  # arena benchmarks
go test -bench -benchmem ./internal/index/...  # index benchmarks
go vet ./...                      # static analysis
go test -fuzz=FuzzArenaPut ./internal/arena/ -fuzztime=60s  # fuzz
```

## Makefile Targets

```bash
make test    # go test -race ./internal/...
make bench   # go test -bench -benchmem ./internal/...
make soak    # go run ./cmd/horreum-bench/ --soak ...
make vet     # go vet ./...
make fuzz    # go test -fuzz=FuzzArenaPut ./internal/arena/ -fuzztime=60s
make clean   # rm -f bench/phase1.txt
```

## Code Style

- Go 1.22+, no CGO, static binary.
- Only stdlib + `golang.org/x/sys/unix`.
- Every exported function gets a godoc comment.
- Every `unsafe` usage gets a `// SAFETY:` comment.
- No `interface{}`/`any` on hot paths.
- No `reflect.SliceHeader` — use `unsafe.Slice` + `unsafe.Add`.
- No `append()` to mmapped slices — use `copy(dst[off:], value)`.
- Commit format: `[task01][groupX] <imperative summary>`

## Skills

Before coding, load required skills via `@skill-name`. Skill files live at `.agents/skills/<name>/SKILL.md`.

Task 01 required skills:
- `@go-zero-copy` — unsafe.Slice/unsafe.Add over mmap'd memory
- `@arena-allocator` — bump + segregated freelist, size classes, alignment
- `@mmap-linux` — unix.Mmap/Munmap/Msync/Madvise
- `@unsafe-safe` — GC invariants, SAFETY-comment requirement
- `@bench-driven` — go test -bench -benchmem, benchstat, p99
- `@go-test-harness` — testing/quick, fuzz, -race, soak runner, table-driven
