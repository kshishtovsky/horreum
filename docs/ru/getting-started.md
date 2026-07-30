# Начало работы с Horreum 🚀

[English](../getting-started.md) | [Русский](getting-started.md) | [中文](../zh/getting-started.md)

---

## 1. Предварительные требования

- Go 1.22+ (тестировано на 1.25)
- GCC не нужен (CGO-free)
- Linux рекомендуется для mmap persistence
- ~64 МиБ RAM на шард; 256 МиБ по умолчанию

Для QUIC нужны TLS-сертификаты.

---

## 2. Сборка

### Из исходников

```bash
git clone https://github.com/kshishtovsky/horreum.git
cd horreum
go build -o horreum ./cmd/horreum
go build -o horreum-bench ./cmd/horreum-bench
```

### Из Docker

```bash
docker build -t horreum:latest .
```

---

## 3. Локальный запуск (анонимный режим)

```bash
./horreum --addr=:7373
```

```
INFO ready; waiting for signal
INFO horreum listening addr=:7373 transport=tcp
INFO metrics listening url=http://:9090/metrics
```

### Smoke test

```bash
( printf '\x48\x48\x02\x00\x06\x00\x05\x00\x00\x00'; printf 'hello'; printf 'world' ) | nc -q1 localhost 7373 | xxd
( printf '\x48\x48\x01\x00\x06\x00\x00\x00\x00\x00'; printf 'hello' ) | nc -q1 localhost 7373 | xxd
```

---

## 4. Запуск с персистом

```bash
mkdir -p /var/lib/horreum
./horreum --persistent-path=/var/lib/horreum
```

### Проверка

```bash
./horreum --persistent-path=/var/lib/horreum &
PID=$!
./examples/client set mykey "data"
kill $PID
./horreum --persistent-path=/var/lib/horreum &
./examples/client get mykey
```

### WAL

```bash
xxd /var/lib/horreum/wal.log | head -50
```

---

## 5. Шифрование

```bash
./horreum --keygen=/etc/horreum.key
chmod 0600 /etc/horreum.key
./horreum --persistent-path=/var/lib/horreum --encryption-key-path=/etc/horreum.key
```

---

## 6. Сжатие

```bash
./horreum --persistent-path=/var/lib/horreum --compression=lz4 --min-compress-size=64
```

---

## 7. QUIC с TLS

```bash
openssl req -x509 -newkey rsa:2048 -nodes \
    -keyout /tmp/key.pem -out /tmp/cert.pem \
    -days 365 -subj "/CN=localhost"

./horreum --transport=quic --tls-cert=/tmp/cert.pem --tls-key=/tmp/key.pem --addr=:7373
```

---

## 8. YAML-конфиг

`/etc/horreum.yaml`:

```yaml
server:
  addr: ":7373"
  transport: tcp
  shards: 8
  region_size: "1GiB"
  evict_capacity: 4096
storage:
  persistent_path: "/var/lib/horreum"
  durable: true
metrics:
  addr: ":9090"
compression:
  algorithm: lz4
  min_size: 64
logging:
  level: info
  format: json
  slow_log_threshold: 10ms
security:
  encryption_key_path: "/etc/horreum.key"
```

```bash
./horreum --config=/etc/horreum.yaml
```

CLI перекрывают YAML:

```bash
./horreum --config=/etc/horreum.yaml --transport=quic --shards=16
```

---

## 9. Референтные клиенты

### Go

```go
package main

import (
    "fmt"
    "github.com/kshishtovsky/horreum/examples/client"
)

func main() {
    h, err := client.Dial("tcp", "localhost:7373")
    if err != nil { panic(err) }
    defer h.Close()

    h.Set("greeting", []byte("hello world"), 0)
    val, _ := h.Get("greeting")
    fmt.Printf("greeting = %q\n", string(val))
}
```

### Python

```python
from examples.client import HorreumClient
client = HorreumClient("127.0.0.1", 7373)
client.connect()
client.set("user:1001", b"Alice")
val = client.get("user:1001")
print(f"User: {val.decode()}")
client.close()
```

### Node.js

```javascript
const Horreum = require("./examples/client.js");
(async () => {
    const client = new Horreum("127.0.0.1", 7373);
    await client.connect();
    await client.set("user:1001", "Alice");
    const val = await client.get("user:1001");
    console.log("User:", val.toString());
    await client.close();
})();
```

---

## 10. Бенчмаркинг

```bash
./horreum-bench --rate=100000 --duration=1000000
./horreum-bench --soak --rate=200000 --sizedist=pareto:4096:16384 --duration=1000000
```

```
op        rate    p50    p95    p99    errors
GET    102367    432ns  812ns  1.2µs  0
SET    100113    1.4µs  2.1µs  3.7µs  0
```

---

## 11. Наблюдаемость

```bash
curl http://localhost:9090/metrics
curl http://localhost:9090/healthz
```

`--log-format=json` — машино-читаемые логи. `--log-level=debug` — verbose.

---

## 12. Docker

```bash
docker build -t horreum:latest .
docker run -d --rm \
    -p 7373:7373 -p 9090:9090 \
    -v $(pwd)/data:/var/lib/horreum:rw \
    -e HORREUM_KEY="$(openssl rand -hex 32)" \
    --name horreum horreum:latest \
    --persistent-path=/var/lib/horreum \
    --addr=:7373
```

---

## 13. Production checklist

- [ ] Реальный TLS-сертификат
- [ ] persistent_path на постоянном хранилище
- [ ] Бэкап ключа шифрования
- [ ] Prometheus scrape на :9090/metrics
- [ ] log-level: info, log-format: json
- [ ] Тест graceful shutdown (SIGTERM → `bye`)
- [ ] Тест recovery после kill -9
- [ ] Тюнинг region-size / shards / evict-capacity

---

## 14. Следующие шаги

- `architecture.md`
- `protocol.md`
- `internals.md`
- `operations.md`
