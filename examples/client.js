const net = require('net');

class HorreumClient {
    constructor(host = '127.0.0.1', port = 7373) {
        this.host = host;
        this.port = port;
        this.socket = null;
    }

    connect() {
        return new Promise((resolve, reject) => {
            this.socket = net.connect({ host: this.host, port: this.port }, () => {
                resolve();
            });
            this.socket.on('error', reject);
        });
    }

    close() {
        if (this.socket) this.socket.end();
    }

    async set(key, value) {
        await this._send(2, key, value);
        const res = await this._read();
        return !res.isError;
    }

    async get(key) {
        await this._send(1, key);
        const res = await this._read();
        return res.isError ? null : res.value;
    }

    async delete(key) {
        await this._send(3, key);
        const res = await this._read();
        return !res.isError;
    }

    _send(op, key, value = '') {
        const keyBuf = Buffer.from(key);
        const valBuf = Buffer.from(value);
        const header = Buffer.alloc(10);
        
        header.writeUInt16LE(0x4848, 0);
        header.writeUInt8(op, 2);
        header.writeUInt8(0, 3);
        header.writeUInt16LE(keyBuf.length, 4);
        header.writeUInt32LE(valBuf.length, 6);

        return new Promise((resolve, reject) => {
            this.socket.write(Buffer.concat([header, keyBuf, valBuf]), (err) => {
                if (err) reject(err);
                else resolve();
            });
        });
    }

    _read() {
        return new Promise((resolve, reject) => {
            this.socket.once('data', (data) => {
                if (data.length < 10) {
                    resolve({ isError: true });
                    return;
                }
                const magic = data.readUInt16LE(0);
                const op = data.readUInt8(2);
                const flags = data.readUInt8(3);
                const keyLen = data.readUInt16LE(4);
                const valLen = data.readUInt32LE(6);
                
                if (magic !== 0x4848) {
                    reject(new Error("Bad magic signature"));
                    return;
                }
                
                const isError = (flags & 0x01) === 0x01;
                const value = data.slice(10 + keyLen, 10 + keyLen + valLen);
                resolve({ isError, value });
            });
        });
    }
}

async function main() {
    const client = new HorreumClient('127.0.0.1', 7373);
    try {
        await client.connect();
        console.log("Connected to Horreum");
    } catch (err) {
        console.error("Failed to connect:", err.message);
        process.exit(1);
    }
    
    // 1. SET
    const success = await client.set('js_test', 'hello_from_javascript_client');
    console.log(`SET js_test: ${success}`);
    
    // 2. GET
    const val = await client.get('js_test');
    console.log(`GET js_test: ${val ? val.toString() : 'Not Found'}`);
    
    // 3. DELETE
    const deleted = await client.delete('js_test');
    console.log(`DELETE js_test: ${deleted}`);
    
    // 4. GET after DELETE
    const valAfter = await client.get('js_test');
    console.log(`GET js_test (after delete): ${valAfter ? valAfter.toString() : 'Not Found (OK)'}`);
    
    client.close();
}

main().catch(console.error);
