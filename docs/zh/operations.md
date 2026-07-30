# Horreum 操作指南 🛡️

[English](../operations.md) | [Русский](../ru/operations.md) | [中文](../zh/operations.md)

---

## 1. 部署拓扑

### 1.1 匿名模式（无状态缓存）

```
客户端 --TCP/QUIC--> horreum :7373
                       |
                       |-- /metrics (Prometheus)
                       +-- (仅内存)
```

`--persistent-path=""`（默认）。进程重启会丢失所有数据。

### 1.2 持久化模式（持久缓存）

```
horreum :7373
   |
   +-- /var/lib/horreum/
        |-- arena.dat   <- MAP_SHARED
        +-- wal.log     <- append-only WAL
```

每次写入时 WAL fsync（`--durable=true`）。后台 MS_ASYNC ticker 每秒一次。

### 1.3 HA 拓扑

Horreum 不实现复制。HA：应用级双写。

### 1.4 垂直扩展

```
N shards × R region
N = 4 × NumCPU (限制 4..64)
R = 256 MiB..4 GiB
```

---

## 2. 容量规划

### 2.1 容量经验法则

| 平均大小 | 对象 @ 256 MiB | 4 × 1 GiB 总计 |
|-----------|----------------|-------------------|
| 64 B | 4 M | 64 M |
| 1 KiB | 256 K | 4 M |
| 16 KiB | 16 K | 256 K |
| 256 KiB | 1 K | 16 K |

`evict_capacity ≈ 1.5× 工作集的对象数量`。

### 2.2 容量数学

```
working_set_bytes ≥ avg_value_size × working_set_objects + ε
arena_total ≥ working_set_bytes × 1.5
shards = arena_total / region_size
evict_capacity = (working_set_objects / shards) × 1.5
```

### 2.3 内存布局

256 MiB 匿名 arena：
```
data 切片：256 MiB (mmap'd)
meta 切片：32 MiB (在第一次 Put 时)
freelist：≤ 1 个/槽
```

---

## 3. 引导过程

### 3.1 新安装（匿名）

```bash
go build -o /usr/local/bin/horreum ./cmd/horreum

cat > /etc/horreum.yaml <<EOF
server:
  addr: ":7373"
  shards: 8
  region_size: "1GiB"
metrics:
  addr: ":9090"
EOF

./horreum --config=/etc/horreum.yaml
```

### 3.2 新安装（持久化 + 加密）

```bash
./horreum --keygen=/etc/horreum.key
chmod 0600 /etc/horreum.key

openssl req -x509 -newkey rsa:2048 -nodes \
    -keyout /etc/horreum.key -out /etc/horreum.crt \
    -days 365 -subj "/CN=horreum.local"

mkdir -p /var/lib/horreum

cat > /etc/horreum.yaml <<EOF
server:
  transport: quic
  tls_cert: /etc/horreum.crt
  tls_key: /etc/horreum.key
  shards: 4
  region_size: "1GiB"
storage:
  persistent_path: /var/lib/horreum
  durable: true
metrics:
  addr: ":9090"
compression:
  algorithm: lz4
security:
  encryption_key_path: /etc/horreum.key
logging:
  format: json
EOF

./horreum --config=/etc/horreum.yaml
```

### 3.3 Docker

```bash
docker build -t horreum:latest .
docker run -d \
    -p 7373:7373 -p 9090:9090 \
    -v /var/lib/horreum:/var/lib/horreum:rw \
    -e HORREUM_KEY="$(cat /etc/horreum.key)" \
    --name horreum horreum:latest \
    --persistent-path=/var/lib/horreum \
    --encryption-key-path=/etc/horreum.key
```

---

## 4. 监控

### 4.1 推荐的 Prometheus 告警

```yaml
groups:
  - name: horreum
    rules:
      - alert: HorreumSlowReads
        expr: |
          histogram_quantile(0.99,
            sum by (le) (rate(horreum_get_latency_seconds_bucket[5m]))
          ) > 0.005
        for: 5m
        labels: {severity: warning}

      - alert: HorreumErrorRate
        expr: |
          rate(horreum_get_observations_total{status="err"}[5m])
          / ignoring(status)
          sum without(status) (rate(horreum_get_observations_total[5m]))
          > 0.01
        for: 5m
        labels: {severity: critical}

      - alert: HorreumArenaFull
        expr: |
          horreum_memory_used_bytes / (horreum_memory_used_bytes + horreum_memory_free_bytes)
          > 0.9
        for: 10m
        labels: {severity: warning}

      - alert: HorreumSlowWalSync
        expr: rate(horreum_slow_wal_sync_total[5m]) > 0
        for: 10m
        labels: {severity: warning}
```

### 4.2 关键面板

| 面板 | 查询 |
|------|------|
| GET p99 | `histogram_quantile(0.99, sum by (le) (rate(horreum_get_latency_seconds_bucket[5m])))` |
| SET p99 | `histogram_quantile(0.99, sum by (le) (rate(horreum_set_latency_seconds_bucket[5m])))` |
| 吞吐量 | `sum(rate(horreum_get_observations_total[1m])) + sum(rate(horreum_set_observations_total[1m]))` |
| 错误率 | `rate(horreum_*_observations_total{status="err"}[5m]) / sum without(status) (rate(horreum_*_observations_total[5m]))` |
| 内存 | `horreum_memory_used_bytes` |
| 索引键 | `horreum_index_keys_total` |
| 淘汰 | `rate(horreum_evictions_total[5m])` by (queue) |

