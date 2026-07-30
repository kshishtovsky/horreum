# Getting Started with Horreum 🚀

[English](getting-started.md) | [Русский](ru/getting-started.md) | [中文](zh/getting-started.md)

---

## 1. Prerequisites

- **Go 1.22+** (tested on 1.25). Install from [go.dev](https://go.dev/dl/).
- **GCC not required** — Horreum is CGO-free.
- **Linux** recommended for mmap persistence (Windows / macOS work for anonymous mode).
- **~64 MiB free RAM** per shard; 256 MiB default per shard.

For QUIC, you also need TLS certificates (self-signed is fine for testing).

---

## 2. Building

### 2.1 From Source

```bash
git clone https://github.com/kshishtovsky/horreum.git
cd horreum
go build -o horreum ./cmd/horreum
go build -o horreum-bench ./cmd/horreum-bench
```

Produces a static binary (`CGO_ENABLED=0` by default).

### 2.2 From Docker

```bash
docker build -t horreum:latest .
```

---

## 3. Running Locally (Anonymous Mode)

```bash
./horreum --addr=:7373
```

Output:
```
INFO ready; waiting for signal
INFO horreum listening addr=:7373 transport=tcp
INFO metrics listening url=http://:9090/metrics
```

### 3.1 Smoke Test

```bash
# Set a key
( printf '\x48\x48\x02\x00\x06\x00\x05\x00\x00\x00'; \
  printf 'hello'; \
  printf 'world' ) | nc -q1 localhost 7373 | xxd

# GET it back
( printf '\x48\x48\x01\x00\x06\x00\x00\x00\x00\x00'; \
  printf 'hello' ) | nc -q1 localhost 7373 | xxd
```

Or use one of the reference clients in `examples/`.

---

## 4. Running with Persistence

```bash
mkdir -p /var/lib/horreum
./horreum --persistent-path=/var/lib/horreum
```

### 4.1 Verify Persistence

```bash
./horreum --persistent-path=/var/lib/horreum &
PID=$!
./examples/client set mykey "data"
kill $PID

./horreum --persistent-path=/var/lib/horreum &
./examples/client get mykey
```

### 4.2 Watch the WAL

```bash
xxd /var/lib/horreum/wal.log | head -50
```

---

## 5. Adding Encryption

```bash
./horreum --keygen=/etc/horreum.key
chmod 0600 /etc/horreum.key

./horreum \
    --persistent-path=/var/lib/horreum \
    --encryption-key-path=/etc/horreum.key
```

All values are now AES-256-GCM encrypted at rest. A new key cannot decrypt old data — back up the key.

---

## 6. Adding Compression

```bash
./horreum \
    --persistent-path=/var/lib/horreum \
    --compression=lz4 \
    --min-compress-size=64
```

Values ≥ 64 bytes are LZ4-compressed before encryption.

---

## 7. QUIC Transport with TLS

```bash
openssl req -x509 -newkey rsa:2048 -nodes \
    -keyout /tmp/key.pem -out /tmp/cert.pem \
    -days 365 -subj "/CN=localhost"

./horreum \
    --transport=quic \
    --tls-cert=/tmp/cert.pem \
    --tls-key=/tmp/key.pem \
    --addr=:7373
```

---

## 8. Configuration Files

Create `/etc/horreum.yaml`:

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

CLI flags override YAML values:

```bash
./horreum --config=/etc/horreum.yaml --transport=quic --shards=16
```

---

## 9. Reference Clients

### 9.1 Go Client

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

    if err := h.Set("greeting", []byte("hello world"), 0); err != nil { panic(err) }

    val, err := h.Get("greeting")
    if err != nil { panic(err) }

    fmt.Printf("greeting = %q\n", string(val))
}
```

### 9.2 Python Client

```python
from examples.client import HorreumClient

client = HorreumClient("127.0.0.1", 7373)
client.connect()
client.set("user:1001", b"Alice")
val = client.get("user:1001")
print(f"User: {val.decode()}")
client.close()
```

### 9.3 Node.js Client

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

Each client implements all 21 opcodes and demonstrates pipelining.

---

## 10. Benchmarking

```bash
# Basic GET/SET benchmark
./horreum-bench --rate=100000 --duration=1000000

# Long soak (Pareto sizes 4 KiB..16 KiB)
./horreum-bench --soak --rate=200000 --sizedist=pareto:4096:16384 --duration=1000000
```

Output:
```
op        rate    p50    p95    p99    errors
GET    102367    432ns  812ns  1.2µs  0
SET    100113    1.4µs  2.1µs  3.7µs  0
```

Flags: `--rate N`, `--duration N`, `--sizedist=value:min:max`, `--read-pct N`, `--pipeline N`.

---

## 11. Observability

### 11.1 Metrics

```bash
curl http://localhost:9090/metrics
```

### 11.2 Health Check

```bash
curl http://localhost:9090/healthz
# → 200 OK
```

### 11.3 Logs

`--log-format=json` produces machine-parseable logs. `--log-level=debug` adds per-op latency lines.

---

## 12. Container Workflow

```bash
docker build -t horreum:latest .
docker run -d --rm \
    -p 7373:7373 \
    -p 9090:9090 \
    -v $(pwd)/data:/var/lib/horreum:rw \
    -e HORREUM_KEY="$(openssl rand -hex 32)" \
    --name horreum horreum:latest \
    --persistent-path=/var/lib/horreum \
    --addr=:7373
```

Probes:
```yaml
livenessProbe:
  httpGet: { path: /healthz, port: 9090 }
  initialDelaySeconds: 1
  periodSeconds: 5

readinessProbe:
  httpGet: { path: /healthz, port: 9090 }
  initialDelaySeconds: 1
  periodSeconds: 2
```

---

## 13. Production Checklist

- [ ] `transport: tcp` or `quic` with a real TLS cert.
- [ ] `persistent_path` on persistent storage.
- [ ] Back up the encryption key to a separate, secure location.
- [ ] Configure Prometheus scraping on `:9090/metrics`.
- [ ] `log-level: info`, `log-format: json`.
- [ ] Verify WAL + checkpoint by stopping/restarting.
- [ ] Tune `region-size`, `shards`, `evict-capacity`.
- [ ] Decide on encryption + compression.
- [ ] Test graceful shutdown (`SIGTERM` → `bye` log).
- [ ] Test recovery from a crash (`kill -9`, restart, verify).

---

## 14. Next Steps

- `architecture.md` — system overview.
- `protocol.md` — wire format details.
- `internals.md` — code-level reference.
- `operations.md` — production deployment.
