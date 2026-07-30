// Package quic implements the QUIC transport.  Each accepted QUIC
// connection multiplexes many streams; each stream carries exactly
// one request/response exchange.  Streams are routed through the
// shared shard worker pool so cache operations remain pinned to a
// single goroutine per shard.
//
// QUIC mandates TLS 1.3; the caller must supply a TLS certificate
// via the in-memory registry (quic.RegisterCertificate) or by
// constructing Transport directly.
package quic

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/kshishtovsky/horreum/internal/logger"
	"github.com/kshishtovsky/horreum/internal/proto"
	"github.com/kshishtovsky/horreum/internal/transport"
	"github.com/kshishtovsky/horreum/internal/transport/api"
)

// noopRecorder is used when api.Options.Recorder is nil.
type noopRecorder struct{}

func (noopRecorder) ObserveSet(string, time.Duration) {}
func (noopRecorder) ObserveGet(string, time.Duration) {}
func (noopRecorder) ObserveDel(string, time.Duration) {}
func (noopRecorder) ObserveCAS(string, time.Duration) {}
func (noopRecorder) ObserveIncr(string, time.Duration) {}
func (noopRecorder) ObserveScan(string, time.Duration) {}
func (noopRecorder) ObserveDelPrefix(string, time.Duration) {}

// Transport is the QUIC-specific transport.
type Transport struct {
	listener *quic.Listener
	router   api.ShardRouter
	workers  *transport.WorkerPool
	recorder api.Recorder
	stop     chan struct{}
	closed   atomic.Bool
}

var _ api.Transport = (*Transport)(nil)

// NewTransport creates a QUIC listener.  cert is required.
//
// recorder may be nil; in that case a noop recorder is used.
func NewTransport(addr string, router api.ShardRouter, cert *tls.Certificate, recorder api.Recorder) (*Transport, error) {
	if router == nil {
		return nil, api.ErrNoBackend
	}
	if cert == nil {
		return nil, errors.New("transport/quic: TLS certificate is required")
	}
	if recorder == nil {
		recorder = noopRecorder{}
	}
	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{*cert},
		NextProtos:   []string{"horreum/1"},
		MinVersion:   tls.VersionTLS13,
	}
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, err
	}
	ln, err := quic.Listen(udpConn, tlsConf, &quic.Config{
		MaxIncomingStreams:    1024,
		MaxIncomingUniStreams: 1024,
		KeepAlivePeriod:       30 * time.Second,
	})
	if err != nil {
		_ = udpConn.Close()
		return nil, err
	}
	numShards := router.ShardCount()
	if numShards <= 0 {
		numShards = 8
	}
	t := &Transport{
		listener: ln,
		router:   router,
		recorder: recorder,
		stop:     make(chan struct{}),
		workers:  transport.NewWorkerPool(numShards, 4096),
	}
	t.workers.Start()
	if ss, ok := router.(*transport.ShardSet); ok {
		ss.StartGCLoop(t.workers, 100)
	}
	return t, nil
}

// Addr implements api.Transport.
func (t *Transport) Addr() net.Addr { return t.listener.Addr() }

// ListenAndServe accepts connections and serves streams.
func (t *Transport) ListenAndServe() error {
	for {
		conn, err := t.listener.Accept(context.Background())
		if err != nil {
			if t.closed.Load() {
				return nil
			}
			var nerr net.Error
			if errors.As(err, &nerr) && nerr.Timeout() {
				continue
			}
			return err
		}
		go t.serveConn(conn)
	}
}

// serveConn accepts streams until the client closes the connection.
// Each accepted stream is wrapped as a streamConn (net.Conn adapter)
// and dispatched to the shard worker pool.
func (t *Transport) serveConn(conn *quic.Conn) {
	remoteAddr := conn.RemoteAddr().String()
	slog.Debug("quic connection accepted", "remote_addr", remoteAddr)
	defer func() {
		slog.Debug("quic connection closed", "remote_addr", remoteAddr)
	}()

	for {
		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			return
		}
		sc := newStreamConn(conn, stream)
		// Route by stream index modulo shard count — streams on the
		// same QUIC connection may touch different shards (their
		// keys may differ), but ordering per shard is preserved.
		idx := shardIndexForStream(stream, t.workers.ShardCount())
		go t.serveStream(sc, idx)
	}
}

