# 配置指南与说明 ⚙️

[English](../configuration.md) | [Русский](../ru/configuration.md) | [中文](configuration.md)

---

## 配置文件规范 (`config.yaml`)

Horreum 配置文件说明：

```yaml
server:
  addr: ":7373"             # 客户端连接监听地址与端口
  transport: "tcp"          # 传输层协议："tcp" 或 "quic"
  shards: 4                 # 内存分片数量
  region_size: "256MiB"     # 每个分片的 mmap 内存大小
  evict_capacity: 4096      # 每个分片的 S3-FIFO 淘汰队列容量上限
  tls_cert: ""              # TLS 证书路径（仅 QUIC 需要）
  tls_key: ""               # TLS 私钥路径（仅 QUIC 需要）
  shutdown_timeout: "30s"   # 平滑关闭最大超时等待时间

storage:
  persistent_path: ""       # 磁盘持久化目录（为空则为内存模式）
  durable: true             # 是否在每次写入后强制刷新 WAL 到磁盘 (fsync)

metrics:
  addr: ":9090"             # Prometheus 指标导出地址

compression:
  algorithm: "none"         # 值压缩算法："none" 或 "lz4"
  min_size: 64              # 触发 LZ4 压缩的最小载荷字节数

logging:
  level: "info"             # 日志级别：debug, info, warn, error
  format: "text"            # 日志输出格式：text 或 json
  slow_log_threshold: "10ms" # WAL 刷新慢日志告警阈值

security:
  encryption_key_path: ""   # 32 字节 AES 加密密钥文件路径
```

---

## 环境变量

- `HORREUM_KEY`: 32 字节二进制、64 字符十六进制 (Hex) 或 44 字符 Base64 AES-256 加密密钥。优先级高于 `encryption_key_path` 配置文件设定。
