# Wire Protocol Specification 📡

[English](protocol.md) | [Русский](ru/protocol.md) | [中文](zh/protocol.md)

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
3. **Flags (1 Byte)**:
   - Bit 0 (`0x01`): **Error Flag**. Set by the server in responses when key is not found or request failed.
4. **KeyLength (2 Bytes, uint16 LE)**: Length of the key bytes following the header. Maximum key size is 65,535 bytes.
5. **ValueLength (4 Bytes, uint32 LE)**: Length of the value bytes following the key. Maximum value size is 4,294,967,295 bytes.

---

## Operations & Framing

### SET Request & Response

- **Request**: `OpCode = 0x02`, `KeyLength > 0`, `ValueLength >= 0`. Body contains `Key Bytes + Value Bytes`.
- **Response**: `OpCode = 0x02`, `Flags = 0x00` (OK) or `0x01` (Error). Body contains `Key Bytes`.

### GET Request & Response

- **Request**: `OpCode = 0x01`, `KeyLength > 0`, `ValueLength = 0`. Body contains `Key Bytes`.
- **Response**: `OpCode = 0x01`, `Flags = 0x00` (Found) or `0x01` (NotFound/Error). Body contains `Key Bytes + Value Bytes`.

### DEL Request & Response

- **Request**: `OpCode = 0x03`, `KeyLength > 0`, `ValueLength = 0`. Body contains `Key Bytes`.
- **Response**: `OpCode = 0x03`, `Flags = 0x00` (OK). Body contains `Key Bytes`.
