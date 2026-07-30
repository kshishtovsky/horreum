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
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/horreum/horreum/internal/logger"
	"github.com/horreum/horreum/internal/proto"
	"github.com/horreum/horreum/internal/transport"
	"github.com/horreum/horreum/internal/transport/api"
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
	if fr.Op != proto.OpScan && fr.Op != proto.OpDelPrefix {
		if router.CacheIndexFor(fr.Key) != workerIdx {
			// Key belongs to a different shard. Reject to prevent data races.
			*writeBuf = proto.EncodeResponse(*writeBuf, fr.Op, 1, fr.Key, nil)
			return true
		}
	}

	t0 := time.Now()
	switch fr.Op {
	case proto.OpGet:
		return handleGet(router, fr, writeBuf, rec, t0)
	case proto.OpSet:
		return handleSet(router, fr, writeBuf, rec, t0)
	case proto.OpSetEx:
		return handleSetEx(router, fr, writeBuf, rec, t0)
	case proto.OpDel:
		return handleDel(router, fr, writeBuf, rec, t0)
	case proto.OpCAS:
		return handleCAS(router, fr, writeBuf, rec, t0)
	case proto.OpIncr:
		return handleIncr(router, fr, writeBuf, rec, t0)
	case proto.OpScan:
		return handleScan(router, fr, writeBuf, rec, t0)
	case proto.OpDelPrefix:
		return handleDelPrefix(router, fr, writeBuf, rec, t0)

	case proto.OpHSet:
		return handleHSet(router, fr, writeBuf, rec, t0)
	case proto.OpHGet:
		return handleHGet(router, fr, writeBuf, rec, t0)
	case proto.OpHDel:
		return handleHDel(router, fr, writeBuf, rec, t0)
	case proto.OpHGetAll:
		return handleHGetAll(router, fr, writeBuf, rec, t0)

	case proto.OpLPush:
		return handleLPush(router, fr, writeBuf, rec, t0)
	case proto.OpLPop:
		return handleLPop(router, fr, writeBuf, rec, t0)
	case proto.OpRPush:
		return handleRPush(router, fr, writeBuf, rec, t0)
	case proto.OpRPop:
		return handleRPop(router, fr, writeBuf, rec, t0)
	case proto.OpLLen:
		return handleLLen(router, fr, writeBuf, rec, t0)

	case proto.OpSAdd:
		return handleSAdd(router, fr, writeBuf, rec, t0)
	case proto.OpSRem:
		return handleSRem(router, fr, writeBuf, rec, t0)
	case proto.OpSIsMember:
		return handleSIsMember(router, fr, writeBuf, rec, t0)
	case proto.OpSMembers:
		return handleSMembers(router, fr, writeBuf, rec, t0)
	default:
		return false
	}
}

func handleGet(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	val, err := cache.Get(fr.Key)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpGet, 1, fr.Key, nil)
		rec.ObserveGet("err", time.Since(t0))
		return true
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpGet, 0, fr.Key, val)
	rec.ObserveGet("ok", time.Since(t0))
	return true
}

func handleSet(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	if _, err := cache.Set(fr.Key, fr.Value, 0); err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSet, 1, fr.Key, nil)
		rec.ObserveSet("err", time.Since(t0))
		return true
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSet, 0, fr.Key, nil)
	rec.ObserveSet("ok", time.Since(t0))
	return true
}

func handleSetEx(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	if len(fr.Value) < 4 {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSetEx, 1, fr.Key, nil)
		rec.ObserveSet("err", time.Since(t0))
		return true
	}
	ttl := binary.LittleEndian.Uint32(fr.Value[:4])
	val := fr.Value[4:]
	cache := router.CacheFor(fr.Key)
	if _, err := cache.Set(fr.Key, val, ttl); err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSetEx, 1, fr.Key, nil)
		rec.ObserveSet("err", time.Since(t0))
		return true
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSetEx, 0, fr.Key, nil)
	rec.ObserveSet("ok", time.Since(t0))
	return true
}

