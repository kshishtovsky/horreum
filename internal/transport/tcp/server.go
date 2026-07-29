// Package tcp implements the TCP transport using the standard library
// net package.
//
// Connection pinning: each accepted connection is dispatched to the
// shard worker that owns its remote IP.  All frames on that connection
// are processed serially on that worker's goroutine, so the shard's
// CacheService is touched by exactly one goroutine.  Keys sent by the
// client must hash to the same shard — see transport.ShardSet for
// the routing function.  Clients with keys spanning multiple shards
// must open separate connections (one per shard).
//
// For a Linux-only epoll-backed variant with strict zero-alloc I/O,
// integrate github.com/cloudwego/netpoll instead — the Transport
// interface in internal/transport/api is shaped so that swapping the
// accept loop is a drop-in change.
package tcp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
	"time"

	"github.com/horreum/horreum/internal/logger"
	"github.com/horreum/horreum/internal/proto"
	"github.com/horreum/horreum/internal/transport"
	"github.com/horreum/horreum/internal/transport/api"
)

// noopRecorder is used when api.Options.Recorder is nil.  Its methods
// are empty; the compiler may inline them away.
type noopRecorder struct{}

func (noopRecorder) ObserveSet(string, time.Duration) {}
func (noopRecorder) ObserveGet(string, time.Duration) {}
func (noopRecorder) ObserveDel(string, time.Duration) {}

// Transport is the TCP-specific transport.
type Transport struct {
	listener net.Listener
	router   api.ShardRouter
	workers  *transport.WorkerPool
	recorder api.Recorder
	stop     chan struct{}
	closed   atomic.Bool
}

var _ api.Transport = (*Transport)(nil)

