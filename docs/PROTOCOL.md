# Horreum Wire Protocol (v1)

A zero-copy, big-endian, fixed-header binary protocol for Horreum's
SET / GET / DEL / STATS operations. Implemented in `internal/proto`.

## Design Goals

- **Zero-allocation parsing** — 0 `B/op` and 0 `allocs/op` on the hot path.
- **Zero-copy body** — `Frame.Key` and `Frame.Val` slice directly into the
  caller's input buffer in the typical (single-Feed) case.
- **Streaming-friendly** — partial feeds are supported without losing bytes.
- **Authenticated integrity** — header CRC8 catches bit errors in the
  metadata; optional CRC32 over KEY+VALUE catches payload corruption.

## Wire Format

All multi-byte integers are big-endian. The header is exactly **16 bytes**.

### Header (16 bytes, fixed)

| Offset | Bytes | Field      | Description                                       |
|-------:|:-----:|:-----------|:--------------------------------------------------|
|   0..1 |   2   | `MAGIC`    | `0x4852` ("HR")                                   |
|      2 |   1   | `VER`      | Protocol version, `0x01`                          |
|      3 |   1   | `CMD`      | Command byte (see below)                          |
|      4 |   1   | `FLAGS`    | Flag bits (see below)                             |
|   5..6 |   2   | `KEY_LEN`  | `uint16` BE, max **255**                          |
|  7..10 |   4   | `VAL_LEN`  | `uint32` BE, max **arena.MaxObjectSize** (64 MiB) |
| 11..14 |   4   | `RESERVED` | Zero-filled                                        |
|     15 |   1   | `HDR_CRC8` | CRC-8/SMBUS over bytes `0..14`                   |

### Payload (variable)

| Offset          | Field         | Notes                                          |
|----------------:|:--------------|:-----------------------------------------------|
| `16 .. 16+KL`   | `KEY`         | `KL = KEY_LEN`                                 |
| `16+KL .. …`    | `VAL`         | `VL = VAL_LEN`; absent for GET/DEL/STATS       |
| `… + 4`         | `CRC32`       | Only if `FLAGS & FlagCRC32 != 0`. Over KEY+VAL |

Total frame length: `16 + KL + VL + (4 if CRC32 else 0)`.

### Commands (`CMD` byte)

| Value | Name   | Use                       |
|------:|:-------|:--------------------------|
| `0x01`| `GET`  | Read a value by key       |
| `0x02`| `SET`  | Write a key+value         |
| `0x03`| `DEL`  | Delete a key               |
| `0x04`| `STATS`| Server stats (no payload) |

### Flags (`FLAGS` byte)

| Bit | Name         | Meaning                                              |
|----:|:-------------|:-----------------------------------------------------|
|  0  | `FlagCRC32`  | Trailing CRC32 over KEY+VALUE is present             |
|  1  | `FlagResponse`| Frame is a server-to-client response                |
|  2  | `FlagOK`     | Response succeeded (NOK when absent)                 |
| 3-7 | reserved     | Must be zero                                         |

## Zero-Allocation Contract

The hot path (`Feed(buf)` with a fully-formed frame in a single call) must:

1. Allocate **zero** bytes (`B/op == 0`).
2. Allocate **zero** times (`allocs/op == 0`).
3. Slice `Frame.Key` and `Frame.Val` directly out of `buf` — no copy.

When bytes arrive across multiple `Feed` calls (partial feed), the parser
accumulates into an internal `bodyBuf`. The `Frame.Key` and `Frame.Val`
slices then point into `bodyBuf` and remain valid until the next `Feed` or
`Reset` call. After `Reset`, the body buffer's capacity is retained, so
subsequent frames of the same size stay zero-alloc.

## API

```go
import "github.com/horreum/horreum/internal/proto"

// Encoder (no allocation if cap(dst) is sufficient; otherwise ErrDstTooSmall)
out, err := proto.Encode(dst, proto.CmdSET, 0, key, val)
out, err := proto.EncodeResponse(dst, proto.CmdSET, ok /*true*/, true /*crc*/, key, val)

// Streaming parser
p := proto.NewParser()
for {
    n, _ := conn.Read(buf)
    for {
        frame, consumed, err := p.Feed(buf[:n])
        if err != nil { /* protocol error */ p.Reset() }
        if consumed == 0 { break }          // need more bytes
        // process frame.Key, frame.Val
        buf = buf[consumed:]
        n  -= consumed
        if n == 0 { break }
    }
}
```

`Feed` returns:

| State                         | Return                            |
|:------------------------------|:----------------------------------|
| Complete frame in this call   | `(Frame, n>0, nil)`               |
| Need more bytes               | `(Frame{}, 0, nil)`               |
| Protocol error                | `(Frame{}, n, Err*); p is reset`  |

## Errors

| Error            | When                                              |
|:-----------------|:--------------------------------------------------|
| `ErrBadMagic`    | Magic prefix is not `HR`                          |
| `ErrBadVersion`  | `VER` byte is not `0x01`                          |
| `ErrBadCmd`      | `CMD` byte is not one of GET/SET/DEL/STATS        |
| `ErrKeyTooLong`  | `KEY_LEN > 255`                                   |
| `ErrValTooLarge` | `VAL_LEN > arena.MaxObjectSize` (64 MiB)          |
| `ErrHdrCRC`      | Header CRC8 mismatch                              |
| `ErrCRCMismatch` | Trailing CRC32 mismatch (only if FlagCRC32 set)   |
| `ErrDstTooSmall` | `cap(dst)` insufficient for `Encode`              |

All errors are comparable with `errors.Is`.

## Performance Targets

Measured on AMD Ryzen 9 7950X3D, Go 1.25, `internal/proto`:

| Benchmark                       | Throughput    | Allocs/op |
|:--------------------------------|:--------------|:---------:|
| `BenchmarkParseSET1K`           | ~14 M ops/s   |     0     |
| `BenchmarkParseGET`             | ~41 M ops/s   |     0     |
| `BenchmarkEncodeSET1K`          | ~14 M ops/s   |     0     |
| `BenchmarkEncodeResponse1K`     | ~13 M ops/s   |     0     |

## Verification

```bash
# Unit + race
go test -race ./internal/proto/...

# Benchmarks
go test -bench=. -benchmem ./internal/proto/...

# Escape analysis (no "escapes to heap" on hot paths)
go build -gcflags="-m -l" ./internal/proto/... 2>&1 | grep "escapes to heap"

# Fuzz (must not panic)
go test -run='^$' -fuzz=FuzzParseFrame$ -fuzztime=60s ./internal/proto/
```

## Wire-Format Diagram

```
 0           1           2   3   4    5     6     7                11    15   16          16+KL     16+KL+VL
 +-----------+-----------+---+---+---+-----+-----+------------------+------+----+------------+---------+
 |  'H' 'R'  | VER  |CMD|FLG| KEY_LEN |    VAL_LEN              | RSVD  |CRC8|   KEY     |   VAL   |
 +-----------+-----------+---+---+---+-----+-----+------------------+------+----+------------+---------+
 |<------- 16 byte fixed header ------->|                        |       |    |<- variable payload  ->|
```

Last 4 bytes (after VAL) are the optional CRC32 (`KEY`+`VAL`), present only
when `FLAGS & FlagCRC32 != 0`.