func handleDel(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	_ = cache.Delete(fr.Key)
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpDel, 0, fr.Key, nil)
	rec.ObserveDel("ok", time.Since(t0))
	return true
}

func handleCAS(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	if len(fr.Value) < 4 {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpCAS, 1, fr.Key, nil)
		rec.ObserveCAS("err", time.Since(t0))
		return true
	}
	expLen := int(binary.LittleEndian.Uint32(fr.Value[:4]))
	if 4+expLen > len(fr.Value) {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpCAS, 1, fr.Key, nil)
		rec.ObserveCAS("err", time.Since(t0))
		return true
	}
	expectedValue := fr.Value[4 : 4+expLen]
	newValue := fr.Value[4+expLen:]
	cache := router.CacheFor(fr.Key)
	current, swapped, err := cache.CAS(fr.Key, expectedValue, newValue)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpCAS, 1, fr.Key, nil)
		rec.ObserveCAS("err", time.Since(t0))
		return true
	}
	if !swapped {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpCAS, 1, fr.Key, current)
		rec.ObserveCAS("err", time.Since(t0))
		return true
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpCAS, 0, fr.Key, nil)
	rec.ObserveCAS("ok", time.Since(t0))
	return true
}

func handleIncr(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	if len(fr.Value) != 8 {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpIncr, 1, fr.Key, nil)
		rec.ObserveIncr("err", time.Since(t0))
		return true
	}
	delta := int64(binary.LittleEndian.Uint64(fr.Value))
	cache := router.CacheFor(fr.Key)
	newVal, err := cache.Incr(fr.Key, delta)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpIncr, 1, fr.Key, nil)
		rec.ObserveIncr("err", time.Since(t0))
		return true
	}
	var respBuf [8]byte
	binary.LittleEndian.PutUint64(respBuf[:], uint64(newVal))
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpIncr, 0, fr.Key, respBuf[:])
	rec.ObserveIncr("ok", time.Since(t0))
	return true
}

func handleScan(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	if len(fr.Value) < 12 {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpScan, 1, fr.Key, nil)
		rec.ObserveScan("err", time.Since(t0))
		return true
	}
	cursor := binary.LittleEndian.Uint64(fr.Value[:8])
	count := int(binary.LittleEndian.Uint32(fr.Value[8:12]))

	keys, nextCursor, err := router.Scan(fr.Key, cursor, count)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpScan, 1, fr.Key, nil)
		rec.ObserveScan("err", time.Since(t0))
		return true
	}

	payloadLen := 8 + 4
	for _, k := range keys {
		payloadLen += 2 + len(k)
	}
	respPayload := make([]byte, payloadLen)
	binary.LittleEndian.PutUint64(respPayload[:8], nextCursor)
	binary.LittleEndian.PutUint32(respPayload[8:12], uint32(len(keys)))
	off := 12
	for _, k := range keys {
		binary.LittleEndian.PutUint16(respPayload[off:off+2], uint16(len(k)))
		copy(respPayload[off+2:], k)
		off += 2 + len(k)
	}

	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpScan, 0, fr.Key, respPayload)
	rec.ObserveScan("ok", time.Since(t0))
	return true
}

func handleDelPrefix(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	deleted, err := router.DelPrefix(fr.Key)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpDelPrefix, 1, fr.Key, nil)
		rec.ObserveDelPrefix("err", time.Since(t0))
		return true
	}
	var respBuf [8]byte
	binary.LittleEndian.PutUint64(respBuf[:], deleted)
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpDelPrefix, 0, fr.Key, respBuf[:])
	rec.ObserveDelPrefix("ok", time.Since(t0))
	return true
}

func handleHSet(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	if len(fr.Value) < 2 {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpHSet, 1, fr.Key, nil)
		return true
	}
	flen := int(binary.LittleEndian.Uint16(fr.Value[:2]))
	if 2+flen > len(fr.Value) {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpHSet, 1, fr.Key, nil)
		return true
	}
	field := fr.Value[2 : 2+flen]
	val := fr.Value[2+flen:]
	cache := router.CacheFor(fr.Key)
	_, err := cache.HSet(fr.Key, field, val)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpHSet, 1, fr.Key, nil)
		return true
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpHSet, 0, fr.Key, nil)
	return true
}

