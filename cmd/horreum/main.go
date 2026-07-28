// Command horreum runs the unified horreum cache server with TCP or
// QUIC transport, optional persistence, and a Prometheus /metrics
// endpoint.
//
// Usage:
//
//	horreum --addr=:7373 --transport=tcp --shards=8 --region-size=1GiB \
//	        --persistent-path=/var/lib/horreum --metrics-addr=:9090
//
// On SIGINT/SIGTERM, the server gracefully shuts down in this order:
//
//  1. Stop accepting new connections.
//  2. Drain active connections (bounded by the shutdown timeout).
//  3. Persist arena + WAL to disk (if persistent mode).
//  4. Close the arena (munmap).
//  5. Stop the metrics HTTP server.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	_ "net/http/pprof"
	"os"
	"runtime"
	"time"

	"github.com/horreum/horreum/internal/arena"
	"github.com/horreum/horreum/internal/arena/persist"
	"github.com/horreum/horreum/internal/index"
	"github.com/horreum/horreum/internal/metrics"
	"github.com/horreum/horreum/internal/shutdown"
	"github.com/horreum/horreum/internal/transport"
	_ "github.com/horreum/horreum/internal/transport/quic" // register "quic" factory
	tpquic "github.com/horreum/horreum/internal/transport/quic"
	_ "github.com/horreum/horreum/internal/transport/tcp" // register "tcp" factory
)

// Gauge handles registered at startup.
var (
	memUsedGauge   metrics.Hash
	memFreeGauge   metrics.Hash
	indexKeysGauge metrics.Hash
	evictS         gaugeHandle
	evictM         gaugeHandle
)

type gaugeHandle struct {
	hash metrics.Hash
}

