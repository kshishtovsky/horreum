# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [1.0.0] - 2026-07-29

### Added
- **Core Engine & Memory Management**:
  - Pure-Go zero-allocation memory-mapped arena allocator (`internal/arena`) with bump allocation and segregated freelists.
  - Custom S3-FIFO eviction policy (`internal/eviction`) featuring atomic frequency counters and dual ring-buffer queues (Small & Main).
  - Open-addressing hash index (`internal/index`) with quadratic probing and inline-key optimization.
- **Transports & Networking**:
  - TCP transport server (`internal/transport/tcp`) with connection-pinning based on `(remoteIP + localPort)` hashing.
  - QUIC transport server (`internal/transport/quic`) with native TLS 1.3 stream multiplexing via `quic-go`.
  - 10-byte zero-alloc binary frame protocol parser (`internal/proto`).
- **Data Protection & Compression**:
  - Pure-Go LZ4 block compression codec (`internal/compress`) with size-class buffer pool recycling.
  - Authenticated encryption at rest (`internal/encrypt`) using AES-256-GCM AEAD mode with random 12-byte nonce generation.
  - Automated 32-byte AES key generation via `--keygen` CLI flag and auto-file creation.
- **Logging & Configuration**:
  - Custom stdlib-only YAML parser (`internal/config`) with indented stack parsing and human-readable size units (e.g. `256MiB`, `1GiB`).
  - Structured logging using Go 1.21+ `log/slog` (`internal/logger`) with token-bucket rate limiting and slow-log threshold alerts for WAL operations.
- **Clients & Tooling**:
  - Standalone client SDKs in Python (`examples/client.py`), Node.js (`examples/client.js`), and Go (`examples/client.go`).
  - Production Dockerfile and root `config.yaml` template.
  - Prometheus metrics exporter integration (`internal/metrics`).

### Fixed
- **Shutdown Coordinator**: Resolved early termination issue where shutdown hooks executed immediately on startup.
- **Windows mmap Compatibility**: Added safe anonymous memory fallback allocation for non-Linux OS platforms.
