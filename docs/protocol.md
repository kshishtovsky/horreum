# Horreum Wire Protocol Specification 📡

[English](protocol.md) | [Русский](ru/protocol.md) | [中文](zh/protocol.md)

---

## 1. Frame Layout

Every Horreum request and response is a single frame with the following layout. The header is always 10 bytes, little-endian.

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|          Magic (0x4848)       |   OpCode (1B) |  Flags (1B)  |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|       KeyLength (2 Bytes)     |     ValueLength (4 Bytes)     |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                  ... Key Bytes ... (KeyLength bytes)           |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                 ... Value Bytes ... (ValueLength bytes)        |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

| Field | Size | Description |
|-------|------|-------------|
| Magic | 2 | `0x4848` (`"HH"`). Rejects foreign traffic. |
| OpCode | 1 | Operation code. See §3. |
| Flags | 1 | Reserved. On responses, `flags=0` for OK, `flags=1` for ERROR. |
| KeyLength | 2 LE | Byte length of `Key`. Max 65535. |
| ValueLength | 4 LE | Byte length of `Value`. Max 2^32-1 but capped by `arena.MaxObjectSize = 64 MiB`. |
| Key | N | UTF-8 / binary key bytes. |
| Value | M | UTF-8 / binary value bytes (op-dependent). |

Max frame size: **64 MiB**.

### 1.1 Endianness

All multi-byte fields are **little-endian**. Magic is `0x48 0x48` (low byte first).

### 1.2 Magic Verification

Frames are rejected with `ErrBadMagic` if the first two bytes are not `0x48 0x48`.

---

## 2. Opcode Catalogue

| Code | Name | Direction | Key | Value layout |
|------|------|-----------|-----|--------------|
| 0x01 | GET | Req | yes | empty |
| 0x01 | GET | Resp | yes | payload |
| 0x02 | SET | Req | yes | payload |
| 0x02 | SET | Resp | yes | empty |
| 0x03 | DEL | Req/Resp | yes | empty |
| 0x04 | SETEX | Req | yes | `[ttl:u32 LE][payload]` |
| 0x04 | SETEX | Resp | yes | empty |
| 0x05 | CAS | Req | yes | `[expLen:u32 LE][expected][new]` |
| 0x05 | CAS | Resp | yes | empty / mismatch carries current value |
| 0x06 | INCR | Req | yes | `[delta:u64 LE]` |
| 0x06 | INCR | Resp | yes | `[newValue:u64 LE]` |
| 0x07 | SCAN | Req | yes (prefix) | `[cursor:u64 LE][count:u32 LE]` |
| 0x07 | SCAN | Resp | yes (prefix) | `[nextCursor:u64 LE][n:u32 LE][(keyLen:u16 LE)(key)]×n` |
| 0x08 | DELPREFIX | Req | yes (prefix) | empty |
| 0x08 | DELPREFIX | Resp | yes (prefix) | `[deletedCount:u64 LE]` |
| 0x09 | HSET | Req | yes | `[fieldLen:u16 LE][field][value]` |
| 0x0A | HGET | Req | yes | `[field]` |
| 0x0B | HDEL | Req | yes | `[field]` |
| 0x0C | HGETALL | Req | yes | empty |
| 0x0C | HGETALL | Resp | yes | `[n:u32 LE][(fieldLen:u16 LE)(field)(valueLen:u32 LE)(value)]×n` |
| 0x0D | LPUSH | Req | yes | `[elem]` |
| 0x0E | LPOP | Req/Resp | yes | empty / `[elem]` |
| 0x0F | RPUSH | Req | yes | `[elem]` |
| 0x10 | RPOP | Req/Resp | yes | empty / `[elem]` |
| 0x11 | LLEN | Req/Resp | yes | empty / `[length:u32 LE]` |
| 0x12 | SADD | Req | yes | `[member]` |
| 0x13 | SREM | Req | yes | `[member]` |
| 0x14 | SISMEMBER | Req | yes | `[member]` |
| 0x15 | SMEMBERS | Req/Resp | yes | empty / `[n:u32 LE][(memberLen:u16 LE)(member)]×n` |

Opcodes `0x16..0xFF` are reserved. Unknown → `ErrUnknownOp`.

---

## 3. Detailed Opcode Semantics

### 3.1 GET (0x01)
- Req: empty value.
- Resp: OK with payload OR ERROR (flags=1) for missing/expired.

### 3.2 SET (0x02)
- Req: key + value.
- Resp: OK (stored) or ERROR (too large, arena exhausted).

### 3.3 DEL (0x03)
- Req: key.
- Resp: OK always (idempotent).

### 3.4 SETEX (0x04)
- Req: key + `[ttl seconds][value]`.
- Resp: OK or ERROR. TTL=0 invalid; use SET for infinite TTL.

### 3.5 CAS (0x05)
- Req: key + `[expLen][expected][new]`.
- Resp: OK (swapped) / ERROR (current value) / ERROR (no key).
- Read-then-write.

### 3.6 INCR (0x06)
- Req: key + `[delta:u64 LE]` (signed via bit 63).
- Resp: OK with `[newValue:u64 LE]` or ERROR.

### 3.7 SCAN (0x07)
- Req: prefix + `[cursor:u64 LE][count:u32 LE]`.
- Resp: `[nextCursor:u64 LE][n:u32 LE][...]`. cursor=0 starts from beginning.
- For multi-shard: upper 16 bits = shard index, lower 48 = intra-shard cursor.

