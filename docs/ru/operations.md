# Операционное руководство Horreum 🛡️

[English](../operations.md) | [Русский](operations.md) | [中文](../zh/operations.md)

---

## 1. Топологии развёртывания

### 1.1 Анонимный режим (stateless)

```
Клиент --TCP/QUIC--> horreum :7373
                       |
                       |-- /metrics (Prometheus)
                       +-- (только память)
```

`--persistent-path=""` (по умолчанию). Потеря данных при рестарте.

### 1.2 Persistent-режим

```
horreum :7373
   |
   +-- /var/lib/horreum/
        |-- arena.dat   <- MAP_SHARED
        +-- wal.log     <- append-only WAL
```

WAL fsync при каждой записи (`--durable=true`). Фоновая MS_ASYNC sync каждую секунду.

### 1.3 HA топология

Horreum не реализует репликацию. Для HA: dual-write на уровне приложения.

### 1.4 Вертикальное масштабирование

```
N шардов × R регионов
N = 4 × NumCPU (clamp 4..64)
R = 256 МиБ..4 ГиБ
```

---

## 2. Определение размеров

### 2.1 Правила

| Ср. размер | Объекты @ 256 МиБ | С 4 × 1 ГиБ |
|-----------|-----|----|
| 64 B | 4 M | 64 M |
| 1 КиБ | 256 K | 4 M |
| 16 КиБ | 16 K | 256 K |
| 256 КиБ | 1 K | 16 K |

`evict_capacity ≈ 1.5× рабочего набора по объектам`.

### 2.2 Математика

```
working_set_bytes ≥ avg_value_size × working_set_objects + ε
arena_total ≥ working_set_bytes × 1.5
shards = arena_total / region_size
evict_capacity = (working_set_objects / shards) × 1.5
```

### 2.3 Память

256 МиБ анонимная арена:
```
data slice: 256 МиБ (mmap'd; VSZ)
meta slice: 32 МиБ (при первом Put)
freelist:   ≤ 1 запись на слот
```

---

## 3. Bootstrap

### 3.1 Свежая установка

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

### 3.2 Persistent + шифрование

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
  level: info
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

## 4. Мониторинг

### 4.1 Алерты Prometheus

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

### 4.2 Дашборды

| Панель | Запрос |
|--------|--------|
| GET p99 | `histogram_quantile(0.99, sum by (le) (rate(horreum_get_latency_seconds_bucket[5m])))` |
| SET p99 | `histogram_quantile(0.99, sum by (le) (rate(horreum_set_latency_seconds_bucket[5m])))` |
| Throughput | `sum(rate(horreum_get_observations_total[1m])) + sum(rate(horreum_set_observations_total[1m]))` |
| Error rate | `rate(horreum_*_observations_total{status="err"}[5m]) / sum without(status) (rate(horreum_*_observations_total[5m]))` |
| Memory | `horreum_memory_used_bytes` |
| Index keys | `horreum_index_keys_total` |
| Evictions | `rate(horreum_evictions_total[5m])` by (queue) |

### 4.3 Профилирование

`net/http/pprof` подключен к metrics HTTP серверу:

```bash
curl http://horreum:9090/debug/pprof/heap > heap.pprof
go tool pprof heap.pprof
```

`/debug/pprof/profile`, `/heap`, `/goroutine`.

---

## 5. Восстановление

### 5.1 Холодный старт

```bash
./horreum --persistent-path=/var/lib/horreum
```

Логи `cold start complete live_keys=N`.

### 5.2 Частичная порча WAL

`repairTail` обрезает оборванную запись при открытии. Логи `wal repair tail truncated N bytes`.

### 5.3 Повреждение суперблока

Плохая магия → считать арену свежей → полный WAL replay.

### 5.4 Усечение arena.dat

`munmap` упадёт на `Close`. Следующий запуск откажется открыть. Бэкапьте `arena.dat` + `wal.log` вместе.

### 5.5 Потеря ключа шифрования

Все зашифрованные значения невосстановимы:

```bash
./horreum --keygen=/etc/horreum.key
rm -rf /var/lib/horreum
./horreum --persistent-path=/var/lib/horreum --encryption-key-path=/etc/horreum.key
```

### 5.6 Ротация ключа

Встроенной нет. Запустить новый процесс, перешифровать через API, вывести старый.

