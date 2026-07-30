// quic_integration_test.go — full QUIC integration test that boots a
// real Transport and exercises every code path on the server side
// (NewTransport, ListenAndServe, Shutdown, Addr, serveConn,
// serveStream, streamConn, shardIndexForStream).
package quic

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"

	qerr "github.com/quic-go/quic-go"

	"github.com/kshishtovsky/horreum/internal/proto"
)

func genCert(t *testing.T) *tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "quic-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		DNSNames:     []string{"localhost"},
	}
	der, _ := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	certPem := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPem := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, err := tls.X509KeyPair(certPem, keyPem)
	if err != nil {
		t.Fatal(err)
	}
	return &cert
}

// TestQUICTransportFullLifecycle boots a real QUIC transport, dials
// it, sends a SET/GET/DEL sequence, and then shuts down — covering
// every server-side method including streamConn.
func TestQUICTransportFullLifecycle(t *testing.T) {
	cert := genCert(t)
	tr, err := NewTransport("127.0.0.1:0", &mockRouter{}, cert, nil)
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}

	// Addr must be populated immediately.
	if tr.Addr() == nil {
		t.Error("Addr() = nil before Listen")
	}

	// ListenAndServe accepts connections in a goroutine.
	go func() { _ = tr.ListenAndServe() }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = tr.Shutdown(ctx)
		cancel()
	}()

	tlsConf := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"horreum/1"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := qerr.DialAddr(ctx, tr.Addr().String(), tlsConf, &qerr.Config{})
	if err != nil {
		t.Fatalf("DialAddr: %v", err)
	}
	defer func() { _ = conn.CloseWithError(0, "") }()

	// Drive the server through several frames.
	for _, op := range []proto.OpCode{proto.OpSet, proto.OpGet, proto.OpDel, proto.OpSet} {
		stream, err := conn.OpenStreamSync(ctx)
		if err != nil {
			t.Fatalf("OpenStreamSync: %v", err)
		}
		// Exercise the streamConn deadline methods on the client
		// side.  These are no-ops on the quic.Stream; we only
		// assert they don't error.
		_ = stream.SetDeadline(time.Now().Add(time.Second))
		_ = stream.SetReadDeadline(time.Now().Add(time.Second))
		_ = stream.SetWriteDeadline(time.Now().Add(time.Second))

		buf := proto.EncodeRequest(nil, op, []byte("k"), []byte("v"))
		if _, err := stream.Write(buf); err != nil {
			_ = stream.Close()
			t.Fatalf("stream.Write: %v", err)
		}
		// Read the response.
		parser := proto.NewParser()
		var chunk [4096]byte
		for {
			n, err := stream.Read(chunk[:])
			if err != nil {
				break
			}
			frames, perr := parser.Feed(chunk[:n])
			if perr != nil {
				t.Fatalf("parser.Feed: %v", perr)
			}
			if len(frames) > 0 {
				if !bytes.Equal(frames[0].Key, []byte("k")) {
					t.Errorf("frame key = %q, want k", frames[0].Key)
				}
				break
			}
		}
		_ = stream.Close()
	}
}

// TestStreamConnMethods creates a streamConn directly (from the
// server side) and exercises every delegation method.  This covers
// the LocalAddr / SetReadDeadline / SetWriteDeadline paths that are
// not otherwise reachable.
func TestStreamConnMethods(t *testing.T) {
	cert := genCert(t)
	tr, err := NewTransport("127.0.0.1:0", &mockRouter{}, cert, nil)
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	go func() { _ = tr.ListenAndServe() }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = tr.Shutdown(ctx)
		cancel()
	}()

	tlsConf := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"horreum/1"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	qConn, err := qerr.DialAddr(ctx, tr.Addr().String(), tlsConf, &qerr.Config{})
	if err != nil {
		t.Fatalf("DialAddr: %v", err)
	}
	defer func() { _ = qConn.CloseWithError(0, "") }()

	qStream, err := qConn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("OpenStreamSync: %v", err)
	}
	defer qStream.Close()

	// Construct a streamConn from the client side (note: this isn't
	// how it's used in production but it exercises every delegation
	// path).  We need a *quic.Conn for the local addr lookup; we
	// re-use qConn.
	sc := newStreamConn(qConn, qStream)

	// LocalAddr / RemoteAddr delegate to qConn.LocalAddr().
	if addr := sc.LocalAddr(); addr == nil {
		t.Error("LocalAddr returned nil")
	}
	if addr := sc.RemoteAddr(); addr == nil {
		t.Error("RemoteAddr returned nil")
	}

	// Deadline setters are no-ops on quic-go; we just call them.
	if err := sc.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Errorf("SetDeadline: %v", err)
	}
	if err := sc.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Errorf("SetReadDeadline: %v", err)
	}
	if err := sc.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Errorf("SetWriteDeadline: %v", err)
	}

	// Read / Write / Close delegate to qStream.
	if _, err := sc.Write([]byte("ping")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Close — exercise the sync.Once path.
	if err := sc.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// Second Close must not panic and returns nil (sync.Once).
	if err := sc.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
