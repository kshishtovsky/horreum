# Справочник Конфигурации ⚙️

[English](../configuration.md) | [Русский](configuration.md) | [中文](../zh/configuration.md)

---

## Конфигурационный Файл (`config.yaml`)

Подробная спецификация настроек сервиса Horreum:

```yaml
server:
  addr: ":7373"             # Хост и порт для сетевых подключений
  transport: "tcp"          # Сетевой протокол: "tcp" или "quic"
  shards: 4                 # Количество шардов БД в памяти
  region_size: "256MiB"     # Размер mmap региона на один шард
  evict_capacity: 4096      # Емкость S3-FIFO очереди вытеснения
  tls_cert: ""              # Сертификат TLS (для quic)
  tls_key: ""               # Приватный ключ TLS (для quic)
  shutdown_timeout: "30s"   # Тайм-аут плавного завершения работы

storage:
  persistent_path: ""       # Путь к директории хранения на диске (WAL)
  durable: true             # Флаг вызова fsync WAL на каждую запись

metrics:
  addr: ":9090"             # Порт экспортера метрик Prometheus

compression:
  algorithm: "none"         # Алгоритм сжатия: "none" или "lz4"
  min_size: 64              # Минимальный размер данных для сжатия

logging:
  level: "info"             # Уровень логов: debug, info, warn, error
  format: "text"            # Формат логов: text или json
  slow_log_threshold: "10ms" # Порог для slow log замедлений fsync WAL

security:
  encryption_key_path: ""   # Путь к 32-байтному файлу ключа шифрования
```

---

## Переменные Окружения

- `HORREUM_KEY`: 32-байтный бинарный ключ, 64-символьный hex или 44-символьный base64 ключ AES-256. Переопределяет параметр `encryption_key_path`.
