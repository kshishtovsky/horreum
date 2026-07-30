# Horreum — Agent Instructions

> Ultra-fast, zero-allocation, authenticated in-memory & persistent cache service in pure Go.

## Project Snapshot

| | |
|---|---|
| Module | `github.com/horreum/horreum` |
| Go | **1.22+** (tested on 1.25; `go.mod` declares `go 1.25.0`) |
| CGO | **Disabled** — pure-Go static binary |
| Stdlib externals | `golang.org/x/sys/unix` (mmap/msync), `crypto/aes`, `crypto/cipher` |
| Optional | `quic-go v0.61.0` (only when `--transport=quic`) |
| Default ports | `7373` (cache), `9090` (Prometheus `/metrics`, `/healthz`) |
| Default shards | `4 × NumCPU`, clamped to `[4, 64]` |
| Default arena region | `256 MiB` per shard |
| Default evict capacity | `4096` objects per shard |
| Default frame size | `64 MiB` (`arena.MaxObjectSize`) |
| Wire format | 10-byte LE header, magic `0x4848` (`"HH"`), 21 opcodes |
| Layout | `cmd/horreum`, `cmd/horreum-bench`, `internal/{arena,arena/persist,compress,config,ds,encrypt,eviction,index,logger,metrics,proto,shutdown,transport,transport/{api,tcp,quic}}`, `examples/` |

---

## Build & Test

### Commands

```bash
go build ./...                                # compile everything
go test ./...                                 # all unit tests
go test -race ./internal/...                  # race detector (required before commit)
go test -bench=. -benchmem ./internal/arena/...   # arena micro-benchmarks
go test -bench=. -benchmem ./internal/index/...   # index micro-benchmarks
go test -fuzz=FuzzArenaPut ./internal/arena/ -fuzztime=60s   # fuzz arena allocator
go vet ./...                                  # static analysis
```

### Makefile Targets

| Target  | Command |
|---------|---------|
| `make test`   | `go test -race ./internal/...` |
| `make bench`  | `go test -bench=. -benchmem ./internal/arena/...` then `.../index/...` |
| `make soak`   | `go run ./cmd/horreum-bench/ --soak --rate=200000 --sizedist=pareto:4096:16384 --duration=1000000` |
| `make vet`    | `go vet ./...` |
| `make fuzz`   | `go test -fuzz=FuzzArenaPut ./internal/arena/ -fuzztime=60s` |
| `make clean`  | `rm -f bench/phase1.txt` |

---

## Hard Constraints (Non-Negotiable)

These rules come from the architecture itself. Violating any of them silently breaks performance or correctness.

### 1. Zero-Allocation Hot Path

Cache values live in `mmap`'d regions accessed via `unsafe.Slice`. **Do not** introduce `make([]byte, n)`, `append`, `bytes.Clone`, or any Go-heap allocation on the read/write hot paths.

### 2. `unsafe` Discipline

- Every `unsafe.*` usage **must** carry a `// SAFETY:` comment explaining why it is sound, the lifetime guarantee on the backing memory, and bounds preconditions enforced by the caller.
- Use **`unsafe.Slice` + `unsafe.Add`** — never `reflect.SliceHeader`.

### 3. No `append()` on `mmap`'d Slices

`mmap`'d regions must **not** be reallocated. Always: `copy(reg.data[offset:offset+n], value)`.

### 4. No `interface{}` / `any` on Hot Paths

Use concrete types or generics.

### 5. Stdlib + `golang.org/x/sys` Only

Only `quic-go` is allowed for QUIC transport.

### 6. Godoc on Every Exported Symbol

Every exported function, method, type, and constant needs a `// Foo does X.` doc comment.

### 7. TDD: RED → GREEN → REFACTOR

Tests first. Use table-driven tests, `testing/quick`, fuzz, `-race`, and `cmd/horreum-bench` for soak.

### 8. Concurrency Model = Shared-Nothing Shards

Each shard's data is touched by exactly one goroutine — the shard worker. No per-shard locks.

### 9. Commit Format

```
[taskXX][groupY] <imperative summary>
```

Examples: `[task03][group2] add S3-FIFO ghost index`, `[task11][group1] implement AES-256-GCM encrypt`.

---

## Required Skills

Load these via `@skill-name` (files in `.agents/skills/<name>/SKILL.md`) before touching the corresponding subsystem:

| Skill | When to load |
|-------|--------------|
| `@go-zero-copy` | Any new `unsafe.Slice` / `unsafe.Add` / mmap-backed view |
| `@arena-allocator` | Touching `internal/arena`, size classes, freelists |
| `@mmap-linux` | `unix.Mmap`, `unix.Munmap`, `unix.Msync`, `unix.Madvise` |
| `@unsafe-safe` | Reviewing GC invariants; every `unsafe` block |
| `@bench-driven` | Adding benchmarks; interpreting `benchstat` output |
| `@go-test-harness` | Writing table-driven tests, fuzz tests, `-race` cases |

---

## Architecture Cheat Sheet

