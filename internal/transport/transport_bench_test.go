// transport_bench_test.go — throughput and latency benchmarks.
//
// Benchmarks spawn a real server (TCP / QUIC), prefill the cache
// through the wire, then drive GET/SET round trips from a single
// client goroutine.  Per-op latency is reported via b.ReportMetric so
// p50/p99 distribution is visible in the bench output.
package transport_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"net"
	"sort"
	"testing"
	"time"

	qerr "github.com/quic-go/quic-go"

	"github.com/kshishtovsky/horreum/internal/proto"
	"github.com/kshishtovsky/horreum/internal/transport"
	tpquic "github.com/kshishtovsky/horreum/internal/transport/quic"
)

// makeKey returns a deterministic 16-byte key for op i.
func makeKey(i int) []byte {
	b := make([]byte, 16)
	b[0] = byte(i >> 24)
	b[1] = byte(i >> 16)
	b[2] = byte(i >> 8)
	b[3] = byte(i)
	return b
}

// setupTCPServer boots a TCP server with the given shard config and
// returns its address.  Cleanup is registered via t.Cleanup.
func setupTCPServer(tb testing.TB, numShards int) string {
	tb.Helper()
	s, err := transport.NewServer(transport.Options{
		Addr:          "127.0.0.1:0",
		TransportName: "tcp",
		ShardConfig: transport.ShardConfig{
			NumShards:     numShards,
			RegionSize:    16 << 20,
			EvictCapacity: 4096,
		},
	})
	if err != nil {
		tb.Fatalf("NewServer(tcp): %v", err)
	}
	go func() { _ = s.ListenAndServe() }()
	tb.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = s.Shutdown(ctx)
		cancel()
	})
	return s.Addr().String()
}

// setupQUICServer boots a QUIC server with an ephemeral self-signed
// certificate.  Cleanup is registered via t.Cleanup.
func setupQUICServer(tb testing.TB, numShards int) (string, *tls.Config) {
	tb.Helper()
	cert := generateTestCert(tb)
	tpquic.RegisterCertificate("bench-cert", cert)
	s, err := transport.NewServer(transport.Options{
		Addr:          "127.0.0.1:0",
		TransportName: "quic",
		TLSCertFile:   "bench-cert",
		ShardConfig: transport.ShardConfig{
			NumShards:     numShards,
			RegionSize:    16 << 20,
			EvictCapacity: 4096,
		},
	})
	if err != nil {
		tb.Fatalf("NewServer(quic): %v", err)
	}
	go func() { _ = s.ListenAndServe() }()
	tb.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = s.Shutdown(ctx)
		cancel()
	})
	tlsConf := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"horreum/1"},
	}
	return s.Addr().String(), tlsConf
}

// prefillTCP sets numKeys keys with the given value via raw TCP.
func prefillTCP(tb testing.TB, addr string, numKeys int, value []byte) {
	tb.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		tb.Fatalf("prefill dial: %v", err)
	}
	defer conn.Close()
	for i := 0; i < numKeys; i++ {
		key := makeKey(i)
		sendFrame(tb, conn, proto.OpSet, key, value)
		if !expectFrame(tb, conn, proto.OpSet, key, nil) {
			tb.FailNow()
		}
	}
}

// ───────────────────── TCP ─────────────────────

// BenchmarkTCPSet measures throughput and p99 latency of SET on the
// TCP transport with a 1 KiB payload.
func BenchmarkTCPSet(b *testing.B) {
	addr := setupTCPServer(b, 4)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		b.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	value := bytes.Repeat([]byte("v"), 1024)
	latencies := make([]time.Duration, 0, b.N)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := makeKey(i)
		t0 := time.Now()
		sendFrame(b, conn, proto.OpSet, key, value)
		if !expectFrame(b, conn, proto.OpSet, key, nil) {
			b.FailNow()
		}
		latencies = append(latencies, time.Since(t0))
	}
	b.StopTimer()
	reportLatencies(b, latencies)
}

