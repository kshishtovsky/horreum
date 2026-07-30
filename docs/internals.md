# Horreum Internals — Developer Reference 🛠️

[English](internals.md) | [Русский](ru/internals.md) | [中文](zh/internals.md)

This document is the deep-dive developer reference for every package in the Horreum codebase.

---

## Table of Contents

1. [`internal/arena`](#1-internalarena)
2. [`internal/arena/persist`](#2-internalarenapersist)
3. [`internal/index`](#3-internalindex)
4. [`internal/eviction`](#4-internaleviction)
5. [`internal/proto`](#5-internalproto)
6. [`internal/encrypt`](#6-internalencrypt)
7. [`internal/compress`](#7-internalcompress)
8. [`internal/transport`](#8-internaltransport)
9. [`internal/transport/api`](#9-internaltransportapi)
10. [`internal/transport/tcp`](#10-internaltransporttcp)
11. [`internal/transport/quic`](#11-internaltransportquic)
12. [`internal/ds`](#12-internalds)
13. [`internal/config`](#13-internalconfig)
14. [`internal/logger`](#14-internallogger)
15. [`internal/metrics`](#15-internalmetrics)
16. [`internal/shutdown`](#16-internalshutdown)
17. [`cmd/horreum`](#17-cmdhorreum)
18. [`cmd/horreum-bench`](#18-cmdhorreum-bench)
19. [`examples/`](#19-examples)

---

## 1. `internal/arena`

The fundamental zero-alloc storage backend. Every cache value lives in `mmap` memory; the hot path never allocates on the Go heap.

### 1.1 `arena.go`

#### `Handle` (12 bytes, pointer-less)

```go
type Handle struct {
    Offset uint32 // 8-byte aligned offset within region
    Size   uint32 // payload length
    Meta   uint16 // frequency (bits 0-1), queue tag (2-3), compressed (4), encrypted (5)
    Region uint8  // region id
    _      [1]byte
}
```

#### Meta Bit Layout

```go
const (
    QueueTagMask   = uint16(0x3 << 2)
    QueueTagNone   = uint16(0x0 << 2)
    QueueTagS      = uint16(0x1 << 2)
    QueueTagM      = uint16(0x2 << 2)
    CompressedFlag = uint16(1 << 4)
    EncryptedFlag  = uint16(1 << 5)
)
```

#### Sentinel Errors

```go
ErrArenaFull    // all regions exhausted; OS denied new mmap
ErrOffsetInvalid // out-of-bounds handle
ErrSizeTooLarge  // value > 64 MiB (MaxObjectSize)
```

#### `region` (single mmap)

```go
type region struct {
    data  []byte             // mmap'd region (immutable after init)
    alloc Allocator          // bump + segregated freelist
    meta  []atomic.Int32     // per-alignBytes slot atomic counters
}
```

#### `Manager`

```go
type Manager struct {
    mu         sync.Mutex
    regions    atomic.Pointer[[]*region]
    current    atomic.Pointer[region]
    currentIdx atomic.Uint32
    size       uint64
    anon       bool
    liveObjs   atomic.Uint64
    filePath   string
}
```

#### Public Surface

| Function | Description |
|----------|-------------|
| `NewManager(regionSize uint64, persistent bool) (*Manager, error)` | Anonymous mmap. |
| `OpenFileManager(path string, regionSize uint64, create bool) (*Manager, error)` | MAP_SHARED file. Reserves superblock + checkpoint prefix. |
| `Put(value []byte) (Handle, error)` | Bump-or-freelist allocate; copy bytes; init meta to 0. |
| `putSlow(value []byte, n uint32)` | Fallback when current region is full. |
| `View(h Handle) ([]byte, error)` | Bounds-check, then `unsafe.Slice` into mmap. |
| `Free(h Handle) error` | Add to freelist; decrement `liveObjs`. |
| `Stats() Stats` | Aggregate per-region stats. |
| `Sync() error` | `MS_SYNC` on every region. |
| `SyncAsync() error` | `MS_ASYNC` for background flushes. |
| `Close() error` | `MS_SYNC` + `munmap` on every region. |
| `IncFreq(h Handle) uint16` | Lock-free CAS loop on `meta[offset/alignBytes]`. |
| `GetFreq/GetMeta/SetFreq/SetMeta/CASMeta(h)` | Atomic read/write helpers. |
| `OrMetaBits / ClearMetaBits` | CAS loops preserving other meta bits. |
| `SetLiveObjects(n uint64)`, `SetUsedBytes(n uint64)` | Reconcile after cold start. |
| `Regions() []*region`, `FilePath() string` | Used by persist layer. |

#### Hot-Path Invariants

1. `Put` is lock-free on the fast path.
2. `View` is lock-free and zero-alloc.
3. `IncFreq` is lock-free (CAS loop).
4. `Free` is the only operation that takes `mu`.

### 1.2 `allocator.go`

#### Size Classes (12)

```go
var sizeClasses = [12]uint32{
    8, 16, 32, 64, 128, 256, 512, 1024, 2048, 4096, 8192, 0,
}
// class 11 (>8192) is bump-only.
```

#### `Allocator`

```go
type Allocator struct {
    offset atomic.Uint64
    size   uint32
    mu     sync.Mutex
    fl     [numSizeClasses]*freeNode
}

type freeNode struct {
    offset uint32
    next   *freeNode   // heap-allocated; only offset lives in mmap
}
```

#### `Alloc(n uint32)` Algorithm

```
n = max(n, 1)
cls = sizeClass(n)
# 1. Try freelist (only concrete classes).
if cls < 11:
    mu.Lock()
    if head := fl[cls]; head != nil:
        fl[cls] = head.next
        mu.Unlock()
        return head.offset, nil
    mu.Unlock()
# 2. Bump alloc.
allocSize = classSize(cls)            # classSize(11) = 0
if cls == 11: allocSize = alignUp(n)
for:
    cur = offset.Load()
    aligned = alignUp(cur)
    newOff = aligned + allocSize
    if newOff > size: return 0, ErrArenaFull
    if offset.CompareAndSwap(cur, newOff): return aligned, nil
```

#### `Free(offset, size)`

```
cls = sizeClass(size)
if cls >= 11: return                # over-size = dead bump space
mu.Lock()
fl[cls] = &freeNode{offset, fl[cls]}
mu.Unlock()
```

### 1.3 `view.go`

The single zero-copy primitive:

```go
// unsafeView returns a []byte view into data at offset, of length size.
//
// SAFETY: data must point to mmap-backed (or otherwise lifetime-stable)
// memory that lives at least until the caller's use of the returned slice.
// offset+size must be valid against data; the caller (Manager.View) enforces
// this. The returned slice does not escape and can be passed through any
// caller that does not retain it.
func unsafeView(data []byte, offset, size uint32) []byte {
    base := unsafe.Pointer(&data[offset])
    return unsafe.Slice((*byte)(base), size)
}
```

### 1.4 `mmap_linux.go`

`//go:build linux`

- `mmapRegion(size)` — `unix.Mmap(-1, 0, size, PROT_READ|PROT_WRITE, MAP_ANON|MAP_PRIVATE)`.
- `mmapFileRegion(path, size, create)` — opens file, `unix.Mmap(fd, 0, size, PROT_READ|PROT_WRITE, MAP_SHARED)`.
- `munmapRegion(data)` — `unix.Msync(data, MS_SYNC)` then `unix.Munmap(data)`.
- `syncRegion(data)` / `syncRegionAsync(data)` — `MS_SYNC` / `MS_ASYNC`.
- `hugepageAlign(data, regionSize)` — for regions ≥ 512 MiB, align to 2 MiB.

### 1.5 `mmap_other.go`

`//go:build !linux`

Fallback to `make([]byte, size)` for Windows / macOS. All `sync*`/`munmap*` are no-ops.

---

## 2. `internal/arena/persist`

Adds cold-start open, WAL append, optional background fsync ticker, and checkpointing on top of `arena.Manager`.

### 2.1 `persist.go`

```go
type PersistentManager struct {
    mgr        *arena.Manager
    wal        *WAL
    dir        string
    regionSize uint64
    durable    bool
    mu         sync.Mutex              // serializes Put/Delete/Sync
    ticker     *time.Ticker
    stop       chan struct{}
    closed     atomic.Bool
}

type Options struct {
    RegionSize   uint64
    Durable      bool
    SyncInterval time.Duration
}
```

#### Public Surface

| Function | Description |
|----------|-------------|
| `New(dir, opts) (*PersistentManager, error)` | Open or create arena.dat + WAL. |
| `Put(key, value, ttl) (Handle, []byte, error)` | Append WAL, optional fsync, return view. |
| `PutEx(key, value, expiresAt)` | Same as Put with absolute expiresAt. |
| `View(handle) ([]byte, error)` | Pass-through. |
| `Delete(key) error` | Append DEL to WAL. |
| `Sync() error` | WAL fsync + arena MS_SYNC. |
| `SyncAsync() error` | WAL fsync + arena MS_ASYNC. |
| `Checkpoint(idx *index.HashIndex) error` | Snapshot index, write body, update superblock, truncate WAL. |
| `Close() error` | Stop ticker, close WAL, close arena. |

WAL is always synchronously fsync'd (even on `SyncAsync`) because async WAL would race against the arena.

### 2.2 `superblock.go`

Layout (v1, 56-byte record):

```
offset  size  field
  0       8   magic = "HORREUM\0"
  8       4   version (1)
 12       8   regionSize
 20       8   indexOffset (always 4096)
 28       8   indexLen
 36       8   walOffset (post-checkpoint; 0 after rotate)
 44       4   flags (bit 0 = durable)
 48       8   checkpointCount
```

Reserved: 4096 bytes total.

Helpers:
- `superblockSize = 4096`
- `magic = "HORREUM\x00"`
- `version = 1`
- `flagDurable = 1 << 0`
- `indexCheckpointMaxLen(regionSize)` = `min(regionSize/8, 1 MiB)`
- `walMaxLen(regionSize)` = `min(regionSize/4, 64 MiB)`

### 2.3 `wal.go`

Header layouts:
```
SET/SETEX (20/24 bytes fixed):
  magic (4) = "WAL\0"
  op     (1)
  keyLen (2)
  --- SET/SETEX only ---
  valLen (4)
  hOff   (4)
  hSize  (4)
  hReg   (1)
  [expiresAt (4)]   // SETEX only
  ...
  key bytes / value bytes

DEL (12 bytes):
  magic "WAL\0" (4) | op (1) | keyLen (2) | valLen=0 (4) | key bytes
```

```go
const (
    walOpSet   uint8 = 1
    walOpDel   uint8 = 2
    walOpSetEx uint8 = 3
)

type WAL struct {
    mu     sync.Mutex
    file   *os.File
    off    int64
    maxOff int64
    path   string
}
```

- `OpenWAL(path)` — opens file, runs `repairTail`.
- `repairTail()` — walks records, truncates last incomplete.
- `AppendSet / AppendSetEx / AppendDel` / `appendRecord()`.
- `Sync()` — `f.Sync()`. Slow syncs emit `slog.Warn("slow WAL sync", dur, path)`.
- `Rotate()` — `f.Truncate(0)` + `f.Seek(0, 0)` + `off = 0`.
- `Iterate() / Next()` — streaming reader; returns `io.EOF` when done.

### 2.4 `checkpoint.go`

Body format:
```
Header (12 bytes):
  magic (4) "IDX\0"
  version (4) u32 = 1
  count    (4) u32

Each entry (24 bytes + keyLen bytes):
  hash (8) u64
  keyLen (2) u16
  hOff  (4) u32
  hSize (4) u32
  hReg  (1) u8
  hMeta (1) u8
  key[0..3] inline (up to 4 bytes, zero-padded)
  ... rest of key bytes
```

- `encodeCheckpoint / decodeCheckpoint`.
- `writeCheckpoint(m, snap, regionSize)` — body, sync, superblock, sync.
- `loadCheckpoint(m, maxSize)` — returns `ErrCheckpointBadMagic` on missing magic.

Recovery rules:

| After a crash... | What we see | What we do |
|------|-----|----|
| Crash before step 1 | No checkpoint body | Full WAL replay. |
| Crash after body, before superblock sync | New body, old superblock | Treat as no checkpoint → full replay. |
| Crash after superblock sync | New superblock, new body, WAL unrotated | Load checkpoint → WAL replay from old walOffset. |

### 2.5 `recovery.go`

```go
type IndexWriter interface {
    Put(key []byte, h arena.Handle, expiresAt uint32) bool
    Delete(key []byte) (arena.Handle, bool)
}
```

`ColdStart(dir, regionSize)`:

1. `os.MkdirAll(dir, 0o755)`.
2. `arenaPath = dir+"/arena.dat"`, `walPath = dir+"/wal.log"`.
3. `mgr, err := arena.OpenFileManager(...)`.
4. Write fresh superblock or validate existing one.
5. Open WAL, run `validateTail`.
6. Close WAL.

`Replay(idx IndexWriter)` — validation only.
`ReplayTo(idx IndexWriter)` — for each record, reconstruct handle, call `idx.Put` / `idx.Delete`. Does **not** call `mgr.Put` — payload is already in mmap.

`copyArenaTo / copyToArena` — get `mgr.Regions()[0].Data()` and copy.

---

## 3. `internal/index`

Open-addressing hash index used by `cmd/horreum` for cold-start. Hot-path shard index is in `internal/transport`, not concurrent-safe.

### 3.1 `index.go`

```go
type Entry struct {
    Hash     uint64
    KeyLen   uint16
    Handle   arena.Handle // 12 bytes
    ExpiresAt uint32
}

type HashIndex struct {
    buckets    []Entry
    keys       [][]byte
    count      int
    mask       uint64
    scanCursor uint64
}
```

#### Public Surface

| Function | Notes |
|----------|-------|
| `New(initialBuckets int) *HashIndex` | Round up to power of two. |
| `NewForCount(n int) *HashIndex` | Sized to `n*2`, min 8. |
| `Put(key, h, expiresAt) (old, replaced bool)` | Linear probe; grows at load factor 0.75. |
| `Get(key, now uint32) (h, ok bool)` | Returns expired as miss. |
| `Delete(key) (h, deleted bool)` | Robin Hood backshift. |
| `Add(key, h, expiresAt) bool` | For replay — never grows. |
| `Snapshot() []SnapshotEntry` | All live entries. |
| `DeleteExpired(now, limit) []Handle` | Round-robin cursor. |
| `ScanPrefix(prefix, cursor, limit, now)` | Up to `limit` keys + next cursor. |
| `DeletePrefix(prefix, now)` | All matching keys. |
| `Count() int` | Live count. |

#### FNV-1a 64-bit (`fnv64`)

```go
const offset = 14695981039346656037
const prime  = 1099511628211
h := offset
for _, b := range key {
    h ^= uint64(b)
    h *= prime
}
return h
```

`deleteAt` uses Robin Hood backshift on delete to keep probe sequences intact.

---

## 4. `internal/eviction`

S3-FIFO — three queues: S (10%), M (90%), Ghost (size of M).

### 4.1 `s3fifo.go`

```go
type Eviction struct {
    mgr *arena.Manager
    mu  sync.Mutex
    sq  *RingBuffer
    mq  *RingBuffer
    gh  *GhostIndex
}
```

Capacity math: `sCap = max(1, capacity * 10 / 100)`, `mCap = max(1, capacity - sCap)`, `gh = NewGhostIndex(mCap)`.

#### Algorithms

**`Add(key, h)`:**
```
if gh.Look(fp(key)):                     # ghost hit → M direct
    if !mq.Push(h): evictM()
    mgr.SetFreq(h, 0); mgr.OrMetaBits(h, QueueTagM)
    return nil
if !sq.Push(h): evictS(key)             # S overflow
mgr.SetFreq(h, 0); mgr.OrMetaBits(h, QueueTagS)
return nil
```

**`evictS(key)`:**
- Pop from S.
- If `freq >= 2`: promote to M (chain-evict if M also full).
- Else: insert fingerprint into ghost, return handle for freeing.

**`evictM()`:**
- Pop from M.
- If `freq >= 1`: re-insert with `freq-1`.
- Else: free handle.

**`Touch(h)` (lock-free):** read `Meta` queue tag, scan matching queue, `mgr.IncFreq(h)`.

**`Delete(key, h)`:** removes from whichever queue holds h; if from S, also removes from ghost.

### 4.2 `queue.go`

```go
type RingBuffer struct {
    cap  uint64
    mask uint64
    head atomic.Uint64
    tail atomic.Uint64
    data []slot
}

type slot struct {
    h     arena.Handle
    next  uint32
    alive atomic.Bool
}
```

Capacity rounded up to power of two. `Push`/`Pop` are atomic-only; `ScanFromHead` walks slots.

### 4.3 `ghost.go`

Open-addressed hash of 4-byte fingerprints:
```go
type bucket struct {
    fp    atomic.Uint32
    alive atomic.Bool
}

func fingerprintOf(key []byte) uint32 {
    h := sha256.Sum256(key)
    return binary.BigEndian.Uint32(h[:4])
}
```

`Look` / `Insert` / `Remove` use linear probing.

---

## 5. `internal/proto`

10-byte LE binary protocol. See `architecture.md` §3 for the full frame layout.

### 5.1 `proto.go`

#### Opcodes (21)

```go
OpGet          OpCode = 1
OpSet          OpCode = 2
OpDel          OpCode = 3
OpSetEx        OpCode = 4
OpCAS          OpCode = 5
OpIncr         OpCode = 6
OpScan         OpCode = 7
OpDelPrefix    OpCode = 8
OpHSet         OpCode = 9
OpHGet         OpCode = 10
OpHDel         OpCode = 11
OpHGetAll      OpCode = 12
OpLPush        OpCode = 13
OpLPop         OpCode = 14
OpRPush        OpCode = 15
OpRPop         OpCode = 16
OpLLen         OpCode = 17
OpSAdd         OpCode = 18
OpSRem         OpCode = 19
OpSIsMember    OpCode = 20
OpSMembers     OpCode = 21
```

#### Errors

```go
ErrShortFrame      // pending buffer < header
ErrBadMagic        // magic != 0x4848
ErrUnknownOp       // unknown op code
ErrFrameTooLarge   // > 64 MiB
ErrTruncatedFrame  // dst cap insufficient
```

#### Encoders (reuse `dst` if `cap - len >= need`)

- `EncodeRequest(dst, op, key, value)`
- `EncodeSetEx(dst, key, value, ttlSeconds)` — `[ttl:4][value]`
- `EncodeCAS(dst, key, expected, new)` — `[expLen:4][expected][new]`
- `EncodeIncr(dst, key, delta int64)` — `[delta:8]`
- `EncodeScan(dst, prefix, cursor uint64, count uint32)` — `[cursor:8][count:4]`
- `EncodeDelPrefix(dst, prefix)`
- `EncodeResponse(dst, op, status uint8, key, value)`

#### Streaming Parser

```go
type Parser struct {
    pending []byte
}
```

- `NewParser()`, `Reset()`, `Pending()`, `Parse()`, `Feed(buf) ([]Frame, error)`.
- `Frame.Key` and `Frame.Value` are zero-copy views into the input buffer.
- Caller MUST keep buffer alive until frame is consumed.
- Parser is **not safe for concurrent use** — one per connection.

---

## 6. `internal/encrypt`

AES-256-GCM AEAD with auto-key management.

### 6.1 `encrypt.go`

```go
type Cipher interface {
    Encrypt(dst, src []byte) ([]byte, error)
    Decrypt(dst, src []byte) ([]byte, error)
    Name() string  // "aes-256-gcm" | "none"
}

type AESGCMCipher struct {
    aead cipher.AEAD
}
```

Wire format: `[12 nonce | ciphertext | 16 tag]` — overhead 28 bytes.

#### Performance

| Payload | Encrypt | Decrypt |
|---------|---------|---------|
| 256 B | 1201 MB/s | 2302 MB/s |
| 4 KiB | 2532 MB/s | 3713 MB/s |
| 64 KiB | 3072 MB/s | 4164 MB/s |
| 1 MiB | **4230 MB/s** | **4930 MB/s** |

`Noop` is pass-through. `LoadKey(path)` requires exactly 32 bytes.

---

## 7. `internal/compress`

Pure-Go LZ4 block codec with buffer pooling.

### 7.1 `compress.go`

```go
type Compressor interface {
    Compress(src []byte) ([]byte, bool)
    Decompress(src []byte) ([]byte, error)
    PutBuf(buf []byte)
    Name() string  // "lz4" | "none"
}
```

`bufPool` — 5 size classes (4 KiB, 16 KiB, 64 KiB, 256 KiB, 1 MiB).

### 7.2 `lz4.go`

Constants: `minMatch=4`, `hashLog=16` (64K hash table), `winSize=64 KiB`.

`Compress(src)` writes `[4 byte size prefix][LZ4 block payload]`. Min size 64 B. Incompressible → `(nil, false)`.

`compressBlock` uses 64K hash table with 64K window.

#### Performance

| Payload | Compress | Decompress |
|---------|----------|------------|
| 256 B | 8.9 MB/s | 1796 MB/s |
| 4 KiB | 131 MB/s | 2051 MB/s |
| 64 KiB | 881 MB/s | 2179 MB/s |
| 1 MiB | **1242 MB/s** | **2052 MB/s** |

---

## 8. `internal/transport`

Shared-nothing shards + shard-set + worker pool.

### 8.1 `transport.go`

```go
type shardCache struct {
    mgr    *arena.Manager
    ev     *eviction.Eviction
    idx    *shardIndex
    comp   compress.Compressor
    cipher encrypt.Cipher
}
```

- `NewShardCache(regionSize, evictCap, comp, cipher)` — anonymous arena.
- `newShardCacheFromArena(mgr, evictCap, comp, cipher)` — for persistent mode.

#### Cache Operations

- `Set(key, value, ttl)` — compress → encrypt → `mgr.Put` → set flags → `ev.Add` → `idx.put`.
- `Get(key)` — `idx.get` → `ev.Touch` (lock-free) → `mgr.View` → decrypt/decompress if flags set.
- `CAS(key, expected, new)` — read-touch-decompress + compare → on match, write new.
- `Incr(key, delta)` — read-touch-decompress + parse LE int64 + add + write.
- `Delete(key)`, `DeleteExpired(limit)`, `Scan(prefix, cursor, count)`, `DelPrefix(prefix)`.

H/L/S operations are built on `Get` + `Set`; per-key locality pins them to one shard worker.

#### `shardIndex` (per-shard, single-threaded)

```go
type shardIndex struct {
    keys      [][]byte
    handles   []arena.Handle
    expiresAt []uint32
    count     int
    mask      uint64
    scanCursor uint64
}
```

Linear-probe FNV-1a. Initial size 64. Grows at load factor 0.75. Robin Hood backshift on delete.

#### `ShardSet`

| Method | Notes |
|--------|-------|
| `NewShardSet(cfg)` | One shard per index. |
| `NewShardSetFromArena(mgr, n, ...)` | All shards share the same arena. |
| `CacheFor(key)` | `shards[hashKey(key) % N]` |
| `CacheIndexFor(key)` | Same. |
| `PickByAddr(addr)` | `hashKey(remoteAddr) % N` for connection-pinning. |
| `Scan(prefix, cursor, count)` | Cursor: upper 16 bits = shard, lower 48 = inner. |
| `DelPrefix(prefix)` | All shards, summed. |
| `StartGCLoop(workers, limit)` | Background `DeleteExpired` ticker. |
| `Close() / Stats()` | Aggregate. |

---

## 9. `internal/transport/api`

Public-facing interfaces:

```go
type Transport interface {
    ListenAndServe() error
    Shutdown(ctx context.Context) error
    Addr() net.Addr
}

type ShardRouter interface {
    CacheFor(key []byte) CacheService
    CacheIndexFor(key []byte) int
    ShardCount() int
    Scan(prefix []byte, cursor uint64, count int) ([][]byte, uint64, error)
    DelPrefix(prefix []byte) (uint64, error)
    StartGCLoop(workers *WorkerPool, limit int)
    Close() error
    Stats() arena.Stats
}

type CacheService interface {
    Set / Get / CAS / Incr / Delete / DeleteExpired / Scan / DelPrefix
    HSet / HGet / HDel / HGetAll
    LPush / LPop / RPush / RPop / LLen
    SAdd / SRem / SIsMember / SMembers
    Close() error
}

type Recorder interface {
    ObserveSet/Get/Del/CAS/Incr/Scan/DelPrefix(status string, dur time.Duration)
}
```

---

## 10. `internal/transport/tcp`

- `net.Listen("tcp", addr)`.
- Per-connection goroutine.
- Each connection reads into a scratch buffer, runs `proto.Parser.Feed`, dispatches each frame via `WorkerPool.Submit`.

`Init()` self-registers `"tcp"` factory.

Errors throttled via `logger.NetErrorLimiter` (5 tokens/sec, burst 10).

---

## 11. `internal/transport/quic`

- `quic.Listen(addr, tlsConfig, quicConfig)`.
- Per-stream jobs.
- `RegisterCertificate(alias, *tls.Certificate)` registers cert globally via `quic.RegisterCertificate`.
- `Init()` self-registers `"quic"` factory.

---

## 12. `internal/ds`

Wire-level encodings for compound data structures. Not concurrent-safe (rely on shard model).

### `hash.go` (192 lines)

Layout: `[Count:u32 LE][FieldLen:u16 LE][Field bytes][ValLen:u32 LE][Val bytes]...`

`HSet`, `HGet` (zero-copy), `HDel`, `HGetAll`.

### `list.go` (113 lines)

Layout: `[Count:u32 LE][ElemLen:u32 LE][Elem bytes]...`

`LPush`, `RPush`, `LPop`, `RPop`, `LLen`.

### `set.go` (129 lines)

Layout: `[Count:u32 LE][MemberLen:u16 LE][Member bytes]...`

`SAdd`, `SRem`, `SIsMember`, `SMembers`.

---

## 13. `internal/config`

```go
type Config struct {
    Server      ServerConfig
    Storage     StorageConfig
    Metrics     MetricsConfig
    Compression CompressionConfig
    Logging     LoggingConfig
    Security    SecurityConfig
}
```

Pointer types (`*int`, `*uint64`, `*bool`) distinguish "explicitly set" from "default".

`ParseYAML(data)` — zero-dependency stdlib YAML parser (subset: nested mappings, no arrays/anchors).

`parseBool(s)` — accepts `true|yes|on|1` and `false|no|off|0`.

`ParseSize(s)` — accepts binary (`K/Ki/KiB`, `M/Mi/MiB`, ...) and decimal (`K/KB`, `M/MB`, ...) units. Decimal point allowed in numeric prefix.

---

## 14. `internal/logger`

```go
var GlobalLogLevel    = slog.LevelInfo
var SlowLogThreshold  = 10 * time.Millisecond
var NetErrorLimiter   = NewRateLimiter(5.0, 10.0)  // 5/s, burst 10
```

`RateLimiter` — token bucket implementation. `Allow() bool`: refill by elapsed time, consume 1 token.

`Init(level, format, slowThreshold)` — configures `slog` default handler.

---

## 15. `internal/metrics`

Zero-allocation Prometheus exposition.

```go
type Label struct { Name, Value string }
type Hash uint64

var registry = &reg{
    counter: make(map[Hash]*counterEntry),
    gauge:   make(map[Hash]*gaugeEntry),
    hist:    make(map[Hash]*histogramEntry),
    mu:      sync.RWMutex{},
}
```

#### Public Surface

| Function | Notes |
|----------|-------|
| `RegisterCounter(name, help, labels) Hash` | Returns registry hash. |
| `IncCounter(hash)`, `IncCounterBy(hash, n)` | Lock-free. |
| `RegisterGauge(...)`, `SetGauge/AddGauge` |  |
| `RegisterHistogram(name, help, labels, bounds) Hash` | Fixed buckets. |
| `Observe(hash, value)` | Histogram observation. |
| `WriteText(w io.Writer)` | Prometheus text exposition. |

`Recorder` interface observes per-op latency.

`DefaultRecorder` — pre-registers `ok`/`err` counters + histograms per op. Buckets (seconds): `100µs, 500µs, 1ms, 5ms, 10ms, 50ms, 100ms, 500ms, 1s, 5s`.

`NoopRecorder` — empty implementations.

`MetricsServer` exposes `/metrics` and `/healthz` with `ReadHeaderTimeout: 5s`.

---

## 16. `internal/shutdown`

```go
type Coordinator struct {
    mu        sync.Mutex
    handlers  []namedHandler
    triggered chan struct{}
}

type namedHandler struct {
    name   string
    handle func(context.Context) error
}
```

- `Register(name, handler)` — append; registration order = execution order.
- `Run(timeout)` — block on signal, then run handlers in reverse order with timeout.
- `Trigger()` — manual trigger (used by `ListenAndServe` returning error).
- `WaitForSignal()` — block on SIGINT/SIGTERM.

---

## 17. `cmd/horreum`

CLI entry point. Single binary, anonymous (default) or persistent mode.

### `main.go` Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `""` | YAML config path. |
| `--addr` | `:7373` | Listen address. |
| `--transport` | `tcp` | `tcp` or `quic`. |
| `--shards` | `4` | Number of shards. |
| `--region-size` | `256MiB` | Arena region per shard. |
| `--evict-capacity` | `4096` | S3-FIFO capacity per shard. |
| `--persistent-path` | `""` | If set, persistent mode. |
| `--durable` | `true` | fsync WAL each write. |
| `--metrics-addr` | `:9090` | Prometheus exporter. |
| `--tls-cert` / `--tls-key` | `""` | TLS cert and key. |
| `--shutdown-timeout` | `30s` | Graceful shutdown bound. |
| `--compression` | `none` | `none` or `lz4`. |
| `--min-compress-size` | `64` | Min bytes to compress. |
| `--log-level` | `info` | `debug\|info\|warn\|error`. |
| `--log-format` | `text` | `text\|json`. |
| `--slow-log-threshold` | `10ms` | WAL fsync slow threshold. |
| `--encryption-key-path` | `""` | 32-byte key file. |
| `--keygen` | `""` | Generate key and exit. |
| `--version` | `false` | Print version and exit. |

#### Bootstrap Helpers

- `parseYAMLPath` — `--config=path`, `--config path`, etc.
- `loadConfig` / `applyParsedYAML` — YAML overrides defaults.
- `loadOrGenerateKeyFile` — auto-generate 32-byte key (0600) if missing.
- `parseEnvKey` — accepts `HORREUM_KEY` env as hex (64), base64 (44), or raw (32).
- `initCompressor` / `initCipher` — wire from flags.
- `initMetrics` — register gauges.
- `initServer` — assemble `transport.Server`.

---

## 18. `cmd/horreum-bench`

Soak + GET/SET/DEL bench driver.

- Generates Pareto-sized requests.
- Reports ops/sec, p50/p95/p99, errors.
- Common flags: `--rate`, `--duration`, `--sizedist=value:min:max`, `--read-pct`, `--pipeline`.

---

## 19. `examples/`

Reference clients in Go, Python, Node.js demonstrating:

- `proto.Encoder` buffer reuse.
- Connection pinning via `(remoteIP + localPort)` → shard.
- All 21 opcodes.
- Synchronous and pipelined modes.

---

## A. Cross-Cutting Notes

### A.1 Why `unsafe.Slice` over `reflect.SliceHeader`?

`reflect.SliceHeader` is deprecated and has lifetime issues. `unsafe.Slice` is the supported escape hatch.

### A.2 Aligned Allocation

All allocations from the arena are 8-byte aligned (`alignBytes = 8`).

### A.3 Atomic Freq Counters

Two-bit saturation in `region.meta[offset/alignBytes]`. CAS loop preserves upper bits. CAS loop on weakly-ordered ARM for portability.

### A.4 Sizing Rules of Thumb

- `--region-size 256MiB --shards 4` → 1 GiB total arena, ample for ~1M small objects.
- `--region-size 1GiB --shards 8` → 8 GiB; production.
- `--region-size 4GiB --shards 16` → 64 GiB; hot-tier.

Each 1 KiB average value → `regionSize / 1 KiB` objects. `evict_capacity` ≈ 1.5× working set in object count.

### A.5 Debugging Tips

- `--log-level=debug` shows per-op latency (verbose).
- `/healthz` always 200; pair with `curl /metrics`.
- `cmd/horreum-bench` can soak against a running server.
- `runtime/pprof` wired via `_ "net/http/pprof"`.

---

## B. Reading Order for New Contributors

1. `cmd/horreum/main.go` — top-down view.
2. `internal/transport/transport.go` — `ShardSet` + `shardCache`.
3. `internal/arena/arena.go` + `internal/arena/allocator.go` — memory model.
4. `internal/eviction/s3fifo.go` — cache replacement.
5. `internal/proto/proto.go` — wire format.
6. `internal/encrypt/encrypt.go` + `internal/compress/lz4.go` — value pipelines.
7. `internal/arena/persist/persist.go` — durability.
