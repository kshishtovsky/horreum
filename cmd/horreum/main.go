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
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	_ "net/http/pprof"
	"os"
	"strings"
	"time"

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

type serverConfig struct {
	addr              string
	transportName     string
	shards            int
	regionSize        uint64
	evictCapacity     uint64
	persistPath       string
	durable           bool
	metricsAddr       string
	tlsCert           string
	tlsKey            string
	shutdownTimeout   time.Duration
	compression       string
	minCompressSize   int
	logLevel          string
	logFormat         string
	slowLogThreshold  time.Duration
	encryptionKeyPath string
}

func defaultServerConfig() serverConfig {
	return serverConfig{
		addr:              ":7373",
		transportName:     "tcp",
		shards:            4,
		regionSize:        uint64(256 << 20), // 256 MiB
		evictCapacity:     4096,
		persistPath:       "",
		durable:           true,
		metricsAddr:       ":9090",
		tlsCert:           "",
		tlsKey:            "",
		shutdownTimeout:   30 * time.Second,
		compression:       "none",
		minCompressSize:   64,
		logLevel:          "info",
		logFormat:         "text",
		slowLogThreshold:  10 * time.Millisecond,
		encryptionKeyPath: "",
	}
}

func main() {
	yamlPath := parseYAMLPath()
	defaults := loadConfig(yamlPath)

	var (
		_                 = flag.String("config", "", "path to YAML configuration file")
		addr              = flag.String("addr", defaults.addr, "listen address (host:port)")
		transportName     = flag.String("transport", defaults.transportName, "transport name: tcp or quic")
		shards            = flag.Int("shards", defaults.shards, "number of shards")
		regionSize        = flag.Uint64("region-size", defaults.regionSize, "arena region size in bytes (default 1 GiB)")
		evictCapacity     = flag.Uint64("evict-capacity", defaults.evictCapacity, "S3-FIFO eviction capacity per shard")
		persistPath       = flag.String("persistent-path", defaults.persistPath, "if set, enable persistent mode rooted at this directory")
		durable           = flag.Bool("durable", defaults.durable, "if true, fsync the WAL after every write (only relevant with --persistent-path)")
		metricsAddr       = flag.String("metrics-addr", defaults.metricsAddr, "Prometheus /metrics listen address")
		tlsCert           = flag.String("tls-cert", defaults.tlsCert, "TLS certificate file (required for quic)")
		tlsKey            = flag.String("tls-key", defaults.tlsKey, "TLS key file (required for quic)")
		shutdownTime      = flag.Duration("shutdown-timeout", defaults.shutdownTimeout, "graceful shutdown timeout")
		compression       = flag.String("compression", defaults.compression, "value compression: none or lz4")
		minCompressSize   = flag.Int("min-compress-size", defaults.minCompressSize, "minimum value size in bytes to attempt compression")
		logLevel          = flag.String("log-level", defaults.logLevel, "logging level: debug, info, warn, error")
		logFormat         = flag.String("log-format", defaults.logFormat, "logging format: text or json")
		slowLogThreshold  = flag.Duration("slow-log-threshold", defaults.slowLogThreshold, "duration threshold above which operations are logged as slow")
		encryptionKeyPath = flag.String("encryption-key-path", defaults.encryptionKeyPath, "path to 32-byte encryption key file")
		keygen            = flag.String("keygen", "", "generate a new random 32-byte encryption key file at path and exit")
		version           = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *version {
		fmt.Println("horreum dev")
		return
	}

	if *keygen != "" {
		generateKeyAndExit(*keygen)
		return
	}

	logger.Init(*logLevel, *logFormat, *slowLogThreshold)
	if yamlPath != "" {
		slog.Info("loaded configuration", "path", yamlPath)
	}

	comp := initCompressor(*compression, *minCompressSize)
	cip := initCipher(*encryptionKeyPath)
	rec := metrics.NewRecorder()
	initMetrics()

	sb := initStorage(*persistPath, *durable, *regionSize, *shards, *evictCapacity, comp, cip)
	ctx, cancelGauges := context.WithCancel(context.Background())
	go updateGauges(ctx, sb.sets, sb.index)

	metricsSrv, err := metrics.NewMetricsServer(*metricsAddr)
	if err != nil {
		slog.Error("failed to initialize metrics server", "error", err)
		os.Exit(1)
	}
	metricsSrv.Start()
	slog.Info("metrics listening", "url", fmt.Sprintf("http://%s/metrics", metricsSrv.Addr()))

	srv := initServer(*addr, *transportName, *tlsCert, *tlsKey, sb.sets, rec)

	coord := shutdown.New()
	coord.Register("stop-listener", srv.Shutdown)
	coord.Register("metrics", metricsSrv.Shutdown)
	coord.Register("storage", func(ctx context.Context) error { cancelGauges(); return sb.closeStorage() })

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

func parseYAMLPath() string {
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		if strings.HasPrefix(arg, "--config=") {
			return strings.TrimPrefix(arg, "--config=")
		} else if arg == "--config" && i+1 < len(os.Args) {
			return os.Args[i+1]
		} else if strings.HasPrefix(arg, "-config=") {
			return strings.TrimPrefix(arg, "-config=")
		} else if arg == "-config" && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}
	return ""
}

func loadConfig(yamlPath string) serverConfig {
	cfg := defaultServerConfig()
	if yamlPath == "" {
		return cfg
	}

	data, err := os.ReadFile(yamlPath)
	if err != nil {
		slog.Error("failed to read config file", "path", yamlPath, "error", err)
		os.Exit(1)
	}
	parsed, err := config.ParseYAML(data)
	if err != nil {
		slog.Error("failed to parse config file", "path", yamlPath, "error", err)
		os.Exit(1)
	}

	applyParsedYAML(&cfg, *parsed)
	return cfg
}

func applyParsedYAML(cfg *serverConfig, parsed config.Config) {
	if parsed.Server.Addr != "" {
		cfg.addr = parsed.Server.Addr
	}
	if parsed.Server.Transport != "" {
		cfg.transportName = parsed.Server.Transport
	}
	if parsed.Server.Shards != nil {
		cfg.shards = *parsed.Server.Shards
	}
	if parsed.Server.RegionSize != "" {
		sz, err := config.ParseSize(parsed.Server.RegionSize)
		if err != nil {
			slog.Error("invalid region_size in config", "region_size", parsed.Server.RegionSize, "error", err)
			os.Exit(1)
		}
		cfg.regionSize = sz
	}
	if parsed.Server.EvictCapacity != nil {
		cfg.evictCapacity = *parsed.Server.EvictCapacity
	}
	if parsed.Server.TLSCert != "" {
		cfg.tlsCert = parsed.Server.TLSCert
	}
	if parsed.Server.TLSKey != "" {
		cfg.tlsKey = parsed.Server.TLSKey
	}
	if parsed.Server.ShutdownTimeout != "" {
		dur, err := time.ParseDuration(parsed.Server.ShutdownTimeout)
		if err != nil {
			slog.Error("invalid shutdown_timeout in config", "shutdown_timeout", parsed.Server.ShutdownTimeout, "error", err)
			os.Exit(1)
		}
		cfg.shutdownTimeout = dur
	}
	if parsed.Storage.PersistentPath != "" {
		cfg.persistPath = parsed.Storage.PersistentPath
	}
	if parsed.Storage.Durable != nil {
		cfg.durable = *parsed.Storage.Durable
	}
	if parsed.Metrics.Addr != "" {
		cfg.metricsAddr = parsed.Metrics.Addr
	}
	if parsed.Compression.Algorithm != "" {
		cfg.compression = parsed.Compression.Algorithm
	}
	if parsed.Compression.MinSize != nil {
		cfg.minCompressSize = *parsed.Compression.MinSize
	}
	if parsed.Logging.Level != "" {
		cfg.logLevel = parsed.Logging.Level
	}
	if parsed.Logging.Format != "" {
		cfg.logFormat = parsed.Logging.Format
	}
	if parsed.Logging.SlowLogThreshold != "" {
		dur, err := time.ParseDuration(parsed.Logging.SlowLogThreshold)
		if err != nil {
			slog.Error("invalid logging.slow_log_threshold in config", "threshold", parsed.Logging.SlowLogThreshold, "error", err)
			os.Exit(1)
		}
		cfg.slowLogThreshold = dur
	}
	if parsed.Security.EncryptionKeyPath != "" {
		cfg.encryptionKeyPath = parsed.Security.EncryptionKeyPath
	}
}

func initCompressor(algorithm string, minSize int) compress.Compressor {
	switch algorithm {
	case "lz4":
		slog.Info("value compression enabled", "algorithm", "lz4", "min_size", minSize)
		return compress.NewLZ4(minSize)
	case "none", "":
		return compress.Noop{}
	default:
		slog.Error("unknown compression value", "compression", algorithm)
		os.Exit(1)
		return nil
	}
}

func initCipher(keyPath string) encrypt.Cipher {
	envKey := os.Getenv("HORREUM_KEY")
	var keyBytes []byte
	var err error

	if envKey != "" {
		keyBytes, err = parseEnvKey(envKey)
		if err != nil {
			slog.Error("failed to parse HORREUM_KEY env variable", "error", err)
			os.Exit(1)
		}
	} else {
		keyBytes = loadOrGenerateKeyFile(keyPath)
	}

	if len(keyBytes) == 0 {
		return encrypt.Noop{}
	}

	cip, err := encrypt.NewAESGCM(keyBytes)
	if err != nil {
		slog.Error("failed to initialize AES-GCM cipher", "error", err)
		os.Exit(1)
	}
	slog.Info("value encryption enabled", "algorithm", cip.Name())
	return cip
}

func parseEnvKey(envKey string) ([]byte, error) {
	switch len(envKey) {
	case 64:
		return hex.DecodeString(envKey)
	case 44:
		return base64.StdEncoding.DecodeString(envKey)
	case 32:
		return []byte(envKey), nil
	default:
		return nil, fmt.Errorf("env key has invalid length %d (expected 64 hex, 44 base64, or 32 raw bytes)", len(envKey))
	}
}

func loadOrGenerateKeyFile(keyPath string) []byte {
	if keyPath == "" {
		keyPath = "horreum.key"
	}

	if _, statErr := os.Stat(keyPath); errors.Is(statErr, os.ErrNotExist) {
		autoKey := make([]byte, 32)
		if _, genErr := rand.Read(autoKey); genErr != nil {
			slog.Error("failed to auto-generate encryption key", "error", genErr)
			os.Exit(1)
		}
		if writeErr := os.WriteFile(keyPath, autoKey, 0600); writeErr != nil {
			slog.Error("failed to save auto-generated encryption key", "path", keyPath, "error", writeErr)
			os.Exit(1)
		}
		slog.Info("auto-generated 32-byte encryption key file", "path", keyPath)
		return autoKey
	}

	keyBytes, err := encrypt.LoadKey(keyPath)
	if err != nil {
		slog.Error("failed to load encryption key from file", "path", keyPath, "error", err)
		os.Exit(1)
	}
	return keyBytes
}

type storageBackend struct {
	sets         *transport.ShardSet
	closeStorage func() error
	index        *index.HashIndex
}

func initStorage(persistPath string, durable bool, regionSize uint64, shards int, evictCapacity uint64, comp compress.Compressor, cip encrypt.Cipher) storageBackend {
	if persistPath != "" {
		return initPersistentStorage(persistPath, durable, regionSize, shards, evictCapacity, comp, cip)
	}

	slog.Info("starting in anonymous mode", "region_mib", regionSize>>20, "shards", shards)
	sets := transport.NewShardSetOrPanic(shards, regionSize, evictCapacity, comp, cip)
	return storageBackend{
		sets:         sets,
		closeStorage: func() error { return sets.Close() },
		index:        nil,
	}
}

func initPersistentStorage(persistPath string, durable bool, regionSize uint64, shards int, evictCapacity uint64, comp compress.Compressor, cip encrypt.Cipher) storageBackend {
	slog.Info("starting in persistent mode", "path", persistPath, "region_mib", regionSize>>20)
	pm, err := persist.New(persistPath, persist.Options{
		RegionSize:   regionSize,
		Durable:      durable,
		SyncInterval: time.Second,
	})
	if err != nil {
		slog.Error("failed to initialize persistent manager", "error", err)
		os.Exit(1)
	}

	sets := buildShardSetFromArena(pm.Manager(), shards, evictCapacity, comp, cip)
	idx := index.New(1 << 16)
	if err := pm.Replay(idx); err != nil {
		slog.Warn("WAL replay error (continuing)", "error", err)
	}
	slog.Info("cold start complete", "live_keys", idx.Count())

	return storageBackend{
		sets:  sets,
		index: idx,
		closeStorage: func() error {
			if err := pm.Sync(); err != nil {
				slog.Error("failed to sync persistent manager", "error", err)
			}
			if err := pm.Checkpoint(idx); err != nil {
				slog.Error("failed to checkpoint persistent manager", "error", err)
			}
			return pm.Close()
		},
	}
}

func initMetrics() {
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
}

func initServer(addr, transportName, tlsCert, tlsKey string, sets *transport.ShardSet, rec metrics.Recorder) *transport.Server {
	var tOpts transport.Options
	tOpts.Addr = addr
	tOpts.TransportName = transportName
	tOpts.Router = sets
	tOpts.Recorder = rec

	if transportName == "quic" {
		cert, err := tls.LoadX509KeyPair(tlsCert, tlsKey)
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
	slog.Info("horreum listening", "addr", srv.Addr().String(), "transport", transportName)
	return srv
}

func generateKeyAndExit(path string) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		fmt.Fprintf(os.Stderr, "failed to generate random key: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(path, key, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write key file %q: %v\n", path, err)
		os.Exit(1)
	}
	fmt.Printf("Generated 32-byte AES key file at %s\n", path)
}

func buildShardSetFromArena(mgr *arena.Manager, numShards int, evictCap uint64, comp compress.Compressor, cipher encrypt.Cipher) *transport.ShardSet {
	if numShards > 1 {
		slog.Info("persistent mode: forcing shards=1 (multi-shard persistent arenas not yet supported)")
		numShards = 1
	}
	return transport.NewShardSetFromArena(mgr, numShards, evictCap, comp, cipher)
}

func updateGauges(ctx context.Context, sets *transport.ShardSet, idx *index.HashIndex) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s := sets.Stats()
			metrics.SetGauge(memUsedGauge, s.UsedBytes)
			metrics.SetGauge(memFreeGauge, s.FreeBytes)
			if idx != nil {
				metrics.SetGauge(indexKeysGauge, uint64(idx.Count()))
			}
		}
	}
}

// Silence unused imports.
var _ net.Listener