### 3.8 DELPREFIX (0x08)
- Req: prefix.
- Resp: OK with `[deletedCount:u64 LE]`. Atomic across shards.

### 3.9..3.12 HSET / HGET / HDEL / HGETALL
Hash layout: `[count:u32 LE][(fieldLen:u16 LE)(field)(valueLen:u32 LE)(value)]×count`.

- HSET: add/update field.
- HGET: zero-copy value slice.
- HDEL: remove field; empty hash → underlying key deleted.
- HGETALL: all fields + values in layout order.

### 3.13..3.17 LPUSH / LPOP / RPUSH / RPOP / LLEN
List layout: `[count:u32 LE][(elemLen:u32 LE)(elem)]×count`.

LPUSH prepends, RPUSH appends. LPOP removes front, RPOP back. Empty list → key deleted.

### 3.18..3.21 SADD / SREM / SISMEMBER / SMEMBERS
Set layout: `[count:u32 LE][(memberLen:u16 LE)(member)]×count`.

- SADD: OK if added, ERROR if already member.
- SREM: OK if removed, ERROR if not member.
- SISMEMBER: OK if member, ERROR otherwise.
- SMEMBERS: all members.

---

## 4. Response Status

| flags | Meaning |
|-------|---------|
| 0x00 | OK |
| 0x01 | ERROR |
| other | reserved |

Request `flags` must be zero. Servers ignore non-zero request flags.

---

## 5. Streaming Behaviour

A connection carries an arbitrary sequence of frames. The server reads as many complete frames as the receive buffer holds and dispatches each in order. Responses are produced in order.

### 5.1 Pipelining

```
Client → Server: GET k1    SET k2 v2    GET k3    DEL k4
Server → Client: OK k1 v1  OK k2       OK k3 v3  OK k4
```

### 5.2 Partial Frames

Incomplete bytes at the buffer end are retained for the next read; clients need not align send boundaries with frame boundaries.

### 5.3 Backpressure

Per-shard worker queue is `4096` jobs (default). If full, the server pauses reading from the connection.

---

## 6. Implementation Notes

### 6.1 Atomicity

A single op is atomic. Composite ops (HGETALL, SMEMBERS) are atomic from the API perspective because they run on a single shard worker. Multi-key patterns (e.g. "increment and read") are NOT atomic across ops.

### 6.2 Connection Pinning

By default, connections are pinned to a shard worker based on `(localAddr, remoteAddr)`. Single client connection touches only one shard's data, simplifying ordering.

### 6.3 Encryption & Compression

Values are stored compressed (LZ4) and/or encrypted (AES-256-GCM) when configured. Wire format does NOT carry compression or encryption metadata. Clients always exchange plain bytes; the server handles transformation at rest transparently.

### 6.4 Time

Server timestamps for TTL are from the host's clock. Clock skew between server and client can cause off-by-one-second expiration. Use NTP.

---

## 7. Examples

### 7.1 SET "user:42" → "Alice"

```
48 48 02 00 08 00 05 00 00 00   <- header (10 bytes)
75 73 65 72 3a 34 32            <- "user:42"
41 6c 69 63 65                  <- "Alice"
```

### 7.2 GET "user:42"

Request:
```
48 48 01 00 08 00 00 00 00 00   <- header (valLen=0)
75 73 65 72 3a 34 32
```

Response (success):
```
48 48 01 00 08 00 05 00 00 00   <- OK
75 73 65 72 3a 34 32
41 6c 69 63 65
```

Response (not found):
```
48 48 01 01 08 00 00 00 00 00   <- flags=1 (ERROR), valLen=0
75 73 65 72 3a 34 32
```

### 7.3 SETEX "session:abc" 60s TTL → "token"

```
48 48 04 00                                        <- SETEX
0b 00                                              <- keyLen=11
09 00 00 00                                        <- valLen=4+5=9
73 65 73 73 69 6f 6e 3a 61 62 63                  <- "session:abc"
3c 00 00 00                                        <- ttl=60 (LE)
74 6f 6b 65 6e                                    <- "token"
```

### 7.4 CAS "user:42" expected "Bob" → "Carol"

```
48 48 05 00
08 00
12 00 00 00                                        <- valLen=12
75 73 65 72 3a 34 32
03 00 00 00                                        <- expLen=3
42 6f 62                                           <- "Bob"
43 61 72 6f 6c                                    <- "Carol"
```

### 7.5 SCAN prefix "user:" cursor=0 count=10

```
48 48 07 00                                        <- SCAN
06 00                                              <- keyLen=6
0c 00 00 00                                        <- valLen=12
75 73 65 72 3a
00 00 00 00 00 00 00 00                            <- cursor=0
0a 00 00 00                                        <- count=10
```

Response:
```
48 48 07 00
06 00
1f 00 00 00
75 73 65 72 3a
00 00 00 00 00 00 00 00                            <- nextCursor=0 (done)
02 00 00 00                                        <- n=2
08 00 75 73 65 72 3a 34 32                        <- "user:42"
08 00 75 73 65 72 3a 34 33                        <- "user:43"
```

---

## 8. Versioning & Compatibility

- Wire protocol is stable across minor versions.
- New opcodes are added at the end of the table.
- Servers reject unknown opcodes via `ErrUnknownOp`.
- Clients treat unknown opcodes as a fatal protocol error.

---

## 9. Reference Implementations

- `examples/client.go` — Go.
- `examples/client.py` — Python.
- `examples/client.js` — Node.js.

These demonstrate how to encode/decode every opcode.