---

## 6. Бэкап

1. SIGTERM (триггер чекпоинт).
2. Скопировать `arena.dat` + `wal.log`.
3. Перезапустить.

```bash
systemctl stop horreum
rsync -a /backup/horreum/ /var/lib/horreum/
chown -R horreum:horreum /var/lib/horreum
systemctl start horreum
```

---

## 7. Тюнинг

### 7.1 Шарды

| Нагрузка | Рекомендация |
|----------|-------------|
| Single-tenant high QPS | 8..16 |
| Multi-tenant | 16..32 |
| Insert-heavy | 32..64 |

### 7.2 Region size

| Ср. размер | Region |
|-----------|--------|
| < 4 КиБ | 256 МиБ |
| 4..64 КиБ | 512 МиБ |
| > 64 КиБ | 1 ГиБ+ |

### 7.3 Compress / Encrypt

| Режим | GET p99 | SET p99 | Диск |
|-------|---------|---------|------|
| plain | 600 нс | 1.2 мкс | полный |
| + lz4 | 1.4 мкс | 6 мкс | 30..60% |
| + aes-gcm | 600 нс | 12 мкс | полный |
| + lz4 + aes-gcm | 1.4 мкс | ~30 мкс | 30..60% |

LZ4 почти всегда стоит того. Шифрование — только если требует модель угроз.

### 7.4 Eviction capacity

```
evict_capacity = (working_set_objects / shards) × 1.5
```

### 7.5 WAL sync

`MS_ASYNC` ticker по умолчанию 1 секунда. Ниже = меньше окно потери, больше системных вызовов.

---

## 8. Операционные паттерны

### 8.1 Graceful shutdown

```
SIGTERM → coord.Run(timeout):
  1. Остановить листенер.
  2. Осушить соединения (timeout-bound).
  3. Persist (sync + checkpoint + close).
  4. Остановить metrics HTTP.
```

### 8.2 Rolling restart

SIGTERM инстансу A, дождаться `bye` лог, рестарт, восстановить трафик.

### 8.3 Увеличение ёмкости

Мигрировать на новый инстанс. Онлайн-изменения размера нет.

### 8.4 Eviction под давлением

Lower `--evict-capacity`, increase `--region-size`, или снизить размер значения (агрессивнее сжатие).

Тяжёлый `S` eviction = одноразовые ключи. Тяжёлый `M` = long-tail.

---

## 9. Incident response

### 9.1 Медленно

1. p99 latency → узкое место.
2. Memory near 100% → eviction голодает.
3. Slow WAL sync → диск медленный.
4. Профиль с `go tool pprof`.

### 9.2 Упал

1. Проверить panic stack.
2. Перезапустить; WAL + checkpoint должны восстановиться.
3. Если `munmap` упал, арена может быть corrupt — бэкап.

### 9.3 Потеря данных

1. `--persistent-path` правильный?
2. WAL существует?
3. `cold start` логи.
4. Если live_keys мало, WAL ротирован перед сбоем — восстановить из чекпоинта.

### 9.4 Не расшифровывается

1. Проверить `--encryption-key-path`.
2. Попробовать `HORREUM_KEY` явно.
3. Если ключ реально изменился — данные невосстановимы.

---

## 10. Известные ограничения

| Ограничение | Workaround |
|-------------|------------|
| Нет online resize | Мигрировать. |
| Нет replication | Dual-write на уровне приложения. |
| Нет key rotation | Перешифровать или wipe. |
| Single-arena persistent | Anonymous для multi-shard persistent. |
| WAL bounded `min(regionSize/4, 64 МиБ)` | Авто-rotate после чекпоинта. |
| Connections не переживают ребаланс | Reconnect. |

---

## 11. Совместимость

- Wire format стабилен в минорных версиях.
- WAL v1, superblock v1, checkpoint v1 — стабильны.
- Миграций не требуется.

---

## 12. SLO

| SLO | Цель |
|-----|------|
| GET p99 (plain) | < 1 мкс |
| SET p99 (plain) | < 5 мкс |
| GET p99 (encrypted hit) | < 10 мкс |
| Восстановление | < 5 с + WAL replay |
| Потеря данных (durable) | 0 |
| Потеря данных (non-durable) | 1 с |
