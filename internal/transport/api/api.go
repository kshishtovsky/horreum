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
	ObserveCAS(status string, dur time.Duration)
	ObserveIncr(status string, dur time.Duration)
	ObserveScan(status string, dur time.Duration)
	ObserveDelPrefix(status string, dur time.Duration)
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
	Set(key, value []byte, ttlSeconds uint32) ([]byte, error)
	Get(key []byte) ([]byte, error)
	Delete(key []byte) error
	DeleteExpired(limit int) error
	CAS(key, expectedValue, newValue []byte) (currentValue []byte, swapped bool, err error)
	Incr(key []byte, delta int64) (int64, error)
	Scan(prefix []byte, cursor uint64, count int) ([][]byte, uint64, error)
	DelPrefix(prefix []byte) (uint64, error)
	Close() error
}

// ShardRouter routes a key (or arbitrary identifier) to the cache
// instance that owns it.  The transport uses this to pin every frame
// for a key to the same shard, preserving the cache's single-threaded
// invariant.
type ShardRouter interface {
	CacheFor(key []byte) CacheService
	CacheIndexFor(key []byte) int
	// ShardCount returns the number of shards.  Transports use this
	// to size their per-shard worker pools.
	ShardCount() int
	Scan(prefix []byte, cursor uint64, count int) ([][]byte, uint64, error)
	DelPrefix(prefix []byte) (uint64, error)
}

// Errors returned by CacheService implementations.
var (
	ErrNotFound  = errors.New("transport: not found")
	ErrTooLarge  = errors.New("transport: payload too large")
	ErrNoBackend = errors.New("transport: no cache backend configured")
)
