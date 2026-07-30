# Horreum Internals — Справочник разработчика 🛠️

[English](../internals.md) | [Русский](internals.md) | [中文](../zh/internals.md)

---

## 1. `internal/arena`

### 1.1 `arena.go`

#### `Handle` (12 байт, без указателей)

```go
type Handle struct {
    Offset uint32
    Size   uint32
    Meta   uint16
    Region uint8
    _      [1]byte
}
```

#### Meta-биты

```go
const (
    QueueTagMask   = uint16(0x3 << 2)
    QueueTagS      = uint16(0x1 << 2)
    QueueTagM      = uint16(0x2 << 2)
    CompressedFlag = uint16(1 << 4)
    EncryptedFlag  = uint16(1 << 5)
)
```

bits 0-1: freq; bits 2-3: queue tag (0/1/2 = none/S/M); bit 4: compressed; bit 5: encrypted.

#### Ошибки

```go
ErrArenaFull    // все регионы исчерпаны
ErrOffsetInvalid // вне границ
ErrSizeTooLarge  // > 64 МиБ
```

#### `region` и `Manager`

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

#### Публичная поверхность

| Функция | Описание |
|---------|----------|
| `NewManager(regionSize, persistent) (*Manager, error)` | Анонимный mmap |
| `OpenFileManager(path, regionSize, create) (*Manager, error)` | MAP_SHARED-файл + Skip префикса |
| `Put(value) (Handle, error)` | bump/freelist + copy |
| `putSlow(value, n)` (внутр.) | Новый регион при нехватке |
| `View(h) ([]byte, error)` | `unsafe.Slice` в mmap |
| `Free(h) error` | Add в freelist; liveObjs−1 |
| `Stats() Stats` | Aggregate |
| `Sync() / SyncAsync()` | MS_SYNC / MS_ASYNC |
| `Close() error` | MS_SYNC + munmap |
| `IncFreq/GetFreq/SetFreq/GetMeta/SetMeta/CASMeta/OrMetaBits/ClearMetaBits(h)` | Атомарные операции на meta |

#### Инварианты горячего пути

1. `Put` lock-free (CAS bump + инкремент).
2. `View` lock-free + zero-alloc.
3. `IncFreq` lock-free (CAS-цикл).
4. `Free` единственная берёт `mu`.

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

`Alloc(n)` — freelist hit или CAS-bump. `Free(offset, size)` — push в freelist. Бамп-only для класса >8192. `Skip(n)` — резерв prefix.

### 1.3 `view.go`

```go
// unsafeView возвращает []byte-view в data с offset, длиной size.
// SAFETY: data — mmap-backed memory, offset+size валидны,
// возвращённый слайс не escape'ит.
func unsafeView(data []byte, offset, size uint32) []byte {
    base := unsafe.Pointer(&data[offset])
    return unsafe.Slice((*byte)(base), size)
}
```

**Используйте `unsafe.Slice` + `unsafe.Add`, никогда `reflect.SliceHeader`.**

### 1.4 `mmap_linux.go`

`//go:build linux` — `unix.Mmap(MAP_ANON|MAP_PRIVATE|MAP_SHARED)`, `munmapRegion` делает MS_SYNC перед munmap (иначе MAP_SHARED не гарантирует flush).

### 1.5 `mmap_other.go`

`//go:build !linux` — fallback `make([]byte, size)`, sync/munmap — no-op.

---

## 2. `internal/arena/persist`

### 2.1 `persist.go`

```go
type PersistentManager struct {
    mgr *arena.Manager
    wal *WAL
    mu  sync.Mutex  // сериализует Put/Delete/Sync
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

`New(dir, opts)` → open arena.dat + WAL + optional ticker.
`Put(key, value, ttl)` → mgr.Put + wal.Append + (durable?) wal.Sync.
`Sync()` → wal.Sync + mgr.Sync.
`Checkpoint(idx)` → snap, write, superblock, rotate WAL.

### 2.2 `superblock.go`

v1, 56 байт:
```
0:8    magic="HORREUM\0"
8:12   version (1)
12:20  regionSize
20:28  indexOffset (4096)
28:36  indexLen
36:44  walOffset (0 после rotate)
44:48  flags (bit 0 = durable)
48:56  checkpointCount
```

`indexCheckpointMaxLen(regionSize)` = `min(regionSize/8, 1 MiB)`. `walMaxLen` = `min(regionSize/4, 64 MiB)`.

### 2.3 `wal.go`

```
SET/SETEX (20/24 байт фикс):
  magic "WAL\0" | op | keyLen | (valLen | hOff | hSize | hReg | [expiresAt]) | key | value