// BenchmarkTCPGet measures throughput and p99 latency of GET on the
// TCP transport against a pre-populated cache.  SET and GET run on
// the same connection so the connection-pinning invariant guarantees
// they land on the same shard.
func BenchmarkTCPGet(b *testing.B) {
	addr := setupTCPServer(b, 4)
	const numKeys = 4096
	value := bytes.Repeat([]byte("v"), 1024)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		b.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Prefill via the same connection.
	for i := 0; i < numKeys; i++ {
		key := makeKey(i)
		sendFrame(b, conn, proto.OpSet, key, value)
		if !expectFrame(b, conn, proto.OpSet, key, nil) {
			b.FailNow()
		}
	}

	latencies := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := makeKey(i % numKeys)
		t0 := time.Now()
		sendFrame(b, conn, proto.OpGet, key, nil)
		resp := readFrame(b, conn)
		if !bytes.Equal(resp.Value, value) {
			b.Fatalf("value mismatch (key %d)", i)
		}
		latencies = append(latencies, time.Since(t0))
	}
	b.StopTimer()
	reportLatencies(b, latencies)
}

// ───────────────────── QUIC ─────────────────────

// BenchmarkQUICGet measures throughput and p99 latency of GET on the
// QUIC transport.
func BenchmarkQUICGet(b *testing.B) {
	addr, tlsConf := setupQUICServer(b, 4)
	const numKeys = 4096
	value := bytes.Repeat([]byte("v"), 1024)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	qConn, err := qerr.DialAddr(ctx, addr, tlsConf, &qerr.Config{})
	if err != nil {
		b.Fatalf("quic.DialAddr: %v", err)
	}
	defer qConn.CloseWithError(0, "")

	// Prefill via QUIC stream per key.
	for i := 0; i < numKeys; i++ {
		stream, err := qConn.OpenStreamSync(ctx)
		if err != nil {
			b.Fatalf("prefill OpenStream: %v", err)
		}
		key := makeKey(i)
		buf := proto.EncodeRequest(nil, proto.OpSet, key, value)
		if _, err := stream.Write(buf); err != nil {
			b.Fatalf("prefill Write: %v", err)
		}
		parser := proto.NewParser()
		var chunk [4096]byte
		n, err := stream.Read(chunk[:])
		if err != nil {
			b.Fatalf("prefill Read: %v", err)
		}
		frames, err := parser.Feed(chunk[:n])
		if err != nil || len(frames) == 0 {
			b.Fatalf("prefill parse: %v", err)
		}
		_ = stream.Close()
	}

	latencies := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stream, err := qConn.OpenStreamSync(ctx)
		if err != nil {
			b.Fatalf("OpenStream: %v", err)
		}
		key := makeKey(i % numKeys)
		buf := proto.EncodeRequest(nil, proto.OpGet, key, nil)
		t0 := time.Now()
		if _, err := stream.Write(buf); err != nil {
			b.Fatalf("Write: %v", err)
		}
		parser := proto.NewParser()
		var chunk [4096]byte
		n, err := stream.Read(chunk[:])
		if err != nil {
			b.Fatalf("Read: %v", err)
		}
		frames, err := parser.Feed(chunk[:n])
		if err != nil || len(frames) == 0 {
			b.Fatalf("parse: %v", err)
		}
		if !bytes.Equal(frames[0].Value, value) {
			b.Fatalf("value mismatch")
		}
		latencies = append(latencies, time.Since(t0))
		_ = stream.Close()
	}
	b.StopTimer()
	reportLatencies(b, latencies)
}

// ───────────────────── helpers ─────────────────────

// reportLatencies computes p50/p95/p99 and reports them as bench
// metrics.
func reportLatencies(b *testing.B, lats []time.Duration) {
	if len(lats) == 0 {
		return
	}
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	pick := func(p float64) time.Duration {
		idx := int(float64(len(lats)) * p)
		if idx >= len(lats) {
			idx = len(lats) - 1
		}
		return lats[idx]
	}
	b.ReportMetric(float64(pick(0.50).Nanoseconds()), "p50-ns/op")
	b.ReportMetric(float64(pick(0.95).Nanoseconds()), "p95-ns/op")
	b.ReportMetric(float64(pick(0.99).Nanoseconds()), "p99-ns/op")
}
