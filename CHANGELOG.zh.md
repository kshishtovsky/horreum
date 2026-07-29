# 更新日志 (Changelog)

本项目的重大变更均将记录于此文件中。

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.0.0/) 格式规范，
并严格遵守 [语义化版本 (Semantic Versioning)](https://semver.org/lang/zh-CN/) 规范。

[English](CHANGELOG.md) | [Русский](CHANGELOG.ru.md) | [中文](CHANGELOG.zh.md)

---

## [1.0.0] - 2026-07-29

### 新增功能
- **核心引擎与内存管理**：
  - 基于纯 Go 的零内存分配 mmap Arena 内存分配器（`internal/arena`），包含 Bump 分配与隔离空闲列表（Segregated Freelist）。
  - 自定义 S3-FIFO 缓存淘汰策略（`internal/eviction`），具备原子频率计数器与双环形缓冲区（Small & Main）。
  - 开放寻址 Hash 索引（`internal/index`），支持二次探查与内联键优化。
- **传输层与网络**：
  - TCP 传输服务器（`internal/transport/tcp`），具备基于 `(remoteIP + localPort)` 哈希的连接绑定路由。
  - QUIC 传输服务器（`internal/transport/quic`），基于 `quic-go` 提供原生 TLS 1.3 多路复用流支持。
  - 10 字节零内存分配二进制帧协议解析器（`internal/proto`）。
- **数据安全与压缩**：
  - 纯 Go 实现的 LZ4 块压缩编解码器（`internal/compress`），具备大小分类缓冲区池复用。
  - AES-256-GCM 静态数据安全加密（`internal/encrypt`），支持随机 12 字节 Nonce 生成。
  - 支持通过 `--keygen` 命令行参数及自动创建 32 字节 AES 密钥文件。
- **日志与配置**：
  - 纯 Go 标准库 YAML 解析器（`internal/config`），支持缩进栈解析与易读内存单位（如 `256MiB`, `1GiB`）。
  - 基于 Go 1.21+ `log/slog` 的结构化日志（`internal/logger`），具备令牌桶限流与 WAL 慢日志预警。
- **客户端与工具**：
  - 开箱即用的 Python（`examples/client.py`）、Node.js（`examples/client.js`）及 Go（`examples/client.go`）客户端。
  - 生产级 Dockerfile 及根目录 `config.yaml` 模板。
  - Prometheus 指标导出器集成（`internal/metrics`）。

### 修复问题
- **平滑关闭协调器 (Shutdown)**：修复服务启动后立即触发关闭钩子的逻辑缺陷。
- **Windows mmap 兼容性**：为非 Linux 操作系统平台补充安全的内存分配回退机制。
