# Horreum 内部 —— 开发者参考 🛠️

[English](../internals.md) | [Русский](../ru/internals.md) | [中文](internals.md)

---

## 1. `internal/arena`

### 1.1 `arena.go`

#### `Handle` (12 字节，无指针)

```go
type Handle struct {
    Offset uint32
    Size   uint32
    Meta   uint16
    Region uint8
    _      [1]byte
}
```

#### Meta 位布局

```go
const (
    QueueTagMask   = uint16(0x3 << 2)
    QueueTagS      = uint16(0x1 << 2)
    QueueTagM      = uint16(0x2 << 2)
    CompressedFlag = uint16(1 << 4)
    EncryptedFlag  = uint16(1 << 5)
)
```

bits 0-1: freq；bits 2-3: queue tag (0/1/2 = none/S/M)；bit 4: compressed；bit 5: encrypted。

#### 哨兵错误

```go
ErrArenaFull     // 所有 region 已耗尽
ErrOffsetInvalid // 句柄越界
ErrSizeTooLarge  // 值 > 64 MiB
```

#### `region` 和 `Manager`

```go
type region struct {
    data  []byte
    alloc Allocator
    meta  []atomic.Int32
}

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

#### 公共接口

| 函数 | 描述 |
|------|------|
| `NewManager(regionSize, persistent) (*Manager, error)` | 匿名 mmap |
| `OpenFileManager(path, regionSize, create) (*Manager, error)` | MAP_SHARED 文件 + 跳过持久化前缀 |
| `Put(value) (Handle, error)` | bump/freelist 分配 + copy |
| `putSlow(value, n)` (内部) | 新 region 回退 |
| `View(h) ([]byte, error)` | `unsafe.Slice` 视图到 mmap |
| `Free(h) error` | 加入 freelist；liveObjs−1 |
| `Stats() Stats` | 聚合 |
| `Sync() / SyncAsync()` | MS_SYNC / MS_ASYNC |
| `Close() error` | MS_SYNC + munmap |
| `IncFreq/GetFreq/SetFreq/GetMeta/SetMeta/CASMeta/OrMetaBits/ClearMetaBits(h)` | meta 上的原子操作 |

#### 热路径不变式

1. `Put` 在快路径上是无锁的（CAS bump + 原子增量）。
2. `View` 无锁且零分配。
3. `IncFreq` 无锁（CAS 循环）。
4. `Free` 是唯一获取 `mu` 的操作。

### 1.2 `allocator.go`

```go
const (
    alignBytes     = 8
    numSizeClasses = 12
)

var sizeClasses = [12]uint32{8, 16, 32, 64, 128, 256, 512, 1024, 2048, 4096, 8192, 0}

type Allocator struct {
    offset atomic.Uint64
    size   uint32
    mu     sync.Mutex
    fl     [12]*freeNode
}
```

`Alloc(n)` - freelist 命中或 CAS-bump。`Free(offset, size)` - 压入 freelist。>8KiB 仅 bump（没有 freelist）。`Skip(n)` - 跳过 prefix。

### 1.3 `view.go`

```go
// SAFETY: data 必须指向 mmap 支持的内存，offset+size 已被调用方验证，
// 返回的切片不会逃逸。
func unsafeView(data []byte, offset, size uint32) []byte {
    base := unsafe.Pointer(&data[offset])
    return unsafe.Slice((*byte)(base), size)
}
```

**使用 `unsafe.Slice` + `unsafe.Add`，永远不要用 `reflect.SliceHeader`。**

### 1.4 `mmap_linux.go`

`//go:build linux`。`unix.Mmap(MAP_ANON|MAP_PRIVATE|MAP_SHARED)`、`MS_SYNC` + `munmap`。

### 1.5 `mmap_other.go`

`//go:build !linux`。fallback 到 `make([]byte, size)`。sync/munmap 是 no-op。

---

## 2. `internal/arena/persist`

### 2.1 `persist.go`

```go
type PersistentManager struct {
    mgr *arena.Manager
    wal *WAL
    mu  sync.Mutex
    ticker *time.Ticker
    stop   chan struct{}
    closed atomic.Bool
}

type Options struct {
    RegionSize   uint64
    Durable      bool
    SyncInterval time.Duration
}
```

`Put`/`Sync`/`Checkpoint` 在 mu 下；WAL 总是同步 fsync（即使在 SyncAsync 上）。

### 2.2 `superblock.go`

v1，56 字节。magic `"HORREUM\0"`，version，regionSize，indexOffset=4096，indexLen，walOffset，flags (bit 0 = durable)，checkpointCount。

### 2.3 `wal.go`

```
SET/SETEX (20/24 固定字节):
  magic "WAL\0" | op | keyLen | (valLen | hOff | hSize | hReg | [expiresAt]) | key | value
DEL (12 固定字节):
  magic | op | keyLen | valLen=0 | key
```

`repairTail` 截断损坏尾部。`Sync` fsync，慢的 → WARN。`Rotate` truncate。`Iterate/Next` 流式读取。

### 2.4 `checkpoint.go`

```
Header (12 B): magic "IDX\0" | version=1 | count
Entry (24 B + keyLen B): hash | keyLen | hOff | hSize | hReg | hMeta | key[0..3] | ...
```

`writeCheckpoint`：encode → copyToArena(4096) → sync → superblock → sync。`loadCheckpoint` 在 magic 缺失时返回 `ErrCheckpointBadMagic`。

恢复规则：

