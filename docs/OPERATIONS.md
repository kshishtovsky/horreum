# Operations Guide

horreum is a single-binary in-memory KV cache with optional
persistence and a Prometheus metrics endpoint. This guide covers
installation, configuration, and day-2 operations.

## Quick Start

```bash
# Build the binary.
CGO_ENABLED=0 go build -ldflags="-s -w" -o horreum ./cmd/horreum

# Run in anonymous mode (no persistence).
./horreum \
    --addr=:7373 \
    --transport=tcp \
    --shards=$(nproc) \
    --region-size=4GiB

# Run in persistent mode.
./horreum \
    --addr=:7373 \
    --persistent-path=/var/lib/horreum \
    --durable=true \
    --metrics-addr=:9090

# Send a SET.
./bench --soak --rate=200000
```

## CLI Flags

| Flag                   | Default          | Description                                  |
|------------------------|------------------|----------------------------------------------|
| `--addr`               | `:7373`          | Listen address (host:port).                 |
| `--transport`          | `tcp`            | `tcp` or `quic`.                             |
| `--shards`             | `NumCPU`         | Number of shared-nothing shards.             |
| `--region-size`        | `1 GiB`          | Arena region size per shard.                 |
| `--evict-capacity`     | `4096`           | S3-FIFO eviction capacity per shard.         |
| `--persistent-path`    | (empty)           | If set, enables persistent mode at this dir. |
| `--durable`            | `true`           | fsync WAL after every write.                 |
| `--metrics-addr`       | `:9090`          | Prometheus /metrics listen address.          |
| `--tls-cert`           | (empty)           | TLS cert (required for QUIC).                |
| `--tls-key`            | (empty)           | TLS key (required for QUIC).                 |
| `--shutdown-timeout`   | `30s`            | Graceful shutdown timeout.                    |
| `--version`            | (empty)           | Print version and exit.                      |

## Persistent Mode

In persistent mode (`--persistent-path` set), horreum maintains two
on-disk files:

* `arena.dat` — memory-mapped arena region (MAP_SHARED on Linux).
* `wal.log` — append-only Write-Ahead Log of SET/DEL operations.

A `superblock` at the start of `arena.dat` records the layout and a
checkpoint offset. The WAL is fsync'd on every write in `--durable`
mode; the arena itself is fsync'd once per second by a background
ticker (or on graceful shutdown).

### Cold Start

On startup with an existing `--persistent-path`:

1. `ColdStart` opens `arena.dat` and `wal.log`.
2. The superblock is validated.
3. The WAL is replayed: every SET record re-Puts the value into the
   arena and updates the in-memory HashIndex; every DEL removes the
   key from the index.
4. The transport starts accepting connections.

### Crash Safety

If the process is killed (`kill -9`) mid-write, the worst case is:

* A WAL record whose header was written but whose payload was not —
  repaired by `repairTail` on reopen (the partial record is dropped).
* A SET whose WAL record was not yet fsync'd — lost. All other data
  is intact.

The arena file itself is durable across crashes because of
`MAP_SHARED` + periodic msync.

## Metrics

`/metrics` serves the Prometheus text format. Key metrics:

| Metric                            | Type      | Labels         |
|-----------------------------------|-----------|----------------|
| `horreum_ops_total`               | counter   | cmd, status    |
| `horreum_latency_seconds`         | histogram | cmd            |
| `horreum_memory_used_bytes`       | gauge     | region         |
| `horreum_memory_free_bytes`       | gauge     | region         |
| `horreum_evictions_total`         | counter   | queue (S/M)    |
| `horreum_index_keys_total`        | gauge     | (none)         |

Latency buckets: 100µs, 500µs, 1ms, 5ms, 10ms, 50ms, 100ms, 500ms, 1s, 5s.

`/healthz` returns `ok` for liveness probes.

## Graceful Shutdown

On `SIGINT` or `SIGTERM`, the shutdown coordinator runs hooks in
order, each bounded by `--shutdown-timeout`:

1. **stop-listener** — close the listener and drain active connections.
2. **storage** — if persistent, `Sync()` (MS_SYNC) then `Checkpoint()`
   to truncate the WAL, then close.
3. **metrics** — stop the metrics HTTP server.

The process exits with status 0 on success, 1 on the first hook
error.

## Resource Tuning

### Linux

For high-throughput deployments, raise the per-process file
descriptor limit:

```bash
# /etc/security/limits.d/horreum.conf
horreum soft nofile 1048576
horreum hard nofile 1048576
```

Increase the kernel's TCP socket limits if you expect >10K concurrent
connections:

```bash
sysctl -w net.core.somaxconn=4096
sysctl -w net.ipv4.tcp_max_syn_backlog=4096
```

### Memory Sizing

The arena consumes `--shards × --region-size` virtual address space
(resident set is smaller; pages are demand-paged). For example:

* `--shards=8 --region-size=4GiB` → 32 GiB virtual, ~RSS depends on
  workload.

Add ~10% overhead for the WAL and the per-handle meta array.

## Benchmarking

The `horreum-bench` binary runs synthetic workloads against the
arena allocator. It is not a network benchmark — for that, use the
benchmarks under `internal/transport`.

```bash
# Quick throughput test.
./horreum-bench --region-size=1GiB

# Long-running soak.
./horreum-bench --soak --rate=200000 --duration=1000000 --region-size=1GiB
```

## Docker

```bash
docker build -t horreum:dev .
docker run -d --name horreum -p 7373:7373 -p 9090:9090 \
    -v horreum-data:/var/lib/horreum \
    horreum:dev \
    --persistent-path=/var/lib/horreum
```

The image is multi-stage and uses `scratch` as the runtime base — only
the compiled binary, timezone data, and a non-root user entry are
included.

## Troubleshooting

* **`persist: bad superblock magic`** — the arena file is corrupt
  or was not created by horreum. Delete `--persistent-path` and let
  horreum recreate it (data loss).
* **`bind: address already in use`** — another process owns the
  port. Pick a different `--addr`.
* **High latency on QUIC** — QUIC requires UDP. Make sure no
  firewall drops UDP packets.
* **Memory leak on crash** — call `pm.Sync()` and `pm.Close()`
  before `os.Exit`; the shutdown coordinator handles this
  automatically.

## Security

* Run as a non-root user (the Dockerfile ships with UID 1000).
* TLS is required for QUIC; for production, also enable TLS on TCP.
* Persistent files contain all keys — set the directory permissions
  to `0700`.