// serveStream processes one request/response on one stream. Network IO runs here,
// while cache operations are submitted to the shard worker for serial execution.
func (t *Transport) serveStream(c net.Conn, workerIdx int) {
	remoteAddr := c.RemoteAddr().String()
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(idleTimeoutQUIC))

	parser := proto.NewParser()
	writeBuf := make([]byte, 0, initialWriteBufCapQUIC)
	var chunk [readChunkSizeQUIC]byte

	for {
		n, err := c.Read(chunk[:])
		if err != nil {
			if err == io.EOF || errors.Is(err, net.ErrClosed) {
				return
			}
			var nerr net.Error
			if errors.As(err, &nerr) && nerr.Timeout() {
				return
			}
			if logger.NetErrorLimiter.Allow() {
				slog.Warn("quic stream read error", "remote_addr", remoteAddr, "error", err)
			}
			return
		}
		if n == 0 {
			return
		}
		frames, err := parser.Feed(chunk[:n])
		if err != nil {
			if logger.NetErrorLimiter.Allow() {
				slog.Warn("quic stream protocol parsing error", "remote_addr", remoteAddr, "error", err)
			}
			return
		}
		if len(frames) > 0 {
			done := make(chan struct{})
			var processErr bool

			submitted := t.workers.Submit(transport.Job{
				Handle: func(canceled bool) {
					if canceled {
						processErr = true
					} else {
						for i := range frames {
							if !handleFrameQUIC(t.router, &frames[i], &writeBuf, t.recorder, workerIdx) {
								processErr = true
								break
							}
						}
					}
					close(done)
				},
			}, workerIdx)

			if !submitted {
				return
			}
			<-done

			if processErr {
				return
			}

			if len(writeBuf) > 0 {
				if _, err := c.Write(writeBuf); err != nil {
					return
				}
				writeBuf = writeBuf[:0]
			}
		}
		_ = c.SetDeadline(time.Now().Add(idleTimeoutQUIC))
	}
}

// Shutdown closes the QUIC listener and waits for workers.
func (t *Transport) Shutdown(ctx context.Context) error {
	if !t.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(t.stop)
	_ = t.listener.Close()
	t.workers.Shutdown(ctx)
	return nil
}

// shardIndexForStream picks a worker index for the given stream.
func shardIndexForStream(s *quic.Stream, numShards int) int {
	if numShards <= 0 {
		return 0
	}
	const (
		offset = 14695981039346656037
		prime  = 1099511628211
	)
	h := uint64(offset)
	id := s.StreamID()
	for id > 0 {
		h ^= uint64(id & 0xff)
		h *= prime
		id >>= 8
	}
	return int(h % uint64(numShards))
}

const (
	readChunkSizeQUIC      = 4096
	initialWriteBufCapQUIC = 1024
	idleTimeoutQUIC        = 2 * time.Minute
)

// handleFrameQUIC is the per-frame handler.
func handleFrameQUIC(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, workerIdx int) bool {
	return transport.HandleFrame(router, fr, writeBuf, rec, workerIdx)
}


// ─────────── streamConn: net.Conn adapter for *quic.Stream ───────────

// streamConn adapts *quic.Stream to the net.Conn interface so it can
// flow through the shared shard worker pool's Job.Conn field.
type streamConn struct {
	conn   *quic.Conn
	stream *quic.Stream
	once   sync.Once
	closed atomic.Bool
}

func newStreamConn(c *quic.Conn, s *quic.Stream) *streamConn {
	return &streamConn{conn: c, stream: s}
}

func (s *streamConn) Read(b []byte) (int, error) {
	return s.stream.Read(b)
}

func (s *streamConn) Write(b []byte) (int, error) {
	return s.stream.Write(b)
}

func (s *streamConn) Close() error {
	s.closed.Store(true)
	var err error
	s.once.Do(func() {
		err = s.stream.Close()
	})
	return err
}

func (s *streamConn) LocalAddr() net.Addr                { return s.conn.LocalAddr() }
func (s *streamConn) RemoteAddr() net.Addr               { return s.conn.RemoteAddr() }
func (s *streamConn) SetDeadline(t time.Time) error      { return s.stream.SetDeadline(t) }
func (s *streamConn) SetReadDeadline(t time.Time) error  { return s.stream.SetReadDeadline(t) }
func (s *streamConn) SetWriteDeadline(t time.Time) error { return s.stream.SetWriteDeadline(t) }