| 崩溃后 | 看到 | 做什么 |
|--------|------|--------|
| 步骤 1 前 | 无 body | 完整 WAL replay |
| body 后、superblock sync 前 | 新 body、旧 superblock | 视为无 checkpoint → replay |
| superblock sync 后 | 新 superblock+body、WAL 未 rotate | checkpoint + 从旧 walOffset 重放 |

### 2.5 `recovery.go`

`IndexWriter` 接口绕过 `arena/persist → index` 的导入循环。

`ColdStart`: open arena + fresh/validate superblock + open WAL + validate。

`ReplayTo` 对每条记录重建 handle，调用 idx.Put/idx.Delete。**不调用 mgr.Put** - 负载已在 mmap。

---

## 3. `internal/index`

冷启动用开放寻址哈希。24 字节 Entry。FNV-1a 64。

`Put` 线性探测，负载因子 0.75 时增长。`Delete` Robin Hood backshift。`DeleteExpired` round-robin。`ScanPrefix`/`DeletePrefix`。

**不是并发安全的。** 分片热路径索引是 transport.shardIndex。

---

## 4. `internal/eviction`

S3-FIFO：S 10%，M 90%，Ghost = M。

```go
type Eviction struct {
    mgr *arena.Manager
    mu  sync.Mutex
    sq, mq *RingBuffer
    gh *GhostIndex
}
```

`Add`：ghost 命中 → M，否则 S。
`evictS`：freq≥2 → promote M；否则 ghost fingerprint + free。
`evictM`：freq≥1 → reinsert freq-1；否则 free。
`Touch(h)` lock-free。
`Delete/Remove` 在 mu 下。

`RingBuffer` - power-of-two，atomic head/tail + atomic slot.alive。
`GhostIndex` - open-addressed u32 fingerprints (SHA-256 first 4 bytes)。

---

## 5. `internal/proto`

10 字节 LE 协议，21 opcodes。`Parser` - stateful frame parser，pending buffer；非并发安全（每连接一个）。

`Frame.Key/Value` 是 buffer 的零拷贝视图。

---

## 6. `internal/encrypt`

AES-256-GCM AEAD：
- 12 字节 nonce + 16 字节 tag（28 字节总开销）。
- ~4.9 GB/s decrypt on 1 MiB with AES-NI。
- `Noop` 用于 none。
- `LoadKey(path)` 要求正好 32 字节。

---

## 7. `internal/compress`

Pure-Go LZ4 block：
- MinSize=64，64K hash table，64KiB 窗口。
- Output: `[4 byte size prefix][LZ4 block]`。
- ~2 GB/s decompress 1 MiB。
- 5-class buffer pool。

---

## 8. `internal/transport`

```go
type shardCache struct {
    mgr    *arena.Manager
    ev     *eviction.Eviction
    idx    *shardIndex
    comp   compress.Compressor
    cipher encrypt.Cipher
}
```

`Set`: compress → encrypt → mgr.Put → flags → ev.Add → idx.put。
`Get`: idx.get → ev.Touch (lock-free) → mgr.View → decrypt/decompress。
`CAS`/`Incr`: 比较 / 解析 int64。

`shardIndex` 线性探测 FNV-1a 单线程。

`ShardSet`：
- `NewShardSet(cfg)`、`NewShardSetFromArena(mgr, n, ...)`。
- `CacheFor(key)` = `shards[hashKey(key) % N]`。
- `PickByAddr(addr)` 用于 TCP 连接绑定。
- `Scan(prefix, cursor, count)` - cursor: upper16=shard, lower48=inner。
- `StartGCLoop(workers, limit)` 后台 `DeleteExpired`。

---

## 9. `internal/transport/api`

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

Accept 循环 + 每连接 `proto.Parser` + `WorkerPool.Submit`。

## 11. `internal/transport/quic`

`quic-go` 监听器 + 每流 jobs，`quic.RegisterCertificate`。

## 12. `internal/ds`

Hash: `[Count:4][FieldLen:2][Field][ValLen:4][Val]…`
List: `[Count:4][ElemLen:4][Elem]…`
Set:  `[Count:4][MemberLen:2][Member]…`

## 13. `internal/config`

stdlib YAML + `ParseSize` (bi/decimal units) + `parseBool`。

## 14. `internal/logger`

`RateLimiter` token bucket。`GlobalLogLevel`、`SlowLogThreshold`、`NetErrorLimiter` (5/s burst 10)。

## 15. `internal/metrics`

零分配 Prometheus。Buckets: `100µs, 500µs, 1ms, 5ms, 10ms, 50ms, 100ms, 500ms, 1s, 5s`。

`/metrics`, `/healthz`。

## 16. `internal/shutdown`

`Coordinator`: 注册 handler，`Run(timeout)` 等待信号并以反向顺序、超时执行。

## 17. `cmd/horreum`

CLI 入口，bootstrap，生成 key via `--keygen`。

## 18. `cmd/horreum-bench`

Soak + GET/SET/DEL driver，Pareto-分布大小。

## 19. `examples/`

Go/Python/Node.js 客户端，所有 21 opcodes + pipeline。

---

## A. 穿透注释

- `unsafe.Slice` 替代 `reflect.SliceHeader`
- 所有 arena 分配 8 字节对齐
- CAS 循环用于 ARM 可移植性
- Sizing：256 MiB × 4 = 1 GiB 用于 ~1M 对象
- `--log-level=debug` 用于详细跟踪

## B. 阅读顺序

1. `cmd/horreum/main.go`
2. `internal/transport/transport.go`
3. `internal/arena/arena.go` + `allocator.go`
4. `internal/eviction/s3fifo.go`
5. `internal/proto/proto.go`
6. `internal/encrypt/encrypt.go` + `internal/compress/lz4.go`
7. `internal/arena/persist/persist.go`