```
                                    +---------------------------+
                                    |     client (TCP/QUIC)    |
                                    +-------------+-------------+
                                                  |
                                       10B header + payload
                                                  v
                                    +---------------------------+
                                    |   proto.Parser (per conn) |
                                    +-------------+-------------+
                                                  |
                                                  v  shardIdx = fnv64(key) % NumShards
                                    +---------------------------+
                                    |     WorkerPool.Submit     |
                                    +-------------+-------------+
                                                  |
                                                  v  (single goroutine per shard)
                  +-----------+   +-------------+-------------+   +-----------+
                  | shard 0   |   | shard 1   ...               |   | shard N-1 |
                  |  cache    |   |  cache                      |   |  cache    |
                  |  + arena  |   |  + arena                    |   |  + arena  |
                  |  + evict  |   |  + evict                    |   |  + evict  |
                  +-----------+   +-----------------------------+   +-----------+
```

---

## Subsystem Owners

| Package | Responsibility | Hot-path notes |
|---------|----------------|----------------|
| `internal/arena` | mmap regions, bump alloc, freelists, atomic meta | Lock-free bump via CAS; mutex only on freelist |
| `internal/arena/persist` | WAL, superblock, checkpoint, cold-start replay | Single mutex on WAL; `MS_ASYNC` ticker for arena |
| `internal/index` | Cold-start key→handle index | Single-threaded |
| `internal/eviction` | S3-FIFO queues + ghost index | `Touch` is lock-free; `Add`/`Delete` under mutex |
| `internal/proto` | 10-byte LE wire frame parser | Zero-copy `Frame.Key/Value` views into buffer |
| `internal/encrypt` | AES-256-GCM AEAD | Hardware-accelerated via AES-NI |
| `internal/compress` | Pure-Go LZ4 + buffer pool | ~2 GB/s decompress |
| `internal/transport` | `shardCache`, `shardIndex`, `ShardSet`, `WorkerPool` | Per-shard single-goroutine model |
| `internal/transport/api` | Public `Transport`, `CacheService`, `ShardRouter` | Plain interfaces, no allocations |
| `internal/transport/tcp` | TCP accept loop + per-conn parser | No allocations after warm-up |
| `internal/transport/quic` | QUIC listener + stream handler | Cert registered via `quic.RegisterCertificate` |
| `internal/ds` | Hash/list/set wire encodings | None — pure byte-level |
| `internal/config` | Stdlib YAML parser, binary size units | O(N) at startup only |
| `internal/logger` | `slog` setup + token-bucket rate limiter | Slow-log threshold for WAL fsync |
| `internal/metrics` | Zero-alloc Prometheus exporter | Atomic counters, fixed buckets |
| `internal/shutdown` | Graceful shutdown coordinator | Sequence: stop listener → drain → close |
| `cmd/horreum` | CLI entry point + main wiring | Single binary, all init code |
| `cmd/horreum-bench` | Soak + GET/SET/DEL benchmark driver | Generates Pareto-sized traffic |
| `examples/` | Reference clients in Go/Python/Node.js | All 21 opcodes |

---

## Style Guide

- **Naming**: short, idiomatic Go. `mgr`, `idx`, `ev`, `h`, `fr`, `sb` are fine for hot-path locals.
- **Errors**: wrap with `%w`, never with `%v` when callers might need `errors.Is/As`.
- **Comments**: prefer godoc on exported symbols; inline `// why` is fine, **no narration of what**.
- **Imports**: stdlib first, then third-party, then internal. Group with blank lines.
- **No `init()` side-effects** outside CLI flags package.
- **No package-level mutable state** except in `internal/metrics` and `internal/logger`.

---

## Adding a New Feature

1. Read the architecture docs (`docs/architecture.md` + this file).
2. Load required skills via `@skill-name`.
3. Write failing tests (RED). For concurrency, use `-race`.
4. Implement minimal passing code (GREEN).
5. Add benchmarks if the change touches a hot path.
6. Run `make test`, `make vet`, `make bench`.
7. Commit with the required format.

---

## Adding a New Opcode

1. Add to `internal/proto/proto.go` (constants, `opName`, `OpCode`).
2. Add encoders if the wire format differs from `EncodeRequest`.
3. Add handler in `internal/transport/handler.go`.
4. Add `CacheService` method in `internal/transport/api`.
5. Implement on `shardCache` in `internal/transport/transport.go`.
6. Update tests in `internal/proto/proto_test.go` and `internal/transport/transport_test.go`.
7. Add `metrics.Recorder.ObserveX` observation in the handler.
8. Document the opcode in `docs/protocol.md` (and `ru/`, `zh/`).

---

## Documentation

Three parallel docs trees:

- `docs/` — English (source of truth).
- `docs/ru/` — Russian translation.
- `docs/zh/` — Chinese translation.

Files: `architecture.md`, `internals.md`, `protocol.md`, `configuration.md`, `getting-started.md`, `operations.md`.

When changing code, update all three language trees.

---

## PR Checklist

- [ ] Tests added (table-driven / fuzz / -race as appropriate).
- [ ] Benchmarks added if hot path touched.
- [ ] `make test` passes (with `-race`).
- [ ] `make vet` passes.
- [ ] Godoc on every new exported symbol.
- [ ] `// SAFETY:` on every new `unsafe.*`.
- [ ] No new top-level dependencies.
- [ ] No `interface{}` / `any` on hot paths.
- [ ] Commit message matches `[taskXX][groupY] <imperative summary>`.
- [ ] Docs updated in `docs/`, `docs/ru/`, `docs/zh/`.

---

## License

See `LICENSE`. The project is provided as-is; no warranty.
