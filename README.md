<p align="center">
  <img src="docs/repo_image.png" alt="Horreum Banner" width="600"/>
</p>

<h1 align="center">Horreum ⚡</h1>

<p align="center">
  <i>Ultra-fast, zero-allocation, authenticated in-memory & persistent cache service written in pure Go.</i>
</p>

<p align="center">
  <a href="README.md">English</a> | <a href="README.ru.md">Русский</a> | <a href="README.zh.md">中文</a>
</p>

---

## Overview

**Horreum** is a high-performance distributed key-value caching system designed with a strict zero-allocation hot-path architecture. Powered by custom memory-mapped arena allocators, S3-FIFO eviction queues, pure-Go LZ4 compression, and authenticated AES-256-GCM encryption at rest, Horreum achieves microsecond latency with zero garbage collection overhead on value storage.

## Key Features

- **Zero-Allocation Hot Path**: Custom `mmap` arena allocator (`internal/arena`) operating on `unsafe.Slice` handles. No heap GC pressure on value mutations.
- **S3-FIFO Eviction**: Lock-free touch operations via atomic frequency counters and dual ring-buffer queues (Small & Main) outperforming classic LRU.
- **Dual Transport Layer**: 
  - **TCP**: Connection-pinning router hashing `(remoteIP + localPort)` to shard workers.
  - **QUIC**: Native TLS 1.3 multiplexed stream support using `quic-go`.
- **Pure-Go LZ4 Compression**: Optional fast value compression with pooled buffer recycling achieving **~2 GB/s** decompress throughput.
- **Authenticated Encryption at Rest**: AES-256-GCM AEAD encryption with automated 32-byte key generation (**~4.9 GB/s** decrypt throughput via AES-NI).
- **Stdlib-Only YAML Config**: Zero-dependency YAML configuration parser supporting binary unit sizes (`256MiB`, `1GiB`).
- **Low-Overhead Logging**: `log/slog` structured logging with token-bucket rate-limiting on network errors and slow-log threshold tracking for WAL syncs.
- **Durable Persistence**: Write-Ahead Logging (WAL) with cold-start replay and atomic HashIndex checkpointing.

---

## Performance Benchmarks

*Measured on AMD Ryzen 9 7950X3D (Go 1.22, Windows/Linux)*

### Value Encryption (AES-256-GCM)

| Payload Size | Encrypt Throughput | Encrypt Latency | Decrypt Throughput | Decrypt Latency |
| :--- | :--- | :--- | :--- | :--- |
| **256 B** | 1201 MB/s | 213 ns | 2302 MB/s | 111 ns |
| **4 KB** | 2532 MB/s | 1.6 µs | 3713 MB/s | 1.1 µs |
| **64 KB** | 3072 MB/s | 21.3 µs | 4164 MB/s | 15.7 µs |
| **1 MB** | **4230 MB/s** | 247 µs | **4930 MB/s** | 212 µs |

### Value Compression (LZ4)

| Payload Size | Compress Throughput | Decompress Throughput |
| :--- | :--- | :--- |
| **256 B** | 8.9 MB/s | 1796 MB/s |
| **4 KB** | 131 MB/s | 2051 MB/s |
| **64 KB** | 881 MB/s | 2179 MB/s |
| **1 MB** | **1242 MB/s** | **2052 MB/s** |

---

## Quick Start

### Build & Run locally

```bash
# Build binary
go build -o horreum ./cmd/horreum

# Run with standard YAML configuration
./horreum --config config.yaml
```

### Run with Docker

```bash
# Build Docker image
docker build -t horreum:latest .

# Run container
docker run -d -p 7373:7373 -p 9090:9090 --name horreum horreum:latest
```

---

## Client Examples

Horreum uses a lightweight 10-byte binary wire protocol. Ready-to-use client implementations are provided in the [`examples/`](file:///e:/pet/horreum/examples/) directory:

- [Python Client](file:///e:/pet/horreum/examples/client.py)
- [Node.js Client](file:///e:/pet/horreum/examples/client.js)
- [Go Client](file:///e:/pet/horreum/examples/client.go)

### Python Example

```python
from examples.client import HorreumClient

client = HorreumClient('127.0.0.1', 7373)
client.connect()

# Set key
client.set("user:1001", "Alice")

# Get key
val = client.get("user:1001")
print("User:", val.decode())

client.close()
```

---

## Documentation

For comprehensive guides and architecture details, refer to the [`docs/`](file:///e:/pet/horreum/docs/) directory:

- [Getting Started Guide](file:///e:/pet/horreum/docs/getting-started.md) — Detailed setup, client usage, and Docker deployment.
- [Configuration Reference](file:///e:/pet/horreum/docs/configuration.md) — Full `config.yaml` and CLI flag specifications.
- [Protocol Specification](file:///e:/pet/horreum/docs/protocol.md) — 10-byte binary frame specification.
- [Architecture Deep Dive](file:///e:/pet/horreum/docs/architecture.md) — Memory layout, concurrency, and persistence design.

---

## License

Standard repository license applies. See `AGENTS.md` for project directives and code style conventions.
