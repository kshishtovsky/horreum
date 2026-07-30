# Horreum 协议规范 📡

[English](../protocol.md) | [Русский](../ru/protocol.md) | [中文](protocol.md)

---

## 1. 帧布局

每个 Horreum 请求和响应是单个帧，10 字节小端头部。

```
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
| Magic (0x4848) | OpCode (1B) | Flags (1B)  |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
| KeyLength (2 LE) | ValueLength (4 LE)       |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
| ... Key ...   | ... Value ...              |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

| 字段 | 大小 | 描述 |
|------|------|------|
| Magic | 2 | `0x4848` ("HH") |
| OpCode | 1 | 操作码 |
| Flags | 1 | 响应：0=OK, 1=ERROR |
| KeyLength | 2 LE | 字节长度，最大 65535 |
| ValueLength | 4 LE | 最大 2^32-1，但受 64 MiB 限制 |
| Key | N | 键字节 |
| Value | M | 值字节 |

最大帧大小：**64 MiB**。

### 1.1 字节序

小端。Magic 为 `0x48 0x48`。

### 1.2 Magic 验证

无效 magic → `ErrBadMagic`。

---

## 2. 操作码目录

| 代码 | 名称 | Req→Key | Req Value | Resp Value |
|------|------|---------|-----------|------------|
| 0x01 | GET | 是 | 空 | payload / 空 |
| 0x02 | SET | 是 | payload | 空 |
| 0x03 | DEL | 是 | 空 | 空 |
| 0x04 | SETEX | 是 | `[ttl:4][payload]` | 空 |
| 0x05 | CAS | 是 | `[expLen:4][expected][new]` | 空 / currentValue 不匹配时 |
| 0x06 | INCR | 是 | `[delta:8]` | `[newValue:8]` |
| 0x07 | SCAN | 是(前缀) | `[cursor:8][count:4]` | `[nextCursor:8][n:4][...]` |
| 0x08 | DELPREFIX | 是(前缀) | 空 | `[deletedCount:8]` |
| 0x09 | HSET | 是 | `[fieldLen:2][field][value]` | 空 |
| 0x0A | HGET | 是 | `[field]` | payload / 空 |
| 0x0B | HDEL | 是 | `[field]` | 空 |
| 0x0C | HGETALL | 是 | 空 | `[n:4][...]` |
| 0x0D | LPUSH | 是 | `[elem]` | `[newLength:4]` |
| 0x0E | LPOP | 是 | 空 | `[elem]` |
| 0x0F | RPUSH | 是 | `[elem]` | `[newLength:4]` |
| 0x10 | RPOP | 是 | 空 | `[elem]` |
| 0x11 | LLEN | 是 | 空 | `[length:4]` |
| 0x12 | SADD | 是 | `[member]` | 空 |
| 0x13 | SREM | 是 | `[member]` | 空 |
| 0x14 | SISMEMBER | 是 | `[member]` | 空 |
| 0x15 | SMEMBERS | 是 | 空 | `[n:4][...]` |

`0x16..0xFF` 保留。

---

## 3. 操作码语义

### GET (0x01)
- Req：空 value。
- Resp：OK with payload 或 ERROR（缺失/过期）。

### SET (0x02)
- Req：key + value。
- Resp：OK 或 ERROR。

### DEL (0x03)
- 幂等：始终 OK。

### SETEX (0x04)
- Req：key + `[ttl 秒][value]`。
- `expiresAt = now + ttl`。SETEX 中 TTL=0 无效。

### CAS (0x05)
- Req：key + `[expLen][expected][new]`。
- 先读后写。不匹配时响应携带 current value。

### INCR (0x06)
- Req：key + `[delta:u64]`。delta 通过位 63 有符号。
- 当前值解析为小端 int64。

### SCAN (0x07)
- Req：prefix + `[cursor][count]`。
- Resp：`[nextCursor][n][(keyLen:2)(key)]×n`。cursor=0 为开始。
- 多分片：高 16 位 = shard，低 48 = 内 cursor。

### DELPREFIX (0x08)
- 原子删除所有前缀键，跨所有分片。

### HSET/HGET/HDEL/HGETALL
Hash 布局：`[count:4][(fieldLen:2)(field)(valueLen:4)(value)]×count`。

### LPUSH/LPOP/RPUSH/RPOP/LLEN
List 布局：`[count:4][(elemLen:4)(elem)]×count`。空 list → 键删除。

### SADD/SREM/SISMEMBER/SMEMBERS
Set 布局：`[count:4][(memberLen:2)(member)]×count`。

---

## 4. 响应状态

| flags | 含义 |
|-------|------|
| 0x00 | OK |
| 0x01 | ERROR |

请求中的 flags 必须为零。

---

## 5. 流式行为

### 5.1 流水线

```
客户端 → 服务器：GET k1    SET k2 v2    GET k3    DEL k4
服务器 → 客户端：OK k1 v1  OK k2       OK k3 v3  OK k4
```

### 5.2 部分帧

解析器缓存部分字节用于下次读取。

### 5.3 反压

分片 worker 队列 = 4096 jobs。队列满时服务器暂停读取。

---

## 6. 实现说明

### 6.1 原子性

单个操作原子。复合操作（HGETALL、SMEMBERS）在 API 视角原子。多键模式跨操作不原子。

### 6.2 连接绑定

默认根据 `(localAddr, remoteAddr)` 绑定到分片 worker。

### 6.3 加密和压缩

元数据不通过线格式传递。客户端始终看到纯字节。

### 6.4 时间

TTL 基于主机时钟。使用 NTP 同步。

---

## 7. 示例

### SET "user:42" → "Alice"

```
48 48 02 00 08 00 05 00 00 00   <- 头部
75 73 65 72 3a 34 32
41 6c 69 63 65
```

### GET "user:42"

Req：
```
48 48 01 00 08 00 00 00 00 00
75 73 65 72 3a 34 32
```

Resp OK：
```
48 48 01 00 08 00 05 00 00 00
75 73 65 72 3a 34 32
41 6c 69 63 65
```

Resp ERROR：
```
48 48 01 01 08 00 00 00 00 00
75 73 65 72 3a 34 32
```

### SETEX "session:abc" 60s TTL → "token"

```
48 48 04 00
0b 00
09 00 00 00            <- valLen=9
73 65 73 73 69 6f 6e 3a 61 62 63
3c 00 00 00            <- ttl=60
74 6f 6b 65 6e
```

### CAS "user:42" 期望 "Bob" → "Carol"

```
48 48 05 00
08 00
12 00 00 00            <- valLen=12
75 73 65 72 3a 34 32
03 00 00 00            <- expLen=3
42 6f 62
43 61 72 6f 6c
```

### SCAN prefix "user:" cursor=0 count=10

Req：
```
48 48 07 00
06 00
0c 00 00 00
75 73 65 72 3a
00 00 00 00 00 00 00 00   <- cursor=0
0a 00 00 00              <- count=10
```

Resp：
```
48 48 07 00
06 00
1f 00 00 00
75 73 65 72 3a
00 00 00 00 00 00 00 00   <- nextCursor=0 (done)
02 00 00 00              <- n=2
08 00 75 73 65 72 3a 34 32
08 00 75 73 65 72 3a 34 33
```

---

## 8. 版本控制

协议在次要版本之间稳定。新操作码附加在末尾。未知操作码 → `ErrUnknownOp`。

## 9. 参考实现

`examples/client.{go,py,js}` 演示所有 21 个操作码。