DEL (12 байт фикс):
  magic | op | keyLen | valLen=0 | key
```

```go
type WAL struct {
    mu     sync.Mutex
    file   *os.File
    off    int64
    maxOff int64
    path   string
}
```

- `repairTail()` — walks records, truncates last incomplete.
- `appendRecord()` — header + body.
- `Sync()` — fsync; slow sync → WARN.
- `Rotate()` — Truncate(0).
- `Iterate()/Next()` — streaming reader.

### 2.4 `checkpoint.go`

```
Header (12 B): magic "IDX\0" | version=1 | count
Entry (24 B + keyLen B): hash | keyLen | hOff | hSize | hReg | hMeta | key[0..3] | ...
```

`writeCheckpoint` → encode → `copyToArena(4096)` → sync → superblock → sync. `loadCheckpoint` → `ErrCheckpointBadMagic` если нет магии.

Recovery rules:

| После сбоя… | Что видим | Что делаем |
|------------|------------|-----------|
| До шага 1 | Нет тела | Полный WAL replay |
| После тела, до superblock sync | Новое тело, старый superblock | Нет чекпоинта → replay |
| После superblock sync | Новые superblock+тело, WAL не rotate | Чекпоинт + replay с old walOffset |

### 2.5 `recovery.go`

```go
type IndexWriter interface {
    Put(key []byte, h arena.Handle, expiresAt uint32) bool
    Delete(key []byte) (arena.Handle, bool)
}
```

`ColdStart(dir, regionSize)`: open arena + fresh/validate superblock + open WAL + validate tail. `Replay(idx)` validation pass. `ReplayTo(idx)` → для каждой записи восстанавливает handle и вызывает `idx.Put`/`idx.Delete` (не вызывает `mgr.Put` — payload уже в mmap).

---

## 3. `internal/index`

```go
type Entry struct {
    Hash, KeyLen uint64 / uint16
    Handle arena.Handle (12 B)
    ExpiresAt uint32
}

