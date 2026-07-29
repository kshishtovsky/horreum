# Configuration Specification ⚙️

[English](configuration.md) | [Русский](ru/configuration.md) | [中文](zh/configuration.md)

---

## Configuration File (`config.yaml`)

Horreum includes a zero-dependency stdlib YAML configuration parser (`internal/config`).

```yaml
server:
  # Host and port to listen for incoming client connections (default ":7373").
  addr: ":7373"

  # Transport layer protocol: "tcp" or "quic" (default "tcp").
  transport: "tcp"

  # Number of in-memory database shards (default 4).
  shards: 4

  # Memory-mapped arena region size per shard (e.g. 256MiB, 1GiB).
  region_size: "256MiB"

  # S3-FIFO eviction capacity (number of objects) per shard.
  evict_capacity: 4096

  # TLS Certificate files (required only if transport is "quic").
  tls_cert: ""
  tls_key: ""

  # Max duration to wait for active connections to drain on shutdown.
  shutdown_timeout: "30s"

storage:
  # Path to persistent storage directory. If empty, runs in anonymous memory mode.
  persistent_path: ""

  # If true, fsyncs the WAL after every write operation (valid in persistent mode).
  durable: true

metrics:
  # Listen address for Prometheus metrics endpoint (host:port/metrics).
  addr: ":9090"

compression:
  # Value compression algorithm: "none" (disabled) or "lz4".
  algorithm: "none"

  # Minimum payload size in bytes to trigger LZ4 compression.
  min_size: 64

logging:
  # Logging level: debug, info, warn, error.
  level: "info"

  # Logging format: text or json.
  format: "text"

  # Operations slower than this threshold will trigger slow log Warn messages.
  slow_log_threshold: "10ms"

security:
  # Path to the 32-byte encryption key file.
  # If empty, a key will be auto-generated at "horreum.key".
  encryption_key_path: ""
```

---

## CLI Flags Reference

Any configuration parameter in `config.yaml` can be overridden via CLI flags:

| Flag | Default | Description |
| :--- | :--- | :--- |
| `--config` | `""` | Path to YAML configuration file |
| `--addr` | `:7373` | Server listen address |
| `--transport` | `tcp` | Transport layer protocol (`tcp` or `quic`) |
| `--shards` | `4` | Number of database shards |
| `--region-size` | `268435456` | Arena region size per shard in bytes |
| `--evict-capacity` | `4096` | S3-FIFO capacity limit per shard |
| `--persistent-path` | `""` | Enable persistent storage mode at this directory |
| `--durable` | `true` | Fsync WAL on every write operation |
| `--metrics-addr` | `:9090` | Prometheus metrics listen address |
| `--compression` | `none` | Value compression algorithm (`none` or `lz4`) |
| `--min-compress-size` | `64` | Minimum payload size to trigger compression |
| `--log-level` | `info` | Logging level (`debug`, `info`, `warn`, `error`) |
| `--log-format` | `text` | Logging format (`text` or `json`) |
| `--slow-log-threshold` | `10ms` | Latency threshold for slow log warnings |
| `--encryption-key-path` | `""` | Path to 32-byte AES encryption key file |
| `--keygen` | `""` | Generate a new 32-byte AES key file at path and exit |
| `--version` | `false` | Print version information and exit |

---

## Environment Variables

- `HORREUM_KEY`: 32-byte raw binary, 64-character hex, or 44-character base64 AES encryption key. Overrides `--encryption-key-path`.
