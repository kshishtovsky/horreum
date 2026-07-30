# Wire Protocol Specification 📡

[English](PROTOCOL.md)

---

## Overview

Horreum uses a zero-copy, binary frame protocol designed for minimum overhead and streaming parsing. Every request and response exchange shares the exact same 10-byte header format.

## Header Structure (10 Bytes Little-Endian)

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|          Magic (0x4848)       |   OpCode (1B) |   Flags (1B)  |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|       KeyLength (2 Bytes)     |     ValueLength (4 Bytes)     |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|     ... Key Bytes ...         |    ... Value Bytes ...        |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

### Header Fields

1. **Magic Code (2 Bytes)**: Must equal `0x4848` (ASCII `'HH'`).
2. **OpCode (1 Byte)**:
   - `0x01` = **GET**
   - `0x02` = **SET**
   - `0x03` = **DEL**
   - `0x04` = **SETEX**
   - `0x05` = **CAS**
   - `0x06` = **INCR**
   - `0x07` = **SCAN**
   - `0x08` = **DELPREFIX**
   - `0x09` = **HSET**
   - `0x0A` = **HGET**
   - `0x0B` = **HDEL**
   - `0x0C` = **HGETALL**
   - `0x0D` = **LPUSH**
   - `0x0E` = **LPOP**
   - `0x0F` = **RPUSH**
   - `0x10` = **RPOP**
   - `0x11` = **LLEN**
   - `0x12` = **SADD**
   - `0x13` = **SREM**
   - `0x14` = **SISMEMBER**
   - `0x15` = **SMEMBERS**
3. **Flags (1 Byte)**:
   - Bit 0 (`0x01`): **Error Flag**. Set by the server in responses when key is not found or request failed.
4. **KeyLength (2 Bytes, uint16 LE)**: Length of the key bytes following the header. Maximum key size is 65,535 bytes.
5. **ValueLength (4 Bytes, uint32 LE)**: Length of the value bytes following the key. Maximum value size is 64 MiB (`arena.MaxObjectSize`).

---

## Operations & Framing

### KV Base Commands
- **GET (0x01)**: Read value by key.
- **SET (0x02)**: Write key and value.
- **DEL (0x03)**: Delete key.
- **SETEX (0x04)**: Write key with TTL (`[TTL:4][Val]`).
- **CAS (0x05)**: Compare-and-Swap (`[ExpLen:4][ExpectedVal][NewVal]`).
- **INCR (0x06)**: Atomic increment (`[Delta:8]`).

### Key Pattern Scanning & Invalidation
- **SCAN (0x07)**: Cursor-based iteration (`[Cursor:8][Count:4]`).
- **DELPREFIX (0x08)**: Batch delete by prefix.

### Hashes (0x09 - 0x0C)
- **HSET (0x09)**: Set hash field (`[FieldLen:2][Field][Val]`).
- **HGET (0x0A)**: Get hash field value (`[Field]`).
- **HDEL (0x0B)**: Delete hash field (`[Field]`).
- **HGETALL (0x0C)**: Get all fields and values.

### Lists / Queues (0x0D - 0x11)
- **LPUSH (0x0D)**: Push element to list head.
- **LPOP (0x0E)**: Pop element from list head.
- **RPUSH (0x0F)**: Push element to list tail.
- **RPOP (0x10)**: Pop element from list tail.
- **LLEN (0x11)**: Get list length.

### Sets (0x12 - 0x15)
- **SADD (0x12)**: Add member to set.
- **SREM (0x13)**: Remove member from set.
- **SISMEMBER (0x14)**: Test set membership.
- **SMEMBERS (0x15)**: Get all set members.
