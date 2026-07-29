<p align="center">
  <img src="docs/repo_image.png" alt="Horreum Banner" width="600"/>
</p>

<h1 align="center">Horreum ⚡</h1>

<p align="center">
  <i>基于纯 Go 编写的极速、零内存分配、具备安全加密与持久化支持的内存缓存服务。</i>
</p>

<p align="center">
  <a href="README.md">English</a> | <a href="README.ru.md">Русский</a> | <a href="README.zh.md">中文</a>
</p>

---

## 概述

**Horreum** 是一个高性能的分布式键值缓存系统，采用严格的零内存分配（Zero-Allocation）热路径架构设计。系统基于自定义 mmap 内存映射 Arena 分配器、S3-FIFO 淘汰算法、纯 Go 实现的 LZ4 压缩以及 AES-256-GCM 静态数据加密构建。Horreum 实现了微秒级延迟，且在值存储与变更时对垃圾回收（GC）零压力。

## 核心特性

- **热路径零内存分配**：自定义 `mmap` Arena 分配器（`internal/arena`）配合 `unsafe.Slice` 句柄，避免垃圾回收（GC）压力。
- **S3-FIFO 淘汰算法**：基于原子频率计数器和双环形缓冲区（Small & Main）实现无锁 Touch 操作，性能超越传统 LRU。
- **双传输层支持**：
  - **TCP**：基于 `(remoteIP + localPort)` 哈希的连接绑定路由，固定分配至分片 Worker。
  - **QUIC**：基于 `quic-go` 的原生 TLS 1.3 多路复用流支持。
- **纯 Go LZ4 压缩**：可选的高速值压缩，结合对象池缓冲区复用，解压吞吐量高达 **~2 GB/s**。
- **静态数据安全加密**：AES-256-GCM AEAD 加密，支持自动生成 32 字节密钥（借助 AES-NI 指令集解密吞吐量高达 **~4.9 GB/s**）。
- **纯标准库 YAML 解析器**：无外部依赖的 YAML 配置解析器，支持二进制内存单位（`256MiB`, `1GiB`）。
- **低开销日志系统**：基于 `log/slog` 的结构化日志，具备网络错误令牌桶限流及 WAL 刷新慢日志追踪。
- **持久化存储**：具备冷启动重放与 HashIndex 原子检查点功能的预写日志（WAL）。

---

## 性能基准测试

*测试环境：AMD Ryzen 9 7950X3D (Go 1.22, Windows/Linux)*

### 数据加密性能 (AES-256-GCM)

| 数据大小 | 加密吞吐量 | 加密延迟 | 解密吞吐量 | 解密延迟 |
| :--- | :--- | :--- | :--- | :--- |
| **256 B** | 1201 MB/s | 213 ns | 2302 MB/s | 111 ns |
| **4 KB** | 2532 MB/s | 1.6 µs | 3713 MB/s | 1.1 µs |
| **64 KB** | 3072 MB/s | 21.3 µs | 4164 MB/s | 15.7 µs |
| **1 MB** | **4230 MB/s** | 247 µs | **4930 MB/s** | 212 µs |

### 数据压缩性能 (LZ4)

| 数据大小 | 压缩吞吐量 | 解压吞吐量 |
| :--- | :--- | :--- |
| **256 B** | 8.9 MB/s | 1796 MB/s |
| **4 KB** | 131 MB/s | 2051 MB/s |
| **64 KB** | 881 MB/s | 2179 MB/s |
| **1 MB** | **1242 MB/s** | **2052 MB/s** |

---

## 快速开始

### 本地编译与运行

```bash
# 编译二进制文件
go build -o horreum ./cmd/horreum

# 使用 YAML 配置文件运行
./horreum --config config.yaml
```

### 使用 Docker 运行

```bash
# 构建 Docker 镜像
docker build -t horreum:latest .

# 运行容器
docker run -d -p 7373:7373 -p 9090:9090 --name horreum horreum:latest
```

---

## 客户端示例

Horreum 使用轻量级 10 字节二进制网络协议。[`examples/`](file:///e:/pet/horreum/examples/) 目录中提供了开箱即用的客户端实现：

- [Python 客户端](file:///e:/pet/horreum/examples/client.py)
- [Node.js 客户端](file:///e:/pet/horreum/examples/client.js)
- [Go 客户端](file:///e:/pet/horreum/examples/client.go)

### Python 示例

```python
from examples.client import HorreumClient

client = HorreumClient('127.0.0.1', 7373)
client.connect()

# 设置键值
client.set("user:1001", "Alice")

# 获取键值
val = client.get("user:1001")
print("User:", val.decode())

client.close()
```

---

## 文档指南

更多详细指南与架构说明，请参阅 [`docs/`](file:///e:/pet/horreum/docs/) 目录：

- [快速入门指南 (zh)](file:///e:/pet/horreum/docs/zh/getting-started.md)
- [配置参考指南 (zh)](file:///e:/pet/horreum/docs/zh/configuration.md)
- [网络协议规范 (zh)](file:///e:/pet/horreum/docs/zh/protocol.md)
- [系统架构详解 (zh)](file:///e:/pet/horreum/docs/zh/architecture.md)
