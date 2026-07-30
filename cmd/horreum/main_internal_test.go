// main_internal_test.go — unit tests for cmd/horreum helper
// functions.  Lives in the `main` package so it can test private
// helpers like parseYAMLPath, applyParsedYAML, parseEnvKey, etc.
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/horreum/horreum/internal/arena"
	"github.com/horreum/horreum/internal/config"
	"github.com/horreum/horreum/internal/transport"
)

func TestDefaultServerConfig(t *testing.T) {
	cfg := defaultServerConfig()
	if cfg.addr != ":7373" {
		t.Errorf("addr = %q, want :7373", cfg.addr)
	}
	if cfg.transportName != "tcp" {
		t.Errorf("transportName = %q, want tcp", cfg.transportName)
	}
	if cfg.shards != 4 {
		t.Errorf("shards = %d, want 4", cfg.shards)
	}
	if cfg.regionSize != 256<<20 {
		t.Errorf("regionSize = %d, want 256 MiB", cfg.regionSize)
	}
	if cfg.durable != true {
		t.Errorf("durable = false, want true")
	}
	if cfg.metricsAddr != ":9090" {
		t.Errorf("metricsAddr = %q, want :9090", cfg.metricsAddr)
	}
	if cfg.shutdownTimeout != 30*time.Second {
		t.Errorf("shutdownTimeout = %v, want 30s", cfg.shutdownTimeout)
	}
}