// NewTransport creates a TCP listener bound to addr and wires it to
// the given ShardRouter.
//
// recorder may be nil; in that case a noop recorder is used.
func NewTransport(addr string, router api.ShardRouter, recorder api.Recorder) (*Transport, error) {
	if router == nil {
		return nil, api.ErrNoBackend
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	numShards := router.ShardCount()
	if numShards <= 0 {
		numShards = 8
	}
	if recorder == nil {
		recorder = noopRecorder{}
	}
	t := &Transport{
		listener: ln,
		router:   router,
		recorder: recorder,
		stop:     make(chan struct{}),
		workers:  transport.NewWorkerPool(numShards, 4096),
	}
	t.workers.Start()
	return t, nil
}

// Addr implements api.Transport.
func (t *Transport) Addr() net.Addr { return t.listener.Addr() }

// ListenAndServe accepts connections until Shutdown is called.
func (t *Transport) ListenAndServe() error {
	for {
		conn, err := t.listener.Accept()
		if err != nil {
			if t.closed.Load() {
				return nil
			}
			return err
		}
		idx := shardIndexFor(conn, t.workers.ShardCount())
		t.workers.Submit(transport.Job{Conn: conn, Handle: t.handle}, idx)
	}
}

// Shutdown closes the listener and waits for workers, bounded by ctx.
func (t *Transport) Shutdown(ctx context.Context) error {
	if !t.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(t.stop)
	_ = t.listener.Close()
	t.workers.Shutdown(ctx)
	return nil
}

// shardIndexFor picks a worker index for the given connection.  The
// index is derived from the connection's remote IP and local port so
// connections from different clients (or the same client hitting
// different local ports) land on different shards.  Connections from
// the same (IP, local port) pair land on the same shard — this is
// the connection-pinning invariant.
//
// The local port is included (not the remote port) because the local
// port uniquely identifies the server-side socket, while the remote
// port may be ephemeral and recycled.
func shardIndexFor(conn net.Conn, numShards int) int {
	if numShards <= 0 {
		return 0
	}
	addr := conn.RemoteAddr()
	if addr == nil {
		return 0
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		host = addr.String()
	}
	const (
		offset = 14695981039346656037
		prime  = 1099511628211
	)
	h := uint64(offset)
	for i := 0; i < len(host); i++ {
		h ^= uint64(host[i])
		h *= prime
	}
	// Mix in the local port so each accepted conn is unique.
	if la := conn.LocalAddr(); la != nil {
		if _, port, err := net.SplitHostPort(la.String()); err == nil {
			for i := 0; i < len(port); i++ {
				h ^= uint64(port[i])
				h *= prime
			}
		}
	}
	return int(h % uint64(numShards))
}

const (
	readChunkSize      = 4096
	initialWriteBufCap = 1024
	idleTimeout        = 2 * time.Minute
)

// handle is the per-job handler invoked by the shard worker.  It
// invokes serve(j.Conn) so the per-connection logic stays in serve.
func (t *Transport) handle(j transport.Job) {
	t.serve(j.Conn)
}

// serve handles one connection from accept to EOF.  All frames on
// the connection are processed serially on the worker goroutine that
// dispatched them.  The cache calls in handleFrame touch the shard
// pinned to this worker (via ShardSet.ShardRouter.CacheFor which
// hashes the key — the result MUST equal the worker's shard index,
// which is the case when the client's keys hash to the same shard as
// the connection's remote IP).
func (t *Transport) serve(c net.Conn) {
	remoteAddr := c.RemoteAddr().String()
	slog.Debug("client connected", "remote_addr", remoteAddr)
	defer func() {
		slog.Debug("client disconnected", "remote_addr", remoteAddr)
		_ = c.Close()
	}()
	_ = c.SetDeadline(time.Now().Add(idleTimeout))

	parser := proto.NewParser()
	writeBuf := make([]byte, 0, initialWriteBufCap)
	var chunk [readChunkSize]byte

	for {
		n, err := c.Read(chunk[:])
		if err != nil {
			if err == io.EOF || errors.Is(err, net.ErrClosed) {
				return
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return
			}
			if logger.NetErrorLimiter.Allow() {
				slog.Warn("connection read error", "remote_addr", remoteAddr, "error", err)
			}
			return
		}
		if n == 0 {
			return
		}
		frames, perr := parser.Feed(chunk[:n])
		if perr != nil {
			if logger.NetErrorLimiter.Allow() {
				slog.Warn("protocol parsing error", "remote_addr", remoteAddr, "error", perr)
			}
			return
		}
		for i := range frames {
			if !handleFrame(t.router, &frames[i], &writeBuf, t.recorder) {
				return
			}
		}
		if len(writeBuf) > 0 {
			if _, err := c.Write(writeBuf); err != nil {
				return
			}
			writeBuf = writeBuf[:0]
		}
		_ = c.SetDeadline(time.Now().Add(idleTimeout))
	}
}

// handleFrame processes one frame on the shard that owns key and
// appends the response to *writeBuf.
//
// IMPORTANT: this function MUST only be called from the shard worker
// whose index matches the shard router's CacheFor(key) result.  The
// shard worker enforces serialisation, so the cache's single-threaded
// invariant holds.  See Transport.ListenAndServe for the dispatch
// logic.
func handleFrame(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder) bool {
	cache := router.CacheFor(fr.Key)
	status := "ok"
	t0 := time.Now()
	switch fr.Op {
	case proto.OpGet:
		val, err := cache.Get(fr.Key)
		if err != nil {
			status = "err"
			*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpGet, 1, fr.Key, nil)
			rec.ObserveGet(status, time.Since(t0))
			return true
		}
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpGet, 0, fr.Key, val)
		rec.ObserveGet(status, time.Since(t0))
		return true
	case proto.OpSet:
		if _, err := cache.Set(fr.Key, fr.Value); err != nil {
			status = "err"
			*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSet, 1, fr.Key, nil)
			rec.ObserveSet(status, time.Since(t0))
			return true
		}
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSet, 0, fr.Key, nil)
		rec.ObserveSet(status, time.Since(t0))
		return true
	case proto.OpDel:
		_ = cache.Delete(fr.Key)
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpDel, 0, fr.Key, nil)
		rec.ObserveDel(status, time.Since(t0))
		return true
	}
	return false
}
