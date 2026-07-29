import socket
import struct
import sys

class HorreumClient:
    def __init__(self, host='127.0.0.1', port=7373):
        self.host = host
        self.port = port
        self.sock = None

    def connect(self):
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self.sock.connect((self.host, self.port))

    def close(self):
        if self.sock:
            self.sock.close()

    def _send_request(self, op, key, value=b''):
        key_bytes = key.encode('utf-8') if isinstance(key, str) else key
        val_bytes = value.encode('utf-8') if isinstance(value, str) else value
        
        # Header: magic(2B) + op(1B) + flags(1B) + keyLen(2B) + valLen(4B)
        header = struct.pack('<H B B H I', 0x4848, op, 0, len(key_bytes), len(val_bytes))
        self.sock.sendall(header + key_bytes + val_bytes)

    def _read_response(self):
        header = self.sock.recv(10)
        if len(header) < 10:
            raise ConnectionError("Short header received")
        
        magic, op, flags, key_len, val_len = struct.unpack('<H B B H I', header)
        if magic != 0x4848:
            raise ValueError("Bad magic signature")
        
        body = b""
        total_len = key_len + val_len
        while len(body) < total_len:
            chunk = self.sock.recv(total_len - len(body))
            if not chunk:
                raise ConnectionError("Connection closed prematurely")
            body += chunk
            
        key = body[:key_len]
        val = body[key_len:]
        
        is_error = (flags & 0x01) == 0x01
        return key, val, is_error

    def set(self, key, value):
        self._send_request(2, key, value)
        _, _, is_error = self._read_response()
        return not is_error

    def get(self, key):
        self._send_request(1, key)
        _, val, is_error = self._read_response()
        if is_error:
            return None
        return val

    def delete(self, key):
        self._send_request(3, key)
        _, _, is_error = self._read_response()
        return not is_error

if __name__ == '__main__':
    client = HorreumClient('127.0.0.1', 7373)
    try:
        client.connect()
        print("Connected to Horreum")
    except Exception as e:
        print(f"Failed to connect: {e}")
        sys.exit(1)
    
    # 1. SET
    success = client.set("python_test", "hello_from_python_client")
    print(f"SET python_test: {success}")
    
    # 2. GET
    val = client.get("python_test")
    print(f"GET python_test: {val.decode('utf-8') if val else 'Not Found'}")
    
    # 3. DELETE
    deleted = client.delete("python_test")
    print(f"DELETE python_test: {deleted}")
    
    # 4. GET after DELETE
    val_after = client.get("python_test")
    print(f"GET python_test (after delete): {val_after.decode('utf-8') if val_after else 'Not Found (OK)'}")
    
    client.close()