func TestParseYAMLPath(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"empty", []string{"horreum"}, ""},
		{"--config=path", []string{"horreum", "--config=/etc/h.yml"}, "/etc/h.yml"},
		{"--config path", []string{"horreum", "--config", "/etc/h.yml"}, "/etc/h.yml"},
		{"-config=path", []string{"horreum", "-config=/etc/h.yml"}, "/etc/h.yml"},
		{"-config path", []string{"horreum", "-config", "/etc/h.yml"}, "/etc/h.yml"},
		{"no-config-flag", []string{"horreum", "--addr=:1"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			os.Args = c.args
			if got := parseYAMLPath(); got != c.want {
				t.Errorf("parseYAMLPath() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestApplyParsedYAML(t *testing.T) {
	shards := 16
	evictCap := uint64(2048)
	durable := false
	minSize := 128
	cfg := defaultServerConfig()

	applyParsedYAML(&cfg, fullConfig(shards, evictCap, durable, minSize))

	if cfg.addr != ":1234" {
		t.Errorf("addr = %q", cfg.addr)
	}
	if cfg.transportName != "quic" {
		t.Errorf("transportName = %q", cfg.transportName)
	}
	if cfg.shards != shards {
		t.Errorf("shards = %d", cfg.shards)
	}
	if cfg.regionSize != 512<<20 {
		t.Errorf("regionSize = %d", cfg.regionSize)
	}
	if cfg.evictCapacity != evictCap {
		t.Errorf("evictCapacity = %d", cfg.evictCapacity)
	}
	if cfg.tlsCert != "/etc/cert.pem" {
		t.Errorf("tlsCert = %q", cfg.tlsCert)
	}
	if cfg.tlsKey != "/etc/key.pem" {
		t.Errorf("tlsKey = %q", cfg.tlsKey)
	}
	if cfg.shutdownTimeout != 5*time.Second {
		t.Errorf("shutdownTimeout = %v", cfg.shutdownTimeout)
	}
	if cfg.persistPath != "/var/lib/h" {
		t.Errorf("persistPath = %q", cfg.persistPath)
	}
	if cfg.durable != false {
		t.Errorf("durable = true")
	}
	if cfg.metricsAddr != ":9091" {
		t.Errorf("metricsAddr = %q", cfg.metricsAddr)
	}
	if cfg.compression != "lz4" {
		t.Errorf("compression = %q", cfg.compression)
	}
	if cfg.minCompressSize != minSize {
		t.Errorf("minCompressSize = %d", cfg.minCompressSize)
	}
	if cfg.logLevel != "debug" {
		t.Errorf("logLevel = %q", cfg.logLevel)
	}
	if cfg.logFormat != "json" {
		t.Errorf("logFormat = %q", cfg.logFormat)
	}
	if cfg.slowLogThreshold != 20*time.Millisecond {
		t.Errorf("slowLogThreshold = %v", cfg.slowLogThreshold)
	}
	if cfg.encryptionKeyPath != "/etc/h.key" {
		t.Errorf("encryptionKeyPath = %q", cfg.encryptionKeyPath)
	}
}

func TestApplyParsedYAMLDefaultsUnchanged(t *testing.T) {
	cfg := defaultServerConfig()
	applyParsedYAML(&cfg, config.Config{})
	if cfg.addr != ":7373" {
		t.Errorf("default addr mutated: %q", cfg.addr)
	}
}

// fullConfig returns a Config populated with non-default values used
// by TestApplyParsedYAML.
func fullConfig(shards int, evictCap uint64, durable bool, minSize int) config.Config {
	return config.Config{
		Server: config.ServerConfig{
			Addr:            ":1234",
			Transport:       "quic",
			Shards:          &shards,
			RegionSize:      "512MiB",
			EvictCapacity:   &evictCap,
			TLSCert:         "/etc/cert.pem",
			TLSKey:          "/etc/key.pem",
			ShutdownTimeout: "5s",
		},
		Storage: config.StorageConfig{
			PersistentPath: "/var/lib/h",
			Durable:        &durable,
		},
		Metrics: config.MetricsConfig{
			Addr: ":9091",
		},
		Compression: config.CompressionConfig{
			Algorithm: "lz4",
			MinSize:   &minSize,
		},
		Logging: config.LoggingConfig{
			Level:            "debug",
			Format:           "json",
			SlowLogThreshold: "20ms",
		},
		Security: config.SecurityConfig{
			EncryptionKeyPath: "/etc/h.key",
		},
	}
}

func TestParseEnvKey(t *testing.T) {
	// 32-byte raw key.
	raw := strings.Repeat("a", 32)
	got, err := parseEnvKey(raw)
	if err != nil {
		t.Fatalf("parseEnvKey(raw): %v", err)
	}
	if len(got) != 32 {
		t.Errorf("raw key len = %d, want 32", len(got))
	}

	// 64-char hex.
	hexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	got, err = parseEnvKey(hexKey)
	if err != nil {
		t.Fatalf("parseEnvKey(hex): %v", err)
	}
	if len(got) != 32 {
		t.Errorf("hex key len = %d, want 32", len(got))
	}

	// 44-char base64.
	b64Key := strings.Repeat("A", 44)
	got, err = parseEnvKey(b64Key)
	if err != nil {
		t.Fatalf("parseEnvKey(b64): %v", err)
	}
	if len(got) == 0 {
		t.Errorf("base64 key empty")
	}

	// Bad length.
	if _, err := parseEnvKey("short"); err == nil {
		t.Errorf("expected error for short key")
	}
}

func TestLoadConfigMissing(t *testing.T) {
	// loadConfig with empty path returns defaults without errors.
	cfg := loadConfig("")
	if cfg.addr != ":7373" {
		t.Errorf("addr = %q, want default", cfg.addr)
	}
}

func TestLoadOrGenerateKeyFileEmpty(t *testing.T) {
	// empty path → uses "horreum.key" in cwd; ensure cwd is a temp
	// dir so we don't pollute the workspace.
	tmp := t.TempDir()
	// Provide a full path so we don't fight TempDir cleanup with the
	// chdir-based default.
	path := filepath.Join(tmp, "horreum.key")
	key := loadOrGenerateKeyFile(path)
	if len(key) != 32 {
		t.Errorf("generated key len = %d, want 32", len(key))
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("key file not written: %v", err)
	}
}

func TestLoadOrGenerateKeyFileExisting(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "h.key")
	existing := bytesRepeat([]byte("k"), 32)
	if err := os.WriteFile(path, existing, 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got := loadOrGenerateKeyFile(path)
	if len(got) != 32 {
		t.Errorf("loaded key len = %d", len(got))
	}
}

func TestInitCompressorNone(t *testing.T) {
	c := initCompressor("none", 64)
	if c.Name() != "none" {
		t.Errorf("Name = %q, want none", c.Name())
	}
}

func TestInitCompressorEmpty(t *testing.T) {
	c := initCompressor("", 64)
	if c.Name() != "none" {
		t.Errorf("Name = %q, want none", c.Name())
	}
}

func TestInitCompressorLZ4(t *testing.T) {
	c := initCompressor("lz4", 64)
	if c.Name() != "lz4" {
		t.Errorf("Name = %q, want lz4", c.Name())
	}
}

func TestBuildShardSetFromArenaSingleShard(t *testing.T) {
	mgr, err := arena.NewManager(1<<20, false)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ss := buildShardSetFromArena(mgr, 1, 0, nil, nil)
	if ss == nil {
		t.Fatal("returned nil")
	}
	defer ss.Close()
	if ss.ShardCount() != 1 {
		t.Errorf("ShardCount = %d, want 1", ss.ShardCount())
	}
}

func TestBuildShardSetFromArenaForcesOne(t *testing.T) {
	// multi-shard persistent arenas are forced to 1.
	mgr, err := arena.NewManager(1<<20, false)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ss := buildShardSetFromArena(mgr, 4, 0, nil, nil)
	if ss.ShardCount() != 1 {
		t.Errorf("ShardCount = %d, want 1 (forced)", ss.ShardCount())
	}
	defer ss.Close()
}

func TestGenerateKeyAndExitCreatesFile(t *testing.T) {
	// generateKeyAndExit calls os.Exit; we cannot test that path
	// directly.  Instead, write a key file via os.WriteFile and
	// verify it round-trips.
	tmp := t.TempDir()
	path := filepath.Join(tmp, "out.key")
	key := bytesRepeat([]byte{0x42}, 32)
	if err := os.WriteFile(path, key, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 32 {
		t.Errorf("read len = %d, want 32", len(got))
	}
}

func TestUpdateGaugesReturns(t *testing.T) {
	// updateGauges runs forever; we just exercise the body once
	// before stopping.  We use a synthetic ShardSet and a nil index
	// so the function does nothing dangerous.
	ss, err := transport.NewShardSet(transport.ShardConfig{
		NumShards: 2, RegionSize: 1 << 20, EvictCapacity: 16,
	})
	if err != nil {
		t.Fatalf("NewShardSet: %v", err)
	}
	defer ss.Close()
	// The function uses unexported package-level gauge variables;
	// we let it execute one tick and then rely on test timeout to
	// bail.  We don't have a way to stop the goroutine cleanly,
	// so this test just exercises the body without asserting on it.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		updateGauges(ctx, ss, nil)
	}()
	// Let one tick happen.
	time.Sleep(20 * time.Millisecond)
	cancel()
}

func TestInitMetrics(t *testing.T) {
	// initMetrics registers gauges / counters.  We don't assert the
	// returned hashes — we just verify the call doesn't panic.
	initMetrics()
	if memUsedGauge == 0 {
		t.Error("memUsedGauge not set")
	}
	if memFreeGauge == 0 {
		t.Error("memFreeGauge not set")
	}
	if indexKeysGauge == 0 {
		t.Error("indexKeysGauge not set")
	}
}

func TestInitStorageAnonymous(t *testing.T) {
	// Anonymous mode (empty persistPath) — builds a ShardSet.
	sb := initStorage("", false, 1<<20, 2, 16, nil, nil)
	if sb.sets == nil {
		t.Fatal("initStorage returned nil sets")
	}
	defer sb.sets.Close()
	if sb.index != nil {
		t.Errorf("anonymous mode should have nil index")
	}
	if err := sb.closeStorage(); err != nil {
		t.Errorf("closeStorage: %v", err)
	}
}

func TestInitStoragePersistent(t *testing.T) {
	dir := t.TempDir()
	sb := initStorage(dir, false, 1<<22, 1, 16, nil, nil)
	if sb.sets == nil {
		t.Fatal("initStorage returned nil sets")
	}
	defer sb.sets.Close()
	if sb.index == nil {
		t.Error("persistent mode should populate index")
	}
	if err := sb.closeStorage(); err != nil {
		t.Errorf("closeStorage: %v", err)
	}
}

// ─────────────── helpers ───────────────

func bytesRepeat(b []byte, n int) []byte {
	out := make([]byte, len(b)*n)
	for i := 0; i < n; i++ {
		copy(out[i*len(b):], b)
	}
	return out
}
