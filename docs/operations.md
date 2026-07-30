# Horreum Operations Guide 🛡️

[English](operations.md) | [Русский](ru/operations.md) | [中文](zh/operations.md)

---

## 1. Deployment Topologies

### 1.1 Anonymous Mode (Stateless Cache)

```
Client --TCP/QUIC--> horreum :7373  (single instance or L4 LB → multiple)
                       |
                       |-- /metrics  (Prometheus scrape)
                       +-- (in-memory only)
```

- Default mode: `--persistent-path=""` (or just omit).
- Process restart loses all data; design accordingly.
- Suitable for distributed caches that tolerate cold-start (Redis, memcached replacement).

### 1.2 Persistent Mode (Durable Cache)

```
Client --TCP/QUIC--> horreum :7373
                       |
                       |-- /metrics    (Prometheus)
                       +-- /var/lib/horreum/
                           |-- arena.dat   ← MAP_SHARED
                           +-- wal.log     ← append-only WAL
```

- WAL fsync each write (`--durable=true`).
- Background `MS_ASYNC` ticker every 1 second.
- Cold start: load checkpoint → replay WAL.

### 1.3 HA Topology

Horreum does not implement replication. For HA: run two processes with disjoint data (via application-side key hashing) and write to both. Or use a write-through proxy hitting both `horreum-east` and `horreum-west`.

### 1.4 Vertical Scaling

```
                 +--------------------------+
                 |  horreum (1 shard set)   |
                 |  N shards × R region    |
                 +--------------------------+
                              N = 4 × NumCPU (clamp 4..64)
                              R = 256 MiB..4 GiB
```

Memory budget = `N × R × (1 + ε)`.

---

## 2. Sizing & Capacity Planning

### 2.1 Capacity Rules of Thumb

| Avg value size | Expected objects @ 256 MiB region | With 4 × 1 GiB total |
|----------------|-----------------------------------|----------------------|
| 64 B           | 4 M objects / shard              | 64 M total           |
| 1 KiB          | 256 K objects / shard             | 4 M total            |
| 16 KiB         | 16 K objects / shard              | 256 K total          |
| 256 KiB        | 1 K objects / shard               | 16 K total           |

`evict_capacity` ≈ 1.5× working set in object count.

### 2.2 Sizing Math

```
working_set_bytes  = avg_value_size × working_set_objects + ε for index + headers
arena_total        ≥ working_set_bytes × 1.5         # 33% free for compaction headroom
shards             = arena_total / region_size       # 256 MiB default per shard
evict_capacity     = (working_set_objects / shards) × 1.5
```

### 2.3 Memory Layout

A 256 MiB anonymous arena:
```
data slice: 256 MiB            (mmap'd; VSZ until touched)
meta slice: 256 MiB / 8 = 32 MiB  (allocated on first Put)
freelist:   ≤ one entry per freed size-class slot
```

The 32 MiB `meta` slice is Go-heap memory; OOM killer may target it under pressure.

---

## 3. Bootstrap Procedure

### 3.1 Fresh Install (Anonymous)

```bash
go build -o /usr/local/bin/horreum ./cmd/horreum
cat > /etc/horreum.yaml <<EOF
server:
  addr: ":7373"
  transport: tcp
  shards: 8
  region_size: "1GiB"
metrics:
  addr: ":9090"
EOF

./horreum --config=/etc/horreum.yaml
```

### 3.2 Fresh Install (Persistent + Encryption)

