package config

import (
	"testing"
)

func TestParseYAML(t *testing.T) {
	yamlData := []byte(`
# This is a comment
server:
  addr: ":7373"
  transport: 'tcp' # inline comment
  shards: 16
  region_size: "2GiB"
  evict_capacity: 8192
  tls_cert: "cert.pem"
  tls_key: 'key.pem'
  shutdown_timeout: 45s

storage:
  persistent_path: "/var/lib/horreum"
  durable: yes

metrics:
  addr: ":9091"

compression:
  algorithm: lz4
  min_size: 128

logging:
  level: debug
  format: json
  slow_log_threshold: 20ms
`)

	cfg, err := ParseYAML(yamlData)
	if err != nil {
		t.Fatalf("ParseYAML failed: %v", err)
	}

	// Verify ServerConfig
	if cfg.Server.Addr != ":7373" {
		t.Errorf("expected server.addr ':7373', got %q", cfg.Server.Addr)
	}
	if cfg.Server.Transport != "tcp" {
		t.Errorf("expected server.transport 'tcp', got %q", cfg.Server.Transport)
	}
	if cfg.Server.Shards == nil || *cfg.Server.Shards != 16 {
		t.Errorf("expected server.shards 16, got %v", cfg.Server.Shards)
	}
	if cfg.Server.RegionSize != "2GiB" {
		t.Errorf("expected server.region_size '2GiB', got %q", cfg.Server.RegionSize)
	}
	if cfg.Server.EvictCapacity == nil || *cfg.Server.EvictCapacity != 8192 {
		t.Errorf("expected server.evict_capacity 8192, got %v", cfg.Server.EvictCapacity)
	}
	if cfg.Server.TLSCert != "cert.pem" {
		t.Errorf("expected server.tls_cert 'cert.pem', got %q", cfg.Server.TLSCert)
	}
	if cfg.Server.TLSKey != "key.pem" {
		t.Errorf("expected server.tls_key 'key.pem', got %q", cfg.Server.TLSKey)
	}
	if cfg.Server.ShutdownTimeout != "45s" {
		t.Errorf("expected server.shutdown_timeout '45s', got %q", cfg.Server.ShutdownTimeout)
	}

	// Verify StorageConfig
	if cfg.Storage.PersistentPath != "/var/lib/horreum" {
		t.Errorf("expected storage.persistent_path '/var/lib/horreum', got %q", cfg.Storage.PersistentPath)
	}
	if cfg.Storage.Durable == nil || !*cfg.Storage.Durable {
		t.Errorf("expected storage.durable true, got %v", cfg.Storage.Durable)
	}

	// Verify MetricsConfig
	if cfg.Metrics.Addr != ":9091" {
		t.Errorf("expected metrics.addr ':9091', got %q", cfg.Metrics.Addr)
	}

	// Verify CompressionConfig
	if cfg.Compression.Algorithm != "lz4" {
		t.Errorf("expected compression.algorithm 'lz4', got %q", cfg.Compression.Algorithm)
	}
	if cfg.Compression.MinSize == nil || *cfg.Compression.MinSize != 128 {
		t.Errorf("expected compression.min_size 128, got %v", cfg.Compression.MinSize)
	}

	// Verify LoggingConfig
	if cfg.Logging.Level != "debug" {
		t.Errorf("expected logging.level 'debug', got %q", cfg.Logging.Level)
	}
	if cfg.Logging.Format != "json" {
		t.Errorf("expected logging.format 'json', got %q", cfg.Logging.Format)
	}
	if cfg.Logging.SlowLogThreshold != "20ms" {
		t.Errorf("expected logging.slow_log_threshold '20ms', got %q", cfg.Logging.SlowLogThreshold)
	}
}

func TestParseYAML_Errors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{"missing colon", "server\n  addr: :7373"},
		{"invalid shards", "server:\n  shards: abc"},
		{"invalid capacity", "server:\n  evict_capacity: abc"},
		{"invalid durable", "storage:\n  durable: maybe"},
		{"invalid min_size", "compression:\n  min_size: abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseYAML([]byte(tt.yaml))
			if err == nil {
				t.Error("expected error but got nil")
			}
		})
	}
}

func TestParseSize(t *testing.T) {
	tests := []struct {
		input    string
		expected uint64
		hasErr   bool
	}{
		{"", 0, false},
		{"1024", 1024, false},
		{"1b", 1, false},
		{"2.5kb", 2500, false},
		{"64mb", 64 * 1000 * 1000, false},
		{"1.2gb", 1200 * 1000 * 1000, false},
		{"4tb", 4 * 1000 * 1000 * 1000 * 1000, false},
		{"1kib", 1024, false},
		{"512mib", 512 * 1024 * 1024, false},
		{"2gib", 2 * 1024 * 1024 * 1024, false},
		{"1tib", 1024 * 1024 * 1024 * 1024, false},
		{"  1.5 GiB  ", 1610612736, false},
		{"abc", 0, true},
		{"10unknown", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			res, err := ParseSize(tt.input)
			if (err != nil) != tt.hasErr {
				t.Fatalf("expected error: %t, got: %v", tt.hasErr, err)
			}
			if !tt.hasErr && res != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, res)
			}
		})
	}
}
