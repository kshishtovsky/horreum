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
	"crypto/rand"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	_ "net/http/pprof"
	"os"
	"time"

	"encoding/base64"
	"encoding/hex"
	"strings"

	"github.com/horreum/horreum/internal/arena"
	"github.com/horreum/horreum/internal/arena/persist"
	"github.com/horreum/horreum/internal/compress"
	"github.com/horreum/horreum/internal/config"
	"github.com/horreum/horreum/internal/encrypt"
	"github.com/horreum/horreum/internal/index"
	"github.com/horreum/horreum/internal/logger"
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
	// 1. Parse --config / -config manually from command line args
	var yamlPath string
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		if strings.HasPrefix(arg, "--config=") {
			yamlPath = strings.TrimPrefix(arg, "--config=")
		} else if arg == "--config" && i+1 < len(os.Args) {
			yamlPath = os.Args[i+1]
			i++
		} else if strings.HasPrefix(arg, "-config=") {
			yamlPath = strings.TrimPrefix(arg, "-config=")
		} else if arg == "-config" && i+1 < len(os.Args) {
			yamlPath = os.Args[i+1]
			i++
		}
	}

	// 2. Load standard defaults
	var (
		defaultAddr             = ":7373"
		defaultTransport        = "tcp"
		defaultShards           = 4
		defaultRegionSize       = uint64(256 << 20) // 256 MiB
		defaultEvictCapacity    = uint64(4096)
		defaultPersistPath      = ""
		defaultDurable          = true
		defaultMetricsAddr      = ":9090"
		defaultTLSCert          = ""
		defaultTLSKey           = ""
		defaultShutdownTimeout  = 30 * time.Second
		defaultCompression      = "none"
		defaultMinCompressSize  = 64
		defaultLogLevel         = "info"
		defaultLogFormat        = "text"
		defaultSlowLogThreshold = 10 * time.Millisecond
		defaultEncryptionKeyPath = ""
	)

	// 3. Apply config file values if provided
	if yamlPath != "" {
		data, err := os.ReadFile(yamlPath)
		if err != nil {
			slog.Error("failed to read config file", "path", yamlPath, "error", err)
			os.Exit(1)
		}
		cfg, err := config.ParseYAML(data)
		if err != nil {
			slog.Error("failed to parse config file", "path", yamlPath, "error", err)
			os.Exit(1)
		}

		if cfg.Server.Addr != "" {
			defaultAddr = cfg.Server.Addr
		}
		if cfg.Server.Transport != "" {
			defaultTransport = cfg.Server.Transport
		}
		if cfg.Server.Shards != nil {
			defaultShards = *cfg.Server.Shards
		}
		if cfg.Server.RegionSize != "" {
			sz, err := config.ParseSize(cfg.Server.RegionSize)
			if err != nil {
				slog.Error("invalid region_size in config", "region_size", cfg.Server.RegionSize, "error", err)
				os.Exit(1)
			}
			defaultRegionSize = sz
		}
		if cfg.Server.EvictCapacity != nil {
			defaultEvictCapacity = *cfg.Server.EvictCapacity
		}
		if cfg.Server.TLSCert != "" {
			defaultTLSCert = cfg.Server.TLSCert
		}
		if cfg.Server.TLSKey != "" {
			defaultTLSKey = cfg.Server.TLSKey
		}
		if cfg.Server.ShutdownTimeout != "" {
			dur, err := time.ParseDuration(cfg.Server.ShutdownTimeout)
			if err != nil {
				slog.Error("invalid shutdown_timeout in config", "shutdown_timeout", cfg.Server.ShutdownTimeout, "error", err)
				os.Exit(1)
			}
			defaultShutdownTimeout = dur
		}
		if cfg.Storage.PersistentPath != "" {
			defaultPersistPath = cfg.Storage.PersistentPath
		}
		if cfg.Storage.Durable != nil {
			defaultDurable = *cfg.Storage.Durable
		}
		if cfg.Metrics.Addr != "" {
			defaultMetricsAddr = cfg.Metrics.Addr
		}
		if cfg.Compression.Algorithm != "" {
			defaultCompression = cfg.Compression.Algorithm
		}
		if cfg.Compression.MinSize != nil {
			defaultMinCompressSize = *cfg.Compression.MinSize
		}
		if cfg.Logging.Level != "" {
			defaultLogLevel = cfg.Logging.Level
		}
		if cfg.Logging.Format != "" {
			defaultLogFormat = cfg.Logging.Format
		}
		if cfg.Logging.SlowLogThreshold != "" {
			dur, err := time.ParseDuration(cfg.Logging.SlowLogThreshold)
			if err != nil {
				slog.Error("invalid logging.slow_log_threshold in config", "threshold", cfg.Logging.SlowLogThreshold, "error", err)
				os.Exit(1)
			}
			defaultSlowLogThreshold = dur
		}
		if cfg.Security.EncryptionKeyPath != "" {
			defaultEncryptionKeyPath = cfg.Security.EncryptionKeyPath
		}
	}

	var (
		_                = flag.String("config", "", "path to YAML configuration file")
		addr             = flag.String("addr", defaultAddr, "listen address (host:port)")
		transportName    = flag.String("transport", defaultTransport, "transport name: tcp or quic")
		shards           = flag.Int("shards", defaultShards, "number of shards")
		regionSize       = flag.Uint64("region-size", defaultRegionSize, "arena region size in bytes (default 1 GiB)")
		evictCapacity    = flag.Uint64("evict-capacity", defaultEvictCapacity, "S3-FIFO eviction capacity per shard")
		persistPath      = flag.String("persistent-path", defaultPersistPath, "if set, enable persistent mode rooted at this directory")
		durable          = flag.Bool("durable", defaultDurable, "if true, fsync the WAL after every write (only relevant with --persistent-path)")
		metricsAddr      = flag.String("metrics-addr", defaultMetricsAddr, "Prometheus /metrics listen address")
		tlsCert          = flag.String("tls-cert", defaultTLSCert, "TLS certificate file (required for quic)")
		tlsKey           = flag.String("tls-key", defaultTLSKey, "TLS key file (required for quic)")
		shutdownTime     = flag.Duration("shutdown-timeout", defaultShutdownTimeout, "graceful shutdown timeout")
		compression      = flag.String("compression", defaultCompression, "value compression: none or lz4")
		minCompressSize  = flag.Int("min-compress-size", defaultMinCompressSize, "minimum value size in bytes to attempt compression")
		logLevel         = flag.String("log-level", defaultLogLevel, "logging level: debug, info, warn, error")
		logFormat        = flag.String("log-format", defaultLogFormat, "logging format: text or json")
		slowLogThreshold = flag.Duration("slow-log-threshold", defaultSlowLogThreshold, "duration threshold above which operations are logged as slow")
		encryptionKeyPath = flag.String("encryption-key-path", defaultEncryptionKeyPath, "path to 32-byte encryption key file")
		keygen            = flag.String("keygen", "", "generate a new random 32-byte encryption key file at path and exit")
		version           = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *version {
		fmt.Println("horreum dev")
		return
	}

	if *keygen != "" {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			fmt.Fprintf(os.Stderr, "failed to generate random key: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(*keygen, key, 0600); err != nil {
			fmt.Fprintf(os.Stderr, "failed to write key file %q: %v\n", *keygen, err)
			os.Exit(1)
		}
		fmt.Printf("Generated 32-byte AES key file at %s\n", *keygen)
		return
	}

	// Initialize structured logger
	logger.Init(*logLevel, *logFormat, *slowLogThreshold)
	if yamlPath != "" {
		slog.Info("loaded configuration", "path", yamlPath)
	}

	// Build the compressor.
	var comp compress.Compressor
	switch *compression {
	case "lz4":
		comp = compress.NewLZ4(*minCompressSize)
		slog.Info("value compression enabled", "algorithm", "lz4", "min_size", *minCompressSize)
	case "none", "":
		comp = compress.Noop{}
	default:
		slog.Error("unknown compression value", "compression", *compression)
		os.Exit(1)
	}

	// Build the cipher.
	var cip encrypt.Cipher = encrypt.Noop{}
	envKey := os.Getenv("HORREUM_KEY")
	var keyBytes []byte
	var err error

	if envKey != "" {
		if len(envKey) == 64 {
			keyBytes, err = hex.DecodeString(envKey)
		} else if len(envKey) == 44 {
			keyBytes, err = base64.StdEncoding.DecodeString(envKey)
		} else if len(envKey) == 32 {
			keyBytes = []byte(envKey)
		} else {
			err = fmt.Errorf("env key has invalid length %d (expected 64 hex, 44 base64, or 32 raw bytes)", len(envKey))
		}
		if err != nil {
			slog.Error("failed to parse HORREUM_KEY env variable", "error", err)
			os.Exit(1)
		}
	} else {
		targetPath := *encryptionKeyPath
		if targetPath == "" {
			targetPath = "horreum.key"
		}

		// Auto-generate key file if it doesn't exist yet
		if _, statErr := os.Stat(targetPath); errors.Is(statErr, os.ErrNotExist) {
			autoKey := make([]byte, 32)
			if _, genErr := rand.Read(autoKey); genErr != nil {
				slog.Error("failed to auto-generate encryption key", "error", genErr)
				os.Exit(1)
			}
			if writeErr := os.WriteFile(targetPath, autoKey, 0600); writeErr != nil {
				slog.Error("failed to save auto-generated encryption key", "path", targetPath, "error", writeErr)
				os.Exit(1)
			}
			slog.Info("auto-generated 32-byte encryption key file", "path", targetPath)
			keyBytes = autoKey
		} else {
			keyBytes, err = encrypt.LoadKey(targetPath)
			if err != nil {
				slog.Error("failed to load encryption key from file", "path", targetPath, "error", err)
				os.Exit(1)
			}
		}
	}

	if len(keyBytes) > 0 {
		cip, err = encrypt.NewAESGCM(keyBytes)
		if err != nil {
			slog.Error("failed to initialize AES-GCM cipher", "error", err)
			os.Exit(1)
		}
		slog.Info("value encryption enabled", "algorithm", cip.Name())
	}

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
		slog.Info("starting in persistent mode", "path", *persistPath, "region_mib", *regionSize>>20)
		var err error
		pm, err = persist.New(*persistPath, persist.Options{
			RegionSize:   *regionSize,
			Durable:      *durable,
			SyncInterval: time.Second,
		})
		if err != nil {
			slog.Error("failed to initialize persistent manager", "error", err)
			os.Exit(1)
		}
		sets = buildShardSetFromArena(pm.Manager(), *shards, *evictCapacity, comp, cip)
		index_ = index.New(1 << 16)
		// Replay WAL into the in-memory index.
		if err := pm.Replay(index_); err != nil {
			slog.Warn("WAL replay error (continuing)", "error", err)
		}
		slog.Info("cold start complete", "live_keys", index_.Count())
		closeStorage = func() error {
			if err := pm.Sync(); err != nil {
				slog.Error("failed to sync persistent manager", "error", err)
			}
			if err := pm.Checkpoint(index_); err != nil {
				slog.Error("failed to checkpoint persistent manager", "error", err)
			}
			return pm.Close()
		}
	} else {
		slog.Info("starting in anonymous mode", "region_mib", *regionSize>>20, "shards", *shards)
		sets = transport.NewShardSetOrPanic(*shards, *regionSize, *evictCapacity, comp, cip)
		index_ = nil
		closeStorage = func() error { return sets.Close() }
	}

	// Background gauge updater.
	go updateGauges(sets, index_)

	// Metrics HTTP server.
	metricsSrv, err := metrics.NewMetricsServer(*metricsAddr)
	if err != nil {
		slog.Error("failed to initialize metrics server", "error", err)
		os.Exit(1)
	}
	metricsSrv.Start()
	slog.Info("metrics listening", "url", fmt.Sprintf("http://%s/metrics", metricsSrv.Addr()))

	// Build the transport.
	var tOpts transport.Options
	tOpts.Addr = *addr
	tOpts.TransportName = *transportName
	tOpts.Router = sets
	tOpts.Recorder = rec
	if *transportName == "quic" {
		cert, err := tls.LoadX509KeyPair(*tlsCert, *tlsKey)
		if err != nil {
			slog.Error("failed to load TLS certificate", "error", err)
			os.Exit(1)
		}
		tpquic.RegisterCertificate("cli-cert", &cert)
		tOpts.TLSCertFile = "cli-cert"
	}

	srv, err := transport.NewServerFromOpts(tOpts)
	if err != nil {
		slog.Error("failed to construct transport server", "error", err)
		os.Exit(1)
	}
	slog.Info("horreum listening", "addr", srv.Addr().String(), "transport", *transportName)

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
			slog.Error("ListenAndServe returned error", "error", err)
		}
		coord.Trigger()
	}()
	slog.Info("ready; waiting for signal")
	if err := coord.Run(*shutdownTime); err != nil {
		slog.Error("shutdown finished with error", "error", err)
		os.Exit(1)
	}
	slog.Info("bye")
}

// buildShardSetFromArena wraps an existing arena.Manager in a ShardSet
// for use with the transport layer.  For persistent mode the arena
// is shared across all shards; the shards share its byte space but
// maintain independent eviction policies.
func buildShardSetFromArena(mgr *arena.Manager, numShards int, evictCap uint64, comp compress.Compressor, cipher encrypt.Cipher) *transport.ShardSet {
	// For v1 we use a single-shard PersistentManager wrapping the
	// shared arena.  Multi-shard persistent arenas require
	// per-shard regions which the persist layer doesn't yet
	// support; for now, persistent mode forces shards=1.
	if numShards > 1 {
		slog.Info("persistent mode: forcing shards=1 (multi-shard persistent arenas not yet supported)")
		numShards = 1
	}
	return transport.NewShardSetFromArena(mgr, numShards, evictCap, comp, cipher)
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