```bash
# Generate key
./horreum --keygen=/etc/horreum.key
chmod 0600 /etc/horreum.key

# Self-signed cert for QUIC (testing only)
openssl req -x509 -newkey rsa:2048 -nodes \
    -keyout /etc/horreum.key -out /etc/horreum.crt \
    -days 365 -subj "/CN=horreum.local"

mkdir -p /var/lib/horreum

cat > /etc/horreum.yaml <<EOF
server:
  transport: quic
  tls_cert: "/etc/horreum.crt"
  tls_key: "/etc/horreum.key"
  shards: 4
  region_size: "1GiB"
storage:
  persistent_path: "/var/lib/horreum"
  durable: true
metrics:
  addr: ":9090"
compression:
  algorithm: lz4
security:
  encryption_key_path: "/etc/horreum.key"
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

## 4. Monitoring

### 4.1 Recommended Prometheus Alerts

```yaml
groups:
  - name: horreum
    rules:
      # GET p99 > 5 ms for 5 min
      - alert: HorreumSlowReads
        expr: |
          histogram_quantile(0.99,
            sum by (le) (rate(horreum_get_latency_seconds_bucket[5m]))
          ) > 0.005
        for: 5m
        labels: {severity: warning}

      # Error rate > 1% for 5 min
      - alert: HorreumErrorRate
        expr: |
          rate(horreum_get_observations_total{status="err"}[5m])
          / ignoring(status)
          sum without(status) (rate(horreum_get_observations_total[5m]))
          > 0.01
        for: 5m
        labels: {severity: critical}

      # Arena utilisation > 90%
      - alert: HorreumArenaFull
        expr: |
          horreum_memory_used_bytes / (horreum_memory_used_bytes + horreum_memory_free_bytes)
          > 0.9
        for: 10m
        labels: {severity: warning}

      # Slow WAL sync rate increasing
      - alert: HorreumSlowWalSync
        expr: rate(horreum_slow_wal_sync_total[5m]) > 0
        for: 10m
        labels: {severity: warning}