func handleHGet(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	field := fr.Value
	cache := router.CacheFor(fr.Key)
	val, err := cache.HGet(fr.Key, field)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpHGet, 1, fr.Key, nil)
		return true
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpHGet, 0, fr.Key, val)
	return true
}

func handleHDel(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	field := fr.Value
	cache := router.CacheFor(fr.Key)
	deleted, err := cache.HDel(fr.Key, field)
	if err != nil || !deleted {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpHDel, 1, fr.Key, nil)
		return true
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpHDel, 0, fr.Key, nil)
	return true
}

func handleHGetAll(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	fields, values, err := cache.HGetAll(fr.Key)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpHGetAll, 1, fr.Key, nil)
		return true
	}
	payloadLen := 4
	for i := range fields {
		payloadLen += 2 + len(fields[i]) + 4 + len(values[i])
	}
	respPayload := make([]byte, payloadLen)
	binary.LittleEndian.PutUint32(respPayload[:4], uint32(len(fields)))
	off := 4
	for i := range fields {
		binary.LittleEndian.PutUint16(respPayload[off:off+2], uint16(len(fields[i])))
		copy(respPayload[off+2:], fields[i])
		off += 2 + len(fields[i])
		binary.LittleEndian.PutUint32(respPayload[off:off+4], uint32(len(values[i])))
		copy(respPayload[off+4:], values[i])
		off += 4 + len(values[i])
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpHGetAll, 0, fr.Key, respPayload)
	return true
}

func handleLPush(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	length, err := cache.LPush(fr.Key, fr.Value)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpLPush, 1, fr.Key, nil)
		return true
	}
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], length)
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpLPush, 0, fr.Key, buf[:])
	return true
}

func handleRPush(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	length, err := cache.RPush(fr.Key, fr.Value)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpRPush, 1, fr.Key, nil)
		return true
	}
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], length)
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpRPush, 0, fr.Key, buf[:])
	return true
}

func handleLPop(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	elem, err := cache.LPop(fr.Key)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpLPop, 1, fr.Key, nil)
		return true
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpLPop, 0, fr.Key, elem)
	return true
}

func handleRPop(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	elem, err := cache.RPop(fr.Key)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpRPop, 1, fr.Key, nil)
		return true
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpRPop, 0, fr.Key, elem)
	return true
}

func handleLLen(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	length, err := cache.LLen(fr.Key)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpLLen, 1, fr.Key, nil)
		return true
	}
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], length)
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpLLen, 0, fr.Key, buf[:])
	return true
}

func handleSAdd(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	added, err := cache.SAdd(fr.Key, fr.Value)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSAdd, 1, fr.Key, nil)
		return true
	}
	status := uint8(0)
	if !added {
		status = 1
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSAdd, status, fr.Key, nil)
	return true
}

func handleSRem(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	removed, err := cache.SRem(fr.Key, fr.Value)
	if err != nil || !removed {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSRem, 1, fr.Key, nil)
		return true
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSRem, 0, fr.Key, nil)
	return true
}

func handleSIsMember(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	isMember, err := cache.SIsMember(fr.Key, fr.Value)
	if err != nil || !isMember {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSIsMember, 1, fr.Key, nil)
		return true
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSIsMember, 0, fr.Key, nil)
	return true
}

func handleSMembers(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	members, err := cache.SMembers(fr.Key)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSMembers, 1, fr.Key, nil)
		return true
	}
	payloadLen := 4
	for i := range members {
		payloadLen += 2 + len(members[i])
	}
	respPayload := make([]byte, payloadLen)
	binary.LittleEndian.PutUint32(respPayload[:4], uint32(len(members)))
	off := 4
	for i := range members {
		binary.LittleEndian.PutUint16(respPayload[off:off+2], uint16(len(members[i])))
		copy(respPayload[off+2:], members[i])
		off += 2 + len(members[i])
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSMembers, 0, fr.Key, respPayload)
	return true
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
