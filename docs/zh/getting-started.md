# Horreum 快速入门指南 🚀

[English](../getting-started.md) | [Русский](../ru/getting-started.md) | [中文](getting-started.md)

---

## 1. 安装说明

### 环境要求
- **Go**：1.22 或更高版本。
- **操作系统**：Linux（推荐，可使用原生 `mmap`）、macOS 或 Windows。

### 源码编译

```bash
# 克隆代码库
git clone https://github.com/horreum/horreum.git
cd horreum

# 编译二进制文件
go build -o horreum ./cmd/horreum

# 验证安装
./horreum --version
```

---

## 2. 运行 Horreum

### 使用默认配置运行

默认情况下，Horreum 启动 4 个缓存分片，每个分片分配 256 MiB 内存，监听 TCP 端口 `:7373`：

```bash
./horreum --config config.yaml
```

### 开启持久化模式运行

开启预写日志（WAL）与磁盘落盘保障：

```bash
./horreum --config config.yaml --persistent-path=/var/lib/horreum --durable=true
```

### 使用 Docker 运行

```bash
# 构建容器镜像
docker build -t horreum:latest .

# 后台运行容器
docker run -d \
  -p 7373:7373 \
  -p 9090:9090 \
  -v $(pwd)/config.yaml:/etc/horreum/config.yaml \
  --name horreum-server \
  horreum:latest
```

---

## 3. 客户端连接

Horreum 通过 TCP 端口 `7373` 使用高效的二进制帧协议。开箱即用的客户端实现见 [`examples/`](file:///e:/pet/horreum/examples/) 目录。
