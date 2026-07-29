# Getting Started with Horreum 🚀

[English](getting-started.md) | [Русский](../ru/getting-started.md) | [中文](../zh/getting-started.md)

---

## 1. Installation

### Requirements
- **Go**: Version 1.22 or higher.
- **Operating System**: Linux (recommended for native `mmap`), macOS, or Windows.

### Building from Source

```bash
# Clone the repository
git clone https://github.com/horreum/horreum.git
cd horreum

# Compile binary
go build -o horreum ./cmd/horreum

# Verify installation
./horreum --version
```

---

## 2. Running Horreum

### Running with Default Configuration

By default, running Horreum brings up the cache with 4 shards, a 256 MiB memory region per shard, and listening on TCP port `:7373`:

```bash
./horreum --config config.yaml
```

### Running in Persistent Mode

To enable Write-Ahead Logging (WAL) and disk durability:

```bash
./horreum --config config.yaml --persistent-path=/var/lib/horreum --durable=true
```

### Running via Docker

```bash
# Build the container image
docker build -t horreum:latest .

# Run container in background
docker run -d \
  -p 7373:7373 \
  -p 9090:9090 \
  -v $(pwd)/config.yaml:/etc/horreum/config.yaml \
  --name horreum-server \
  horreum:latest
```

---

## 3. Connecting a Client

Horreum communicates using a binary wire protocol over TCP port `7373`.

### Python Example

```python
import socket
import struct

# Header: magic(0x4848) + op(SET=2) + flags(0) + keyLen + valLen
key = b"user:100"
val = b"John Doe"
header = struct.pack('<H B B H I', 0x4848, 2, 0, len(key), len(val))

sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
sock.connect(('127.0.0.1', 7373))
sock.sendall(header + key + val)

# Read response (10 bytes header)
res_header = sock.recv(10)
magic, op, flags, klen, vlen = struct.unpack('<H B B H I', res_header)
print("SET response status:", "Success" if flags == 0 else "Error")
sock.close()
```

For complete client implementations, check [`examples/client.py`](file:///e:/pet/horreum/examples/client.py), [`examples/client.js`](file:///e:/pet/horreum/examples/client.js), and [`examples/client.go`](file:///e:/pet/horreum/examples/client.go).
