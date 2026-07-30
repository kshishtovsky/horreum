# Horreum Configuration Reference ⚙️

[English](configuration.md) | [Русский](ru/configuration.md) | [中文](zh/configuration.md)

---

## 1. Loading Order

Configuration values resolve in this order, later overriding earlier:

1. **Built-in defaults** — compiled into the binary.
2. **YAML file** — `--config=path.yaml` (or `-config path.yaml`).
3. **CLI flags** — override YAML.
4. **Environment variables** — `HORREUM_KEY` for encryption.

YAML loads first; flags use YAML-loaded values as defaults.

---

## 2. Built-in Defaults

| Field | Default |
|-------|---------|
| `addr` | `:7373` |
| `transport` | `tcp` |
| `shards` | `4` (server) or `4 × NumCPU` clamped `[4, 64]` |
| `region_size` | `256MiB` |
| `evict_capacity` | `4096` |
| `persistent_path` | `""` |
| `durable` | `true` |
| `metrics_addr` | `:9090` |
| `tls_cert` / `tls_key` | `""` |
| `shutdown_timeout` | `30s` |
| `compression` | `none` |
| `min_size` | `64` |
| `log_level` | `info` |
| `log_format` | `text` |
| `slow_log_threshold` | `10ms` |
| `encryption_key_path` | `""` |

---

## 3. YAML Schema

```yaml
server:
  addr: ":7373"                # listen address
  transport: "tcp"             # "tcp" | "quic"
  shards: 4                    # 0 → 4×NumCPU, clamped 4..64
  region_size: "256MiB"        # per-shard arena region
  evict_capacity: 4096
  tls_cert: ""                 # TLS cert (QUIC)
  tls_key: ""                  # TLS private key (QUIC)
  shutdown_timeout: "30s"

storage:
  persistent_path: ""          # empty = anonymous
  durable: true                # fsync WAL each write

metrics:
  addr: ":9090"

compression:
  algorithm: "none"            # "none" | "lz4"
  min_size: 64

logging:
  level: "info"                # "debug" | "info" | "warn" | "error"
  format: "text"               # "text" | "json"
  slow_log_threshold: "10ms"

security:
  encryption_key_path: ""      # 32-byte AES key file
```

### 3.1 Size Units

| Unit | Multiplier |
|------|------------|
| `B` (or empty) | 1 |
| `K`, `KB` | 1000 |
| `M`, `MB` | 1,000,000 |
| `G`, `GB` | 1,000,000,000 |
| `T`, `TB` | 10^12 |
| `Ki`, `KiB` | 1024 |
| `Mi`, `MiB` | 1024^2 |
| `Gi`, `GiB` | 1024^3 |
| `Ti`, `TiB` | 1024^4 |

### 3.2 Duration Format

`shutdown_timeout`, `slow_log_threshold` accept Go-style durations (`30s`, `1m30s`, `100ms`, `1h`). Uses `time.ParseDuration` internally.

### 3.3 Boolean Values

Case-insensitive: `true|yes|on|1` / `false|no|off|0`.

---

## 4. CLI Flags

### 4.1 Server
```
--addr=":7373"
--transport="tcp"
--shards=4
--region-size=268435456     # 256 MiB
--evict-capacity=4096
--tls-cert=""
--tls-key=""
--shutdown-timeout=30s
```

### 4.2 Storage
```
--persistent-path=""
--durable=true
```

### 4.3 Metrics
```
--metrics-addr=":9090"
```

### 4.4 Compression
```
--compression="none"     # or "lz4"
--min-compress-size=64
```

### 4.5 Logging
```
--log-level="info"
--log-format="text"
--slow-log-threshold=10ms
```

### 4.6 Security
```
--encryption-key-path=""
```

If file is missing, a fresh 32-byte key is auto-generated (0600). Or set `HORREUM_KEY`:

- 64 hex chars (256-bit)
- 44 base64 chars (raw bytes)
- 32 raw chars

`HORREUM_KEY` overrides the file.

### 4.7 Operational
```
--keygen="/path/to/key"   # generate key and exit
--version                 # print and exit
--config=""               # YAML config
```

---

## 5. Environment Variables

| Variable | Effect |
|----------|--------|
| `HORREUM_KEY` | Encryption key override. |

---

## 6. Examples

### 6.1 Minimal TCP, Anonymous

```bash
./horreum --addr=:7373
```

### 6.2 Production QUIC with TLS

```yaml
# /etc/horreum.yaml
server:
  addr: ":7373"
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
  min_size: 64
security:
  encryption_key_path: /etc/horreum.key
logging:
  level: info
  format: json
```

```bash
./horreum --config=/etc/horreum.yaml
```

### 6.3 Environment Override

```bash
export HORREUM_KEY="$(cat /etc/horreum.key)"
./horreum --config=/etc/horreum.yaml
```

---

## 7. Validation

Horreum validates each value at startup and exits with code 1 + a log line if invalid:

| Field | Check |
|-------|-------|
| `region_size` | Parses as size; > 0 |
| `shards` | > 0; ≤ 256 |
| `addr` | Parses as `host:port` |
| `transport` | `tcp` or `quic` |
| `persistent_path` | Directory must exist or be creatable |
| `evict_capacity` | > 0 |
| `slow_log_threshold` | Parses as duration |
| `shutdown_timeout` | Parses as duration |

The TLS cert and key files (if specified) are loaded via `tls.LoadX509KeyPair`.

The encryption key file must be exactly 32 bytes.

---

## 8. Hot Reload

Horreum does **not** reload configuration at runtime. To apply a new configuration:

1. `SIGTERM` the running process (graceful shutdown runs the checkpoint).
2. Restart with new flags / YAML.

---

## 9. Schema Reference (Code-Level)

```go
type ServerConfig struct {
    Addr            string
    Transport       string
    Shards          *int
    RegionSize      string    // e.g. "1GiB"
    EvictCapacity   *uint64
    TLSCert         string
    TLSKey          string
    ShutdownTimeout string    // e.g. "30s"
}

type StorageConfig struct {
    PersistentPath string
    Durable        *bool
}

type MetricsConfig struct {
    Addr string
}

type CompressionConfig struct {
    Algorithm string
    MinSize   *int
}

type LoggingConfig struct {
    Level            string
    Format           string
    SlowLogThreshold string
}

type SecurityConfig struct {
    EncryptionKeyPath string
}

type Config struct {
    Server      ServerConfig
    Storage     StorageConfig
    Metrics     MetricsConfig
    Compression CompressionConfig
    Logging     LoggingConfig
    Security    SecurityConfig
}
```

Pointer types (`*int`, `*uint64`, `*bool`) distinguish "explicitly set" from "use default".

---

## 10. Defaults Tied to Hardware

Two values derive from the runtime environment:

- **`shards`**: `4 × NumCPU`, clamped `[4, 64]`.
- **`SlowLogThreshold`**: `10 ms` (increase on slow disks).

These are the only hardware-dependent knobs.
