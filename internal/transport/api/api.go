// Package api exposes the public Transport interface and the Server
// factory.  It is intentionally minimal — concrete TCP and QUIC
// implementations live in their own sub-packages to break the import
// cycle that would otherwise arise (each transport sub-package needs
// this interface; this package must not import them).
package api

import (
	"context"
	"errors"
	"net"
	"time"
)

// Recorder records per-op latency and counter increments.  The
// transport layer invokes the appropriate Observe* method on every
// frame; nil is treated as "no recording".  The recorder interface
// lives here so transport implementations don't have to import the
// metrics package directly (which would force a metrics dependency
// even on minimal builds).
type Recorder interface {
	ObserveSet(status string, dur time.Duration)
	ObserveGet(status string, dur time.Duration)
	ObserveDel(status string, dur time.Duration)
}

// Transport is the abstract listener / connection-servicer used by
// Server.  Implementations: tcp.Transport, quic.Transport.
type Transport interface {
	ListenAndServe() error
	Shutdown(ctx context.Context) error
	Addr() net.Addr
}

// CacheService is the storage backend that the transport layer reads
// and writes.  The transport is single-threaded per shard, so the
// implementation does not need to provide its own synchronisation.
type CacheService interface {
	Set(key, value []byte) ([]byte, error)
	Get(key []byte) ([]byte, error)
	Delete(key []byte) error
	Close() error
}

// ShardRouter routes a key (or arbitrary identifier) to the cache
// instance that owns it.  The transport uses this to pin every frame
// for a key to the same shard, preserving the cache's single-threaded
// invariant.
type ShardRouter interface {
	CacheFor(key []byte) CacheService
	// ShardCount returns the number of shards.  Transports use this
	// to size their per-shard worker pools.
	ShardCount() int
}

// Errors returned by CacheService implementations.
var (
	ErrNotFound  = errors.New("transport: not found")
	ErrTooLarge  = errors.New("transport: payload too large")
	ErrNoBackend = errors.New("transport: no cache backend configured")
)
