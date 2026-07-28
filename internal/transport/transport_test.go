// transport_test.go — end-to-end transport tests.
//
// Tests boot a real Server on 127.0.0.1:<random>, dial it with raw
// net.Conn (TCP) or quic-go (QUIC), and exercise SET/GET/DEL round
// trips.  All tests use the in-process shard set built by
// transport.NewServer.
package transport_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	qerr "github.com/quic-go/quic-go"

	"github.com/horreum/horreum/internal/proto"
	"github.com/horreum/horreum/internal/transport"
	tpquic "github.com/horreum/horreum/internal/transport/quic"
	tptransport "github.com/horreum/horreum/internal/transport/tcp"
)

func mustListen(t *testing.T, name string) *transport.Server {
	t.Helper()
	s, err := transport.NewServer(transport.Options{
		Addr:          "127.0.0.1:0",
		TransportName: name,
		ShardConfig: transport.ShardConfig{
			NumShards:     4,
			RegionSize:    16 << 20,
			EvictCapacity: 1024,
		},
	})
	if err != nil {
		t.Fatalf("NewServer(%s): %v", name, err)
	}
	return s
}

// ───────────────────────── TCP ─────────────────────────

func TestTCPSetGetRoundTrip(t *testing.T) {
	srv := mustListen(t, "tcp")
	go func() { _ = srv.ListenAndServe() }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
	}()

	conn, err := net.Dial("tcp", srv.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	sendFrame(t, conn, proto.OpSet, []byte("alpha"), []byte("value-A"))
	if !expectFrame(t, conn, proto.OpSet, []byte("alpha"), nil) {
		t.FailNow()
	}

	sendFrame(t, conn, proto.OpGet, []byte("alpha"), nil)
	resp := readFrame(t, conn)
	if !bytes.Equal(resp.Value, []byte("value-A")) {
		t.Errorf("got %q, want value-A", resp.Value)
	}

	sendFrame(t, conn, proto.OpDel, []byte("alpha"), nil)
	if !expectFrame(t, conn, proto.OpDel, []byte("alpha"), nil) {
		t.FailNow()
	}
	sendFrame(t, conn, proto.OpGet, []byte("alpha"), nil)
	resp = readFrame(t, conn)
	if !resp.IsError() {
		t.Errorf("expected error flag after delete+get")
	}
}

func TestTCPGracefulShutdown(t *testing.T) {
	srv := mustListen(t, "tcp")
	go func() { _ = srv.ListenAndServe() }()
	addr := srv.Addr().String()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	if c2, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		_ = c2.Close()
		t.Errorf("dial succeeded after Shutdown")
	}
}

func TestTCPConcurrent(t *testing.T) {
	srv := mustListen(t, "tcp")
	go func() { _ = srv.ListenAndServe() }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
	}()
	addr := srv.Addr().String()

	const clients = 32
	const opsPerClient = 200
	done := make(chan error, clients)
	for i := 0; i < clients; i++ {
		go func(id int) {
			conn, err := net.Dial("tcp", addr)
			if err != nil {
				done <- err
				return
			}
			defer conn.Close()
			for j := 0; j < opsPerClient; j++ {
				key := []byte{byte(id), byte(j >> 8), byte(j & 0xff)}
				sendFrame(t, conn, proto.OpSet, key, []byte("v"))
				if !expectFrame(t, conn, proto.OpSet, key, nil) {
					done <- nil
					return
				}
				sendFrame(t, conn, proto.OpGet, key, nil)
				resp := readFrame(t, conn)
				if !bytes.Equal(resp.Value, []byte("v")) {
					done <- nil
					return
				}
			}
			done <- nil
		}(i)
	}
	for i := 0; i < clients; i++ {
		if err := <-done; err != nil {
			t.Errorf("client err: %v", err)
		}
	}
}

// ───────────────────────── QUIC ─────────────────────────

