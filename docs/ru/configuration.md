# Справочник конфигурации Horreum ⚙️

[English](../configuration.md) | [Русский](configuration.md) | [中文](../zh/configuration.md)

---

## 1. Порядок загрузки

1. Встроенные дефолты
2. YAML-файл (`--config=path.yaml`)
3. CLI-флаги
4. Переменные окружения (`HORREUM_KEY`)

YAML загружается первым, флаги его перекрывают.

---

## 2. Встроенные дефолты

| Поле | По умолчанию |
|------|--------------|
| `addr` | `:7373` |
| `transport` | `tcp` |
| `shards` | `4` / `4 × NumCPU` (clamp `[4, 64]`) |
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

## 3. YAML-схема

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

### 3.1 Единицы размера

| Единица | Множитель |
|---------|-----------|
| `B` / пусто | 1 |
| `K`, `KB` | 1000 |
| `M`, `MB` | 1,000,000 |
| `G`, `GB` | 1,000,000,000 |
| `T`, `TB` | 10^12 |
| `Ki`, `KiB` | 1024 |
| `Mi`, `MiB` | 1024^2 |
| `Gi`, `GiB` | 1024^3 |
| `Ti`, `TiB` | 1024^4 |

### 3.2 Длительности

Go-формат: `30s`, `1m30s`, `100ms`, `1h`. `time.ParseDuration`.

### 3.3 Булевы

Регистр-нечувствительно: `true|yes|on|1` / `false|no|off|0`.

---

## 4. CLI-флаги

```
--addr=":7373"
--transport="tcp"
--shards=4
--region-size=268435456
--evict-capacity=4096
--tls-cert=""
--tls-key=""
--shutdown-timeout=30s
--persistent-path=""
--durable=true
--metrics-addr=":9090"
--compression="none"
--min-compress-size=64
--log-level="info"
--log-format="text"
--slow-log-threshold=10ms
--encryption-key-path=""
```

`--keygen="/path"` генерирует ключ и завершается.

`HORREUM_KEY` env: 64 hex, 44 base64, или 32 raw символа.

---

## 5. Примеры

### Минимальный TCP

```bash
./horreum --addr=:7373
```

### Production QUIC + TLS

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

### Env-overrides

```bash
export HORREUM_KEY="$(cat /etc/horreum.key)"
./horreum --config=/etc/horreum.yaml
```

---

## 6. Валидация

Каждое значение проверяется при старте. Невалидно → exit 1.

`region_size > 0`, `shards ∈ (0, 256]`, `addr` парсится как `host:port`, `transport ∈ {tcp, quic}`, ключ шифрования ровно 32 байта.

---

## 7. Горячая перезагрузка

Horreum **не** перезагружает конфигурацию в рантайме. SIGTERM + рестарт.

---

## 8. Справочник по схеме

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

Указатели `*int`, `*uint64`, `*bool` различают "явно установлено" от "по умолчанию".

---

## 9. Привязанные к оборудованию дефолты

`shards = 4 × NumCPU` (clamp `[4, 64]`). `SlowLogThreshold = 10 ms`. Остальное определяется оператором.