func main() {
	var (
		addr          = flag.String("addr", ":7373", "listen address (host:port)")
		transportName = flag.String("transport", "tcp", "transport name: tcp or quic")
		shards        = flag.Int("shards", runtime.NumCPU(), "number of shards")
		regionSize    = flag.Uint64("region-size", 1<<30, "arena region size in bytes (default 1 GiB)")
		evictCapacity = flag.Uint64("evict-capacity", 4096, "S3-FIFO eviction capacity per shard")
		persistPath   = flag.String("persistent-path", "", "if set, enable persistent mode rooted at this directory")
		durable       = flag.Bool("durable", true, "if true, fsync the WAL after every write (only relevant with --persistent-path)")
		metricsAddr   = flag.String("metrics-addr", ":9090", "Prometheus /metrics listen address")
		tlsCert       = flag.String("tls-cert", "", "TLS certificate file (required for quic)")
		tlsKey        = flag.String("tls-key", "", "TLS key file (required for quic)")
		shutdownTime  = flag.Duration("shutdown-timeout", 30*time.Second, "graceful shutdown timeout")
		version       = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *version {
		fmt.Println("horreum dev")
		return
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// Build the recorder and pre-register gauges.
	rec := metrics.NewRecorder()
	memUsedGauge = metrics.RegisterGauge("horreum_memory_used_bytes",
		"Bytes currently allocated in the arena.",
		[]metrics.Label{{Name: "region"}})
	memFreeGauge = metrics.RegisterGauge("horreum_memory_free_bytes",
		"Bytes free in the arena.",
		[]metrics.Label{{Name: "region"}})
	indexKeysGauge = metrics.RegisterGauge("horreum_index_keys_total",
		"Number of live keys in the index.",
		nil)
	evictS.hash = metrics.RegisterCounter("horreum_evictions_total",
		"Total evictions by queue (S or M).",
		[]metrics.Label{{Name: "queue", Value: "S"}})
	evictM.hash = metrics.RegisterCounter("horreum_evictions_total",
		"Total evictions by queue (S or M).",
		[]metrics.Label{{Name: "queue", Value: "M"}})

	// Bring up the storage backend.
	var (
		sets         *transport.ShardSet
		pm           *persist.PersistentManager
		closeStorage func() error
		index_       *index.HashIndex
	)

	if *persistPath != "" {
		log.Printf("starting in persistent mode at %s (region=%d MiB)",
			*persistPath, *regionSize>>20)
		var err error
		pm, err = persist.New(*persistPath, persist.Options{
			RegionSize:   *regionSize,
			Durable:      *durable,
			SyncInterval: time.Second,
		})
		if err != nil {
			log.Fatalf("persist.New: %v", err)
		}
		sets = buildShardSetFromArena(pm.Manager(), *shards, *evictCapacity)
		index_ = index.New(1 << 16)
		// Replay WAL into the in-memory index.
		if err := pm.Replay(index_); err != nil {
			log.Printf("WAL replay error (continuing): %v", err)
		}
		log.Printf("cold start complete: %d live keys in index", index_.Count())
		closeStorage = func() error {
			if err := pm.Sync(); err != nil {
				log.Printf("pm.Sync: %v", err)
			}
			if err := pm.Checkpoint(index_); err != nil {
				log.Printf("pm.Checkpoint: %v", err)
			}
			return pm.Close()
		}
	} else {
		log.Printf("starting in anonymous mode (region=%d MiB, shards=%d)",
			*regionSize>>20, *shards)
		sets = transport.NewShardSetOrPanic(*shards, *regionSize, *evictCapacity)
		index_ = nil
		closeStorage = func() error { return sets.Close() }
	}

	// Background gauge updater.
	go updateGauges(sets, index_)

	// Metrics HTTP server.
	metricsSrv, err := metrics.NewMetricsServer(*metricsAddr)
	if err != nil {
		log.Fatalf("metrics.NewMetricsServer: %v", err)
	}
	metricsSrv.Start()
	log.Printf("metrics listening on http://%s/metrics", metricsSrv.Addr())

	// Build the transport.
	var tOpts transport.Options
	tOpts.Addr = *addr
	tOpts.TransportName = *transportName
	tOpts.Router = sets
	tOpts.Recorder = rec
	if *transportName == "quic" {
		cert, err := tls.LoadX509KeyPair(*tlsCert, *tlsKey)
		if err != nil {
			log.Fatalf("TLS load: %v", err)
		}
		tpquic.RegisterCertificate("cli-cert", &cert)
		tOpts.TLSCertFile = "cli-cert"
	}

	srv, err := transport.NewServerFromOpts(tOpts)
	if err != nil {
		log.Fatalf("transport.NewServer: %v", err)
	}
	log.Printf("horreum listening on %s (%s)", srv.Addr(), *transportName)

	// Bring up the shutdown coordinator.
	coord := shutdown.New()
	coord.Register("stop-listener", func(ctx context.Context) error {
		return srv.Shutdown(ctx)
	})
	coord.Register("metrics", metricsSrv.Shutdown)
	coord.Register("storage", func(ctx context.Context) error {
		return closeStorage()
	})

	// Listen for SIGINT/SIGTERM in a goroutine; the main goroutine
	// serves until the signal arrives.
	go coord.WaitForSignal()
	go func() {
		if err := srv.ListenAndServe(); err != nil {
			log.Printf("ListenAndServe returned: %v", err)
		}
		coord.Trigger()
	}()
	log.Printf("ready; waiting for signal...")
	if err := coord.Run(*shutdownTime); err != nil {
		log.Printf("shutdown finished with: %v", err)
		os.Exit(1)
	}
	log.Printf("bye")
}

// buildShardSetFromArena wraps an existing arena.Manager in a ShardSet
// for use with the transport layer.  For persistent mode the arena
// is shared across all shards; the shards share its byte space but
// maintain independent eviction policies.
func buildShardSetFromArena(mgr *arena.Manager, numShards int, evictCap uint64) *transport.ShardSet {
	// For v1 we use a single-shard PersistentManager wrapping the
	// shared arena.  Multi-shard persistent arenas require
	// per-shard regions which the persist layer doesn't yet
	// support; for now, persistent mode forces shards=1.
	if numShards > 1 {
		log.Printf("persistent mode: forcing shards=1 (multi-shard persistent arenas not yet supported)")
		numShards = 1
	}
	return transport.NewShardSetFromArena(mgr, numShards, evictCap)
}

// updateGauges periodically refreshes memory and index gauges.
func updateGauges(sets *transport.ShardSet, idx *index.HashIndex) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for range t.C {
		s := sets.Stats()
		metrics.SetGauge(memUsedGauge, s.UsedBytes)
		metrics.SetGauge(memFreeGauge, s.FreeBytes)
		if idx != nil {
			metrics.SetGauge(indexKeysGauge, uint64(idx.Count()))
		}
	}
}

// Silence unused imports.
var _ net.Listener