func TestQUICSetGetRoundTrip(t *testing.T) {
	cert := generateTestCert(t)
	tpquic.RegisterCertificate("test-cert", cert)

	srv, err := transport.NewServer(transport.Options{
		Addr:          "127.0.0.1:0",
		TransportName: "quic",
		TLSCertFile:   "test-cert",
		ShardConfig: transport.ShardConfig{
			NumShards:     4,
			RegionSize:    16 << 20,
			EvictCapacity: 1024,
		},
	})
	if err != nil {
		t.Fatalf("NewServer(quic): %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
	}()
	go func() { _ = srv.ListenAndServe() }()

	tlsConf := &tls.Config{
		InsecureSkipVerify: true, // self-signed for tests only
		NextProtos:         []string{"horreum/1"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	qConn, err := qerr.DialAddr(ctx, srv.Addr().String(), tlsConf, &qerr.Config{})
	if err != nil {
		t.Fatalf("quic.DialAddr: %v", err)
	}
	defer func() { _ = qConn.CloseWithError(0, "") }()

	stream, err := qConn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("OpenStreamSync: %v", err)
	}
	defer stream.Close()

	buf := make([]byte, 0, 64)
	buf = proto.EncodeRequest(buf, proto.OpSet, []byte("k"), []byte("v"))
	if _, err := stream.Write(buf); err != nil {
		t.Fatalf("stream.Write: %v", err)
	}

	parser := proto.NewParser()
	var chunk [4096]byte
	n, err := stream.Read(chunk[:])
	if err != nil {
		t.Fatalf("stream.Read: %v", err)
	}
	frames, err := parser.Feed(chunk[:n])
	if err != nil {
		t.Fatalf("parser.Feed: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}
	if frames[0].Op != proto.OpSet || !bytes.Equal(frames[0].Key, []byte("k")) {
		t.Errorf("unexpected frame: %+v", frames[0])
	}
}

// ───────────────────────── helpers ─────────────────────────

func sendFrame(tb testing.TB, w io.Writer, op proto.OpCode, key, value []byte) {
	tb.Helper()
	buf := make([]byte, 0, 64)
	buf = proto.EncodeRequest(buf, op, key, value)
	if _, err := w.Write(buf); err != nil {
		tb.Fatalf("write: %v", err)
	}
}

func readFrame(tb testing.TB, r io.Reader) proto.Frame {
	tb.Helper()
	parser := proto.NewParser()
	var chunk [4096]byte
	for {
		n, err := r.Read(chunk[:])
		if err != nil {
			tb.Fatalf("read: %v", err)
		}
		if n == 0 {
			tb.Fatal("EOF before frame complete")
		}
		frames, perr := parser.Feed(chunk[:n])
		if perr != nil {
			tb.Fatalf("parse: %v", perr)
		}
		if len(frames) > 0 {
			return frames[0]
		}
	}
}

func expectFrame(tb testing.TB, r io.Reader, op proto.OpCode, key []byte, value []byte) bool {
	tb.Helper()
	f := readFrame(tb, r)
	if f.Op != op {
		tb.Errorf("op = %s, want %s", f.Op, op)
		return false
	}
	if !bytes.Equal(f.Key, key) {
		tb.Errorf("key = %q, want %q", f.Key, key)
		return false
	}
	if value != nil && !bytes.Equal(f.Value, value) {
		tb.Errorf("value = %q, want %q", f.Value, value)
		return false
	}
	return true
}

func generateTestCert(tb testing.TB) *tls.Certificate {
	tb.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		tb.Fatalf("rsa.GenerateKey: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "horreum-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		tb.Fatalf("CreateCertificate: %v", err)
	}
	certPem := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPem := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, err := tls.X509KeyPair(certPem, keyPem)
	if err != nil {
		tb.Fatalf("X509KeyPair: %v", err)
	}
	return &cert
}

// Compile-time import sanity.
var _ = tptransport.NewTransport