type HashIndex struct {
    buckets, keys [][]byte
    count, mask, scanCursor
}
```

- `Put` linear probe, load factor 0.75.
- `Get` expires-aware.
- `Delete` Robin Hood backshift.
- `Add` для replay (без growth).
- `Snapshot` список живых entry.
- `DeleteExpired` round-robin cursor.
- `ScanPrefix` from cursor + next cursor.
- `DeletePrefix` все совпадения.
- FNV-1a 64-bit.

---

## 4. `internal/eviction`

S3-FIFO: S 10%, M 90%, Ghost = M.

```go
type Eviction struct {
    mgr *arena.Manager
    mu  sync.Mutex
    sq, mq *RingBuffer
    gh *GhostIndex
}
```

`Add` — ghost-hit → M, иначе S.
`evictS` — freq≥2 промоут в M; иначе ghost + освобождение.
`evictM` — freq≥1 reinsert с freq-1; иначе освобождение.
`Touch(h)` — lock-free; читает queue tag из Meta, сканирует соответствующую очередь, IncFreq.
`Delete/Remove` под `mu`.

`RingBuffer` — power-of-two, atomic head/tail + atomic slot.alive.
`GhostIndex` — open-addressed hash u32 fingerprints (SHA-256 first 4 bytes).

---

## 5. `internal/proto`

10-байтный LE протокол, 21 опкод. `Parser` — stateful frame parser с pending буфером; `Feed(buf)` извлекает все полные фреймы, `Parse()` — один. Не конкурентно-безопасен (один на соединение).

Энкодеры (`EncodeRequest`, `EncodeSetEx`, `EncodeCAS`, `EncodeIncr`, `EncodeScan`, `EncodeDelPrefix`, `EncodeResponse`) переиспользуют `dst` если `cap - len >= need`.

Zero-copy: `Frame.Key`/`Value` — слайсы в `buf`, caller держит буфер живым.

---

## 6. `internal/encrypt`

AES-256-GCM AEAD:
- nonce 12 байт + tag 16 байт (28 байт overhead).
- ~4.9 ГБ/с decrypt на 1 МиБ с AES-NI.
- `Noop` для `none`.
- `LoadKey(path)` — 32 байт.

```go
type Cipher interface {
    Encrypt(dst, src []byte) ([]byte, error)
    Decrypt(dst, src []byte) ([]byte, error)
    Name() string
}
```

---

## 7. `internal/compress`

Pure-Go LZ4 block:
- `MinSize=64`, hash table 64K, окно 64 КиБ.
- Output: `[4 byte size prefix][LZ4 block]`.
- ~2 ГБ/с decompress 1 МиБ.
- 5-class buffer pool.

```go
type Compressor interface {
    Compress(src []byte) ([]byte, bool)
    Decompress(src []byte) ([]byte, error)
    PutBuf(buf []byte)
    Name() string
}
```

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

`Set`: compress → encrypt → mgr.Put → flags → ev.Add → idx.put.
`Get`: idx.get → ev.Touch (lock-free) → mgr.View → decrypt/decompress.
`CAS`/`Incr`: decompress & compare / parse int64.

`shardIndex`: linear-probe FNV-1a; single-threaded.

`ShardSet`:
- `NewShardSet(cfg)`, `NewShardSetFromArena(mgr, n, ...)`.
- `CacheFor(key)` = `shards[hashKey(key) % N]`.
- `PickByAddr(addr)` для connection-pinning.
- `Scan(prefix, cursor, count)` — cursor: upper16=shard, lower48=inner.
- `StartGCLoop(workers, limit)` — background `DeleteExpired`.

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

Accept-loop + per-conn `proto.Parser` + `WorkerPool.Submit`. Ошибки троттлятся `logger.NetErrorLimiter`.

## 11. `internal/transport/quic`

`quic-go` listener, per-stream jobs, `quic.RegisterCertificate`.

## 12. `internal/ds`

Hash: `[Count:4][FieldLen:2][Field][ValLen:4][Val]…`
List: `[Count:4][ElemLen:4][Elem]…`
Set:  `[Count:4][MemberLen:2][Member]…`

## 13. `internal/config`

YAML-парсер stdlib + `ParseSize` (бинарные/десятичные единицы) + `parseBool`.

## 14. `internal/logger`

`RateLimiter` — token bucket. `GlobalLogLevel`, `SlowLogThreshold`, `NetErrorLimiter` (5/s burst 10).

## 15. `internal/metrics`

Zero-alloc Prometheus: `RegisterCounter/Gauge/Histogram`, `IncCounter/IncCounterBy`, `Observe`, `WriteText`. `Recorder` для per-op метрик. Бакеты: `100µs, 500µs, 1ms, 5ms, 10ms, 50ms, 100ms, 500ms, 1s, 5s`.

`MetricsServer` → `/metrics`, `/healthz`.

## 16. `internal/shutdown`

`Coordinator`: регистрация хендлеров, `Run(timeout)` ждёт сигнал и выполняет их в обратном порядке с таймаутом.

## 17. `cmd/horreum`

CLI, bootstrap, генерация ключа через `--keygen`. Полный набор флагов см. в `getting-started.md`.

## 18. `cmd/horreum-bench`

Soak + GET/SET/DEL driver с Pareto-распределёнными размерами. Flags: `--rate`, `--duration`, `--sizedist`, `--read-pct`, `--pipeline`.

## 19. `examples/`

Go/Python/Node.js клиенты, демонстрирующие все 21 опкод с буферным пулингом и pipeline.

---

## A. Сквозные заметки

- `unsafe.Slice` вместо `reflect.SliceHeader` — обязательно.
- Все арена-аллокации 8-байтно выровнены.
- CAS-цикл на ARM для переносимости.
- Размеры: 256 МиБ × 4 = 1 ГиБ для ~1М объектов.
- `--log-level=debug` для verbose tracing.

## B. Порядок чтения

1. `cmd/horreum/main.go`
2. `internal/transport/transport.go`
3. `internal/arena/arena.go` + `allocator.go`
4. `internal/eviction/s3fifo.go`
5. `internal/proto/proto.go`
6. `internal/encrypt/encrypt.go` + `internal/compress/lz4.go`
7. `internal/arena/persist/persist.go`
