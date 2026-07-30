# Horreum 配置参考 ⚙️

[English](../configuration.md) | [Русский](../ru/configuration.md) | [中文](configuration.md)

---

## 1. 加载顺序

1. 内置默认值
2. YAML 文件（`--config=path.yaml`）
3. CLI 标志
4. 环境变量（`HORREUM_KEY`）

YAML 先加载；标志覆盖 YAML。

---

## 2. 内置默认值

| 字段 | 默认 |
|------|------|
| `addr` | `:7373` |
| `transport` | `tcp` |
| `shards` | `4` / `4 × NumCPU`（限制 `[4, 64]`） |
| `region_size` | `256MiB` |
| `evict_capacity` | `4096` |
| `persistent_path` | `""` |
| `durable` | `true` |
| `metrics_addr` | `:9090` |
| `shutdown_timeout` | `30s` |
| `compression` | `none` |
| `log_level` | `info` |
| `log_format` | `text` |
| `slow_log_threshold` | `10ms` |

---

## 3. YAML 模式

```yaml
server:
  addr: ":7373"
  transport: "tcp"
  shards: 4
  region_size: "256MiB"
  evict_capacity: 4096
  tls_cert: ""
  tls_key: ""
  shutdown_timeout: "30s"
storage:
  persistent_path: ""
  durable: true
metrics:
  addr: ":9090"
compression:
  algorithm: "none"
  min_size: 64
logging:
  level: "info"
  format: "text"
  slow_log_threshold: "10ms"
security:
  encryption_key_path: ""
```

### 3.1 大小单位

| 单位 | 倍数 |
|------|------|
| `B` / 空 | 1 |
| `K`, `KB` | 1000 |
| `M`, `MB` | 1,000,000 |
| `G`, `GB` | 1,000,000,000 |
| `T`, `TB` | 10^12 |
| `Ki`, `KiB` | 1024 |
| `Mi`, `MiB` | 1024^2 |
| `Gi`, `GiB` | 1024^3 |
| `Ti`, `TiB` | 1024^4 |

### 3.2 持续时间格式

Go 格式：`30s`, `1m30s`, `100ms`, `1h`。

### 3.3 布尔值

不区分大小写：`true|yes|on|1` / `false|no|off|0`。

---

## 4. CLI 标志

```
--addr=":7373"
--transport="tcp"
--shards=4
--region-size=268435456
--evict-capacity=4096
--persistent-path=""
--durable=true
--metrics-addr=":9090"
--compression="none"
--min-compress-size=64
--log-level="info"
--log-format="text"
--encryption-key-path=""
--keygen="/path"     # 生成密钥并退出
--version
```

`HORREUM_KEY` 环境变量：64 个十六进制字符、44 个 base64 字符或 32 个原始字符。

---

## 5. 示例

### 最小 TCP

```bash
./horreum --addr=:7373
```

### 生产 QUIC + TLS

```yaml
server:
  transport: quic
  tls_cert: /etc/horreum.crt
  tls_key: /etc/horreum.key
  shards: 8
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
```

```bash
./horreum --config=/etc/horreum.yaml
```

### 环境覆盖

```bash
export HORREUM_KEY="$(cat /etc/horreum.key)"
./horreum --config=/etc/horreum.yaml
```

---

## 6. 验证

启动时验证每个值。无效 → 退出码 1。

`region_size > 0`，`shards ∈ (0, 256]`，`addr` 解析为 `host:port`，密钥文件正好 32 字节。

---

## 7. 热重载

Horreum 不支持运行时重新加载配置。SIGTERM + 重启。

---

## 8. 模式参考（代码级）

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

指针类型 `*int`、`*uint64`、`*bool` 区分"显式设置"与"使用默认值"。

---

## 9. 绑定硬件的默认值

`shards = 4 × NumCPU`（限制 `[4, 64]`）。`SlowLogThreshold = 10 ms`。