```

### 4.2 Key Dashboards

| Panel | Query |
|-------|-------|
| GET p99 | `histogram_quantile(0.99, sum by (le) (rate(horreum_get_latency_seconds_bucket[5m])))` |
| SET p99 | `histogram_quantile(0.99, sum by (le) (rate(horreum_set_latency_seconds_bucket[5m])))` |
| Throughput | `sum(rate(horreum_get_observations_total[1m])) + sum(rate(horreum_set_observations_total[1m]))` |
| Error rate | `rate(horreum_*_observations_total{status="err"}[5m]) / sum without(status) (rate(horreum_*_observations_total[5m]))` |
| Memory used | `horreum_memory_used_bytes` |
| Index keys | `horreum_index_keys_total` |
| Evictions | `rate(horreum_evictions_total[5m])` by (queue) |

### 4.3 Profiling

`net/http/pprof` is wired into the metrics HTTP server:

```bash
curl http://horreum:9090/debug/pprof/heap > heap.pprof
go tool pprof heap.pprof
```

Targets: `/debug/pprof/profile` (30s CPU), `/debug/pprof/heap`, `/debug/pprof/goroutine`.

---

## 5. Recovery Scenarios

### 5.1 Cold Start (Process Restart)

```bash
./horreum --persistent-path=/var/lib/horreum
```

Logs `cold start complete live_keys=N` after replay.

### 5.2 Partial WAL Corruption

If the WAL tail has a torn record, `repairTail` truncates it on open. Check logs for `wal repair tail truncated N bytes`.

### 5.3 Superblock Corruption

If superblock magic is missing or version mismatches, Horreum treats the arena as fresh; the entire WAL is replayed. Check logs for `superblock invalid; full WAL replay expected`.

### 5.4 Arena File Truncated

If `arena.dat` is truncated below `regionSize`, `munmap` fails on `Close`. The next restart will detect the truncation and refuse to open.

**Mitigation**: back up `arena.dat` and `wal.log` together as a unit.

### 5.5 Forgotten Encryption Key

If the encryption key file is lost, all encrypted values are unrecoverable. Wipe and start fresh:

```bash
./horreum --keygen=/etc/horreum.key
rm -rf /var/lib/horreum
./horreum --persistent-path=/var/lib/horreum --encryption-key-path=/etc/horreum.key
```

### 5.6 Key Rotation

There is **no built-in key rotation**. To rotate: start new process, re-encrypt via read+write through the API, decommission old.

---

## 6. Backup & Restore

### 6.1 Online Snapshot

Horreum does not expose a snapshot API. To create a consistent backup:

1. Trigger a checkpoint (send SIGTERM, let `coord.Run` shut down gracefully).
2. Copy `arena.dat` + `wal.log` together.
3. Restart the process.

### 6.2 Periodic Checkpoint

Add a wrapper that periodically sends SIGTERM and restarts (heavier than ideal). Better: hook into the application to call a snapshot API (future work).

### 6.3 Restoring

```bash
systemctl stop horreum
rsync -a /backup/horreum/ /var/lib/horreum/
chown -R horreum:horreum /var/lib/horreum
systemctl start horreum
```

---

## 7. Capacity & Performance Tuning

### 7.1 Shard Count

| Workload | Recommended |
|----------|-------------|
| Single-tenant high QPS | `8..16` |
| Multi-tenant mixed keys | `16..32` |
| Mostly inserts, low read p99 | `32..64` |

### 7.2 Region Size

| Avg value size | Region size |
|----------------|-------------|
| < 4 KiB | 256 MiB |
| 4..64 KiB | 512 MiB |
| > 64 KiB | 1 GiB+ |

### 7.3 Compress / Encrypt Trade-offs

| Mode | GET p99 | SET p99 | Disk |
|------|---------|---------|------|
| plain | 600 ns | 1.2 µs | full |
| + lz4 | 1.4 µs | 6 µs | 30..60% |
| + aes-gcm | 600 ns | 12 µs | full |
| + lz4 + aes-gcm | 1.4 µs | ~30 µs | 30..60% |

LZ4 is almost always worth it. Encrypt only when the threat model demands; software AES (~500 MB/s) cuts throughput ~10×.

### 7.4 Eviction Capacity

```
evict_capacity = (working_set_objects / shards) × 1.5
```

### 7.5 WAL Sync Cadence

Default `MS_ASYNC` ticker is 1 second. Lower (e.g. 100 ms) reduces data loss window with more syscalls. The fsync-on-write path (`--durable=true`) is independent.

---

## 8. Operational Patterns

### 8.1 Graceful Shutdown

Send SIGTERM. Coordinator runs:

```
1. Stop listener.
2. Drain active connections (bounded by --shutdown-timeout).
3. Persist (sync + checkpoint + close).
4. Stop metrics HTTP server.
```

If shutdown exceeds timeout, process exits forcefully.

### 8.2 Rolling Restart

```
# Drain traffic from A, SIGTERM, wait for "bye" log, restart, restore traffic.
```

### 8.3 Capacity Increase

Migrate to a new Horreum instance with larger region and/or more shards. There is no online resize.

### 8.4 Eviction Tuning Under Pressure

If arena hits 90% and eviction can't keep up: lower `--evict-capacity`, increase `--region-size`, or reduce value size (more aggressive compression).

Heavy `S` queue evictions = many one-hit wonders. Heavy `M` = long-tail reuse.

---

## 9. Incident Response

### 9.1 Service is Slow

1. Check `horreum_get_latency_seconds` p99.
2. Check `horreum_memory_used_bytes` (near 100%? eviction starved).
3. Check `horreum_slow_wal_sync_total` (high = slow disk).
4. Profile with `go tool pprof`.

### 9.2 Service Crashed

1. Check last log lines for panic stack.
2. Try restart; WAL + checkpoint should recover.
3. If `munmap` failed, arena may be corrupt — restore from backup.

### 9.3 Data Loss After Restart

1. Verify `--persistent-path` is the same as before.
2. Check WAL on disk.
3. `cold start` logs should show `replayed N records, live keys = M`.
4. If `M` is much smaller than expected, WAL was rotated before crash — recover from previous checkpoint.

### 9.4 Cannot Decrypt Values

1. Verify `--encryption-key-path`.
2. Try `HORREUM_KEY` env var explicitly.
3. If the key really changed, values are unrecoverable.

---

## 10. Known Limitations & Workarounds

| Limitation | Workaround |
|------------|------------|
| No online resize | Migrate to new instance. |
| No built-in replication | Application-level dual-write. |
| No key rotation | Re-encrypt via application or wipe. |
| Single-arena persistent mode | Use anonymous for multi-shard persistent (trade-off: no persistence). |
| WAL bounded at `min(regionSize/4, 64 MiB)` | `wal.log` auto-rotates after checkpoint. Adjust region if 64 MiB feels tight. |
| Connections don't survive shard rebalancing | Reconnect after restart. |

---

## 11. Upgrades & Compatibility

- **Wire format**: stable across minor versions; new opcodes appended.
- **On-disk format**: WAL v1, superblock v1, checkpoint v1 — all stable across 1.x.
- **Migrations**: none required across minor versions.

---

## 12. SLOs

| SLO | Target |
|-----|--------|
| GET p99 latency (in-memory, plain) | < 1 µs |
| SET p99 latency (in-memory, plain) | < 5 µs |
| GET p99 latency (encrypted cache hit) | < 10 µs |
| Recovery time after restart | < 5 s + WAL replay |
| Data loss window (durable mode) | 0 (one fsync per write) |
| Data loss window (non-durable) | 1 s (background ticker) |

Set alerts on these SLOs.
