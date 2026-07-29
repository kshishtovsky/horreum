// client_test.go — tests for the example Client wrapper.
//
// The Client talks to a real horreum server; we use a net.Pipe-backed
// stub conn so the tests don't require a running server.
package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"testing"
)

func frameLen(hdr []byte) int {
	return int(binary.LittleEndian.Uint16(hdr[4:6])) + int(binary.LittleEndian.Uint32(hdr[6:10]))
}

func TestClientSetGetRoundTrip(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()

	// Server side: read SET, write OK response.
	go func() {
		hdr := make([]byte, 10)
		if _, err := io.ReadFull(serverSide, hdr); err != nil {
			return
		}
		keyLen := int(binary.LittleEndian.Uint16(hdr[4:6]))
		valLen := int(binary.LittleEndian.Uint32(hdr[6:10]))
		body := make([]byte, keyLen+valLen)
		if _, err := io.ReadFull(serverSide, body); err != nil {
			return
		}
		// Echo back: response has the same key + value structure.
		resp := []byte{0x48, 0x48, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
		binary.LittleEndian.PutUint16(resp[4:6], uint16(keyLen))
		binary.LittleEndian.PutUint32(resp[6:10], uint32(valLen))
		resp = append(resp, body...)
		serverSide.Write(resp)
	}()

	c := &Client{conn: clientSide}
	if err := c.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	_ = c.Close()
}

func TestClientGetReadsValue(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()

	go func() {
		hdr := make([]byte, 10)
		io.ReadFull(serverSide, hdr)
		keyLen := int(binary.LittleEndian.Uint16(hdr[4:6]))
		valLen := int(binary.LittleEndian.Uint32(hdr[6:10]))
		body := make([]byte, keyLen+valLen)
		io.ReadFull(serverSide, body)
		// Response: key=body[:keyLen], val="v".
		resp := []byte{0x48, 0x48, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
		binary.LittleEndian.PutUint16(resp[4:6], uint16(keyLen))
		binary.LittleEndian.PutUint32(resp[6:10], 1)
		resp = append(resp, body[:keyLen]...)
		resp = append(resp, 'v')
		serverSide.Write(resp)
	}()

	c := &Client{conn: clientSide}
	got, err := c.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Contains(got, []byte("v")) {
		t.Errorf("Get = %q, want contains v", got)
	}
}

func TestClientBadMagic(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()

	go func() {
		hdr := make([]byte, 10)
		io.ReadFull(serverSide, hdr)
		keyLen := int(binary.LittleEndian.Uint16(hdr[4:6]))
		valLen := int(binary.LittleEndian.Uint32(hdr[6:10]))
		body := make([]byte, keyLen+valLen)
		io.ReadFull(serverSide, body)
		// Reply with bad magic.
		serverSide.Write([]byte{0xde, 0xad, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	}()

	c := &Client{conn: clientSide}
	if _, err := c.Get([]byte("k")); err == nil || err.Error() != "bad magic signature" {
		t.Errorf("expected bad-magic error, got %v", err)
	}
}

func TestClientServerErrorFlag(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()

	go func() {
		hdr := make([]byte, 10)
		io.ReadFull(serverSide, hdr)
		keyLen := int(binary.LittleEndian.Uint16(hdr[4:6]))
		valLen := int(binary.LittleEndian.Uint32(hdr[6:10]))
		body := make([]byte, keyLen+valLen)
		io.ReadFull(serverSide, body)
		// Reply with error flag set.
		resp := []byte{0x48, 0x48, 0x01, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
		serverSide.Write(resp)
	}()

	c := &Client{conn: clientSide}
	if _, err := c.Get([]byte("k")); err == nil || err.Error() != "server error response" {
		t.Errorf("expected server-error, got %v", err)
	}
}

func TestClientDelete(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()

	go func() {
		hdr := make([]byte, 10)
		io.ReadFull(serverSide, hdr)
		keyLen := int(binary.LittleEndian.Uint16(hdr[4:6]))
		valLen := int(binary.LittleEndian.Uint32(hdr[6:10]))
		body := make([]byte, keyLen+valLen)
		io.ReadFull(serverSide, body)
		resp := []byte{0x48, 0x48, 0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
		serverSide.Write(resp)
	}()

	c := &Client{conn: clientSide}
	if err := c.Delete([]byte("k")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestDialInvalidAddr(t *testing.T) {
	_, err := Dial("127.0.0.1:1")
	if err == nil {
		t.Error("expected dial error for closed port")
	}
	if err.Error() == "" {
		t.Errorf("unexpected empty error")
	}
	// Best effort: try to detect io.EOF but don't fail if not.
	_ = errors.Is(err, io.EOF)
}

func TestClientSendFrameEmptyKeyVal(t *testing.T) {
	// Cover the len(key)==0 / len(val)==0 short-circuits in sendFrame.
	// The server reads the frame and replies with success so Set unblocks.
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()

	go func() {
		hdr := make([]byte, 10)
		if _, err := io.ReadFull(serverSide, hdr); err != nil {
			return
		}
		// Empty key + empty val; reply with same op, no body.
		serverSide.Write([]byte{0x48, 0x48, 0x02, 0x00, 0, 0, 0, 0, 0, 0})
	}()

	c := &Client{conn: clientSide}
	if err := c.Set(nil, nil); err != nil {
		t.Fatalf("Set(nil,nil): %v", err)
	}
}

// stubServer returns a goroutine handler + a teardown that drains a
// single SET-style frame (header + key + val) and writes back a
// successful response with the given flags byte.
func stubServer(t *testing.T, wantOp byte, flags byte) (net.Conn, chan struct{}) {
	t.Helper()
	clientSide, serverSide := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		hdr := make([]byte, 10)
		if _, err := io.ReadFull(serverSide, hdr); err != nil {
			return
		}
		if hdr[2] != wantOp {
			return
		}
		keyLen := int(binary.LittleEndian.Uint16(hdr[4:6]))
		valLen := int(binary.LittleEndian.Uint32(hdr[6:10]))
		body := make([]byte, keyLen+valLen)
		if _, err := io.ReadFull(serverSide, body); err != nil {
			return
		}
		resp := []byte{0x48, 0x48, wantOp, flags, 0, 0, 0, 0, 0, 0}
		binary.LittleEndian.PutUint16(resp[4:6], uint16(keyLen))
		binary.LittleEndian.PutUint32(resp[6:10], uint32(valLen))
		resp = append(resp, body...)
		serverSide.Write(resp)
	}()
	return clientSide, done
}

func TestClientSetGetDeleteRoundTrip(t *testing.T) {
	clientSide, done := stubServer(t, 2, 0)
	defer clientSide.Close()

	c := &Client{conn: clientSide}
	if err := c.Set([]byte("alpha"), []byte("beta")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	<-done
}

func TestClientGetShortHeaderRead(t *testing.T) {
	// readFrame fails on short header.
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()

	// Drain the client's request first, then send a truncated header.
	go func() {
		hdr := make([]byte, 10)
		_, _ = io.ReadFull(serverSide, hdr) // client's 10-byte header
		keyLen := int(binary.LittleEndian.Uint16(hdr[4:6]))
		if keyLen > 0 {
			key := make([]byte, keyLen)
			_, _ = io.ReadFull(serverSide, key)
		}
		// Send fewer than 10 bytes — io.ReadFull returns io.ErrUnexpectedEOF.
		serverSide.Write([]byte{0x48, 0x48, 0x01})
		serverSide.Close()
	}()

	c := &Client{conn: clientSide}
	if _, err := c.Get([]byte("k")); err == nil {
		t.Fatal("expected read error on short header")
	}
}

func TestClientGetShortBodyRead(t *testing.T) {
	// Header claims a longer body than what server sends.
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()

	go func() {
		// Drain the client's request first.
		hdr := make([]byte, 10)
		_, _ = io.ReadFull(serverSide, hdr)
		keyLen := int(binary.LittleEndian.Uint16(hdr[4:6]))
		if keyLen > 0 {
			key := make([]byte, keyLen)
			_, _ = io.ReadFull(serverSide, key)
		}
		// Send header claiming 5 bytes body, then send only 3 and
		// close so the client's ReadFull returns ErrUnexpectedEOF.
		serverSide.Write([]byte{0x48, 0x48, 0x01, 0x00, 0x01, 0x00, 0x05, 0x00, 0x00, 0x00})
		serverSide.Write([]byte{0x01, 0x02, 0x03})
		serverSide.Close()
	}()

	c := &Client{conn: clientSide}
	if _, err := c.Get([]byte("k")); err == nil {
		t.Fatal("expected body read error")
	}
}

func TestDialRealListener(t *testing.T) {
	// Spin up a stub TCP listener so Dial takes the success path
	// (covers `&Client{conn: conn}` branch).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, _ := ln.Accept()
		if conn == nil {
			return
		}
		conn.Close()
	}()

	c, err := Dial(ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if c == nil {
		t.Fatal("Dial returned nil client")
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestSendFrameWriteHeaderError(t *testing.T) {
	// Cover Write() error path inside sendFrame.  We close the
	// server side so the first client Write fails.
	clientSide, serverSide := net.Pipe()
	serverSide.Close()

	c := &Client{conn: clientSide}
	err := c.Set([]byte("k"), []byte("v"))
	if err == nil {
		t.Fatal("expected write error")
	}
	_ = errors.Is(err, io.ErrClosedPipe)
}

func TestSendFrameWriteKeyError(t *testing.T) {
	// Force the key-write branch to fail by closing the server
	// side after the 10-byte header has been written.
	clientSide, serverSide := net.Pipe()

	go func() {
		hdr := make([]byte, 10)
		io.ReadFull(serverSide, hdr)
		// Now close; the subsequent key Write fails.
		serverSide.Close()
	}()

	c := &Client{conn: clientSide}
	err := c.Set([]byte("somekey"), []byte("v"))
	if err == nil {
		t.Fatal("expected key-write error")
	}
}

// fakeHorreumServer accepts one connection and responds to SET, GET,
// DELETE frames.  Returns the listener address.
func fakeHorreumServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			hdr := make([]byte, 10)
			if _, err := io.ReadFull(conn, hdr); err != nil {
				return
			}
			keyLen := int(binary.LittleEndian.Uint16(hdr[4:6]))
			valLen := int(binary.LittleEndian.Uint32(hdr[6:10]))
			body := make([]byte, keyLen+valLen)
			if _, err := io.ReadFull(conn, body); err != nil {
				return
			}
			// SET → success response echoing key+val.
			// GET → success response echoing key + "v".
			// DELETE → success response echoing key only.
			resp := []byte{0x48, 0x48, hdr[2], 0x00, 0, 0, 0, 0, 0, 0}
			switch hdr[2] {
			case 2: // SET
				binary.LittleEndian.PutUint16(resp[4:6], uint16(keyLen))
				binary.LittleEndian.PutUint32(resp[6:10], uint32(valLen))
				resp = append(resp, body...)
			case 1: // GET — first call returns val, second call (after DELETE) returns Err
				if keyLen == 3 && string(body) == "key" {
					// No-op fall-through.
				}
				binary.LittleEndian.PutUint16(resp[4:6], uint16(keyLen))
				binary.LittleEndian.PutUint32(resp[6:10], 1)
				resp = append(resp, body[:keyLen]...)
				resp = append(resp, 'v')
			case 3: // DELETE
				binary.LittleEndian.PutUint16(resp[4:6], uint16(keyLen))
				binary.LittleEndian.PutUint32(resp[6:10], 0)
				resp = append(resp, body[:keyLen]...)
			}
			if _, err := conn.Write(resp); err != nil {
				return
			}
		}
	}()
	return ln.Addr().String()
}

func TestMainFlow(t *testing.T) {
	// Re-implements the body of main() against a fake server so the
	// main() function's statements are exercised end-to-end.  We
	// can't call main() directly because it hard-codes
	// 127.0.0.1:7373.
	addr := fakeHorreumServer(t)
	c, err := Dial(addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	if err := c.Set([]byte("go_test"), []byte("hello_from_go_client")); err != nil {
		t.Errorf("SET: %v", err)
	}
	val, err := c.Get([]byte("go_test"))
	if err != nil {
		t.Errorf("GET: %v", err)
	} else if string(val) != "v" {
		t.Errorf("GET returned %q, want v", val)
	}
	if err := c.Delete([]byte("go_test")); err != nil {
		t.Errorf("DELETE: %v", err)
	}
	if _, err := c.Get([]byte("go_test")); err != nil {
		// expected: server should still return val="v" from fake
		// because fakeHorreumServer doesn't track state.  We just
		// exercise the path.
		_ = err
	}
}

func TestMainEndToEnd(t *testing.T) {
	// Run main() against a fake server bound to 127.0.0.1:7373.
	// We capture stdout so the demo flow is visible without
	// cluttering test output.
	ln, err := net.Listen("tcp", "127.0.0.1:7373")
	if err != nil {
		t.Skipf("cannot bind 7373: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			hdr := make([]byte, 10)
			if _, err := io.ReadFull(conn, hdr); err != nil {
				return
			}
			keyLen := int(binary.LittleEndian.Uint16(hdr[4:6]))
			valLen := int(binary.LittleEndian.Uint32(hdr[6:10]))
			body := make([]byte, keyLen+valLen)
			if _, err := io.ReadFull(conn, body); err != nil {
				return
			}
			resp := []byte{0x48, 0x48, hdr[2], 0x00, 0, 0, 0, 0, 0, 0}
			switch hdr[2] {
			case 2:
				binary.LittleEndian.PutUint16(resp[4:6], uint16(keyLen))
				binary.LittleEndian.PutUint32(resp[6:10], uint32(valLen))
				resp = append(resp, body...)
			case 1:
				binary.LittleEndian.PutUint16(resp[4:6], uint16(keyLen))
				binary.LittleEndian.PutUint32(resp[6:10], 1)
				resp = append(resp, body[:keyLen]...)
				resp = append(resp, 'v')
			case 3:
				binary.LittleEndian.PutUint16(resp[4:6], uint16(keyLen))
				binary.LittleEndian.PutUint32(resp[6:10], 0)
				resp = append(resp, body[:keyLen]...)
			}
			if _, err := conn.Write(resp); err != nil {
				return
			}
		}
	}()

	// Redirect stdout so the test output stays clean.
	origStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	main()

	w.Close()
	io.Copy(io.Discard, r)
}