### 4.3 性能分析

`net/http/pprof` 已连接到指标 HTTP 服务器：

```bash
curl http://horreum:9090/debug/pprof/heap > heap.pprof
go tool pprof heap.pprof
```

目标：`/debug/pprof/profile`、`/heap`、`/goroutine`。

---

## 5. 恢复场景

### 5.1 冷启动

```bash
./horreum --persistent-path=/var/lib/horreum
```

日志 `cold start complete live_keys=N`。

### 5.2 部分 WAL 损坏

`repairTail` 在打开时截断。检查日志 `wal repair tail truncated N bytes`。

### 5.3 超级块损坏

错误的 magic/版本 → 视为新 arena → 完整 WAL 重放。日志 `superblock invalid; full WAL replay expected`。

### 5.4 arena.dat 被截断

`munmap` 在 `Close` 时失败。下次启动拒绝打开。一起备份 `arena.dat` + `wal.log`。

### 5.5 忘记加密密钥

所有加密值不可恢复：

```bash
./horreum --keygen=/etc/horreum.key
rm -rf /var/lib/horreum
./horreum --persistent-path=/var/lib/horreum --encryption-key-path=/etc/horreum.key
```

### 5.6 密钥轮换

无内建。启动新进程，通过 API 重新加密，停用旧进程。

---

## 6. 备份与恢复

1. SIGTERM（触发 checkpoint）。
2. 一起复制 `arena.dat` + `wal.log`。
3. 重启。

```bash
systemctl stop horreum
rsync -a /backup/horreum/ /var/lib/horreum/
chown -R horreum:horreum /var/lib/horreum
systemctl start horreum
```

---

## 7. 容量与性能调优

### 7.1 分片数

| 工作负载 | 推荐 |
|----------|------|
| 单租户高 QPS | 8..16 |
| 多租户 | 16..32 |
| 插入密集 | 32..64 |

### 7.2 Region 大小

| 平均大小 | Region |
|----------|--------|
| < 4 KiB | 256 MiB |
| 4..64 KiB | 512 MiB |
| > 64 KiB | 1 GiB+ |

### 7.3 压缩 / 加密

| 模式 | GET p99 | SET p99 | 磁盘 |
|------|---------|---------|------|
| plain | 600 ns | 1.2 µs | 完整 |
| + lz4 | 1.4 µs | 6 µs | 30..60% |
| + aes-gcm | 600 ns | 12 µs | 完整 |
| + lz4 + aes-gcm | 1.4 µs | ~30 µs | 30..60% |

LZ4 几乎总是值得的。仅在威胁模型需要时加密。

### 7.4 Eviction 容量

```
evict_capacity = (working_set_objects / shards) × 1.5
```

### 7.5 WAL 同步频率

默认 1 秒。较低 = 更小的丢失窗口，更多系统调用。

---

## 8. 操作模式

### 8.1 优雅关闭

```
SIGTERM → coord.Run(timeout):
  1. 停止监听器。
  2. 排空连接（超时限制）。
  3. 持久化（sync + checkpoint + close）。
  4. 停止指标 HTTP。
```

### 8.2 滚动重启

对实例 A 发送 SIGTERM，等待 `bye` 日志，重启，恢复流量。

### 8.3 增加容量

迁移到新实例。无在线调整大小。

### 8.4 容量压力下的淘汰调优

降低 `--evict-capacity`，增加 `--region-size`，或减少值大小。

重 `S` = 一次性键。重 `M` = 长尾重用。

---

## 9. 事件响应

### 9.1 服务慢

1. p99 延迟 → 瓶颈。
2. 内存接近 100% → 淘汰饥饿。
3. WAL 同步慢 → 磁盘慢。
4. 用 `go tool pprof` 分析。

### 9.2 服务崩溃

1. 检查 panic 栈。
2. 重启；WAL + checkpoint 应该恢复。
3. 如果 `munmap` 失败，arena 可能损坏 — 备份。

### 9.3 重启后数据丢失

1. `--persistent-path` 正确？
2. WAL 存在？
3. `cold start` 日志。
4. 如果 live_keys 少，WAL 在崩溃前轮换 — 从 checkpoint 恢复。

### 9.4 无法解密

1. 验证 `--encryption-key-path`。
2. 显式尝试 `HORREUM_KEY`。
3. 如果密钥真的更改 — 数据不可恢复。

---

## 10. 已知限制

| 限制 | 解决方法 |
|------|----------|
| 无在线调整大小 | 迁移。 |
| 无内建复制 | 应用级双写。 |
| 无密钥轮换 | 重新加密或擦除。 |
| 单 arena 持久化 | 匿名用于多 shard 持久化。 |
| WAL bounded `min(regionSize/4, 64 MiB)` | 检查点后自动轮换。 |
| 连接不跨分片重新平衡 | 重连。 |

---

## 11. 兼容性

- 线格式在小版本之间稳定。
- WAL v1、超级块 v1、checkpoint v1 — 稳定。
- 次要版本之间无需迁移。

---

## 12. SLO

| SLO | 目标 |
|-----|------|
| GET p99（plain） | < 1 µs |
| SET p99（plain） | < 5 µs |
| GET p99（加密命中） | < 10 µs |
| 恢复时间 | < 5 s + WAL 重放 |
| 数据丢失（durable） | 0 |
| 数据丢失（非 durable） | 1 s |
