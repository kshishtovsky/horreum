package config

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// ServerConfig configures core server and network parameters.
type ServerConfig struct {
	Addr            string
	Transport       string
	Shards          *int
	RegionSize      string // e.g. "1GiB" or "512MiB"
	EvictCapacity   *uint64
	TLSCert         string
	TLSKey          string
	ShutdownTimeout string // e.g. "30s"
}

// StorageConfig configures the persist/WAL parameters.
type StorageConfig struct {
	PersistentPath string
	Durable        *bool
}

// MetricsConfig configures the Prometheus exporter.
type MetricsConfig struct {
	Addr string
}

// CompressionConfig configures optional LZ4 value compression.
type CompressionConfig struct {
	Algorithm string // "none" or "lz4"
	MinSize   *int   // minimum byte size to compress
}

// Config represents the complete application configuration hierarchy.
type LoggingConfig struct {
	Level            string
	Format           string
	SlowLogThreshold string
}

type SecurityConfig struct {
	EncryptionKeyPath string
}

type Config struct {
	Server      ServerConfig
	Storage     StorageConfig
	Metrics     MetricsConfig
	Compression CompressionConfig
	Logging     LoggingConfig
	Security    SecurityConfig
}

type stackItem struct {
	indent int
	key    string
}

// ParseYAML decodes a subset of YAML into a Config struct using only stdlib.
// It supports nested mapping sections based on line indentation (spaces/tabs).
func ParseYAML(data []byte) (*Config, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	lineNum := 0
	var stack []stackItem
	kv := make(map[string]string)

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Trim trailing whitespace and carriage returns
		line = strings.TrimRight(line, " \t\r\n")

		// Count leading indent size
		indent := 0
		for indent < len(line) && (line[indent] == ' ' || line[indent] == '\t') {
			indent++
		}

		trimmed := line[indent:]
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue // skip empty lines or comments
		}

		// Handle inline comments: remove suffix starting with '#'
		// only if it is preceded by whitespace.
		if idx := strings.Index(trimmed, "#"); idx != -1 {
			if idx == 0 || trimmed[idx-1] == ' ' || trimmed[idx-1] == '\t' {
				trimmed = strings.TrimRight(trimmed[:idx], " \t")
			}
		}

		if trimmed == "" {
			continue
		}

		colonIdx := strings.Index(trimmed, ":")
		if colonIdx == -1 {
			return nil, fmt.Errorf("line %d: missing colon in YAML line: %q", lineNum, line)
		}

		key := strings.TrimSpace(trimmed[:colonIdx])
		val := strings.TrimSpace(trimmed[colonIdx+1:])

		// Strip quotes from the key if any
		key = strings.Trim(key, `"'`)

		// Pop stack items that are at the same or deeper indentation level
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}

		if val == "" && strings.HasSuffix(trimmed, ":") {
			// This line represents a nested map section (e.g. "server:")
			stack = append(stack, stackItem{indent: indent, key: key})
		} else {
			// Strip quotes from value
			if (strings.HasPrefix(val, `"`) && strings.HasSuffix(val, `"`)) ||
				(strings.HasPrefix(val, `'`) && strings.HasSuffix(val, `'`)) {
				val = val[1 : len(val)-1]
			}

			// Construct hierarchal full key path, e.g. "server.addr"
			var fullKeyParts []string
			for _, item := range stack {
				fullKeyParts = append(fullKeyParts, item.key)
			}
			fullKeyParts = append(fullKeyParts, key)
			fullKey := strings.Join(fullKeyParts, ".")
			kv[fullKey] = val
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	cfg := &Config{}

	// Map key-value pairs to the Config structure
	// Server
	if v, ok := kv["server.addr"]; ok {
		cfg.Server.Addr = v
	}
	if v, ok := kv["server.transport"]; ok {
		cfg.Server.Transport = v
	}
	if v, ok := kv["server.shards"]; ok {
		shards, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid server.shards %q: %w", v, err)
		}
		cfg.Server.Shards = &shards
	}
	if v, ok := kv["server.region_size"]; ok {
		cfg.Server.RegionSize = v
	}
	if v, ok := kv["server.evict_capacity"]; ok {
		capVal, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid server.evict_capacity %q: %w", v, err)
		}
		cfg.Server.EvictCapacity = &capVal
	}
	if v, ok := kv["server.tls_cert"]; ok {
		cfg.Server.TLSCert = v
	}
	if v, ok := kv["server.tls_key"]; ok {
		cfg.Server.TLSKey = v
	}
	if v, ok := kv["server.shutdown_timeout"]; ok {
		cfg.Server.ShutdownTimeout = v
	}

	// Storage
	if v, ok := kv["storage.persistent_path"]; ok {
		cfg.Storage.PersistentPath = v
	}
	if v, ok := kv["storage.durable"]; ok {
		durable, err := parseBool(v)
		if err != nil {
			return nil, fmt.Errorf("invalid storage.durable %q: %w", v, err)
		}
		cfg.Storage.Durable = &durable
	}

	// Metrics
	if v, ok := kv["metrics.addr"]; ok {
		cfg.Metrics.Addr = v
	}

	// Compression
	if v, ok := kv["compression.algorithm"]; ok {
		cfg.Compression.Algorithm = v
	}
	if v, ok := kv["compression.min_size"]; ok {
		minSize, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid compression.min_size %q: %w", v, err)
		}
		cfg.Compression.MinSize = &minSize
	}

	// Logging
	if v, ok := kv["logging.level"]; ok {
		cfg.Logging.Level = v
	}
	if v, ok := kv["logging.format"]; ok {
		cfg.Logging.Format = v
	}
	if v, ok := kv["logging.slow_log_threshold"]; ok {
		cfg.Logging.SlowLogThreshold = v
	}

	// Security
	if v, ok := kv["security.encryption_key_path"]; ok {
		cfg.Security.EncryptionKeyPath = v
	}

	return cfg, nil
}

func parseBool(s string) (bool, error) {
	s = strings.ToLower(s)
	switch s {
	case "true", "yes", "on", "1":
		return true, nil
	case "false", "no", "off", "0":
		return false, nil
	default:
		return false, fmt.Errorf("cannot parse as bool: %q", s)
	}
}

// ParseSize converts strings representing memory sizes (like "1GiB", "512MiB", "64MB")
// into a raw byte count (uint64). It accepts base-10 (MB, GB) and base-2 (MiB, GiB) units.
func ParseSize(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}

	// Split numeric part and unit part
	idx := 0
	for idx < len(s) && ((s[idx] >= '0' && s[idx] <= '9') || s[idx] == '.') {
		idx++
	}
	numStr := s[:idx]
	unitStr := strings.ToLower(strings.TrimSpace(s[idx:]))

	val, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size number: %q", numStr)
	}

	var multiplier float64 = 1
	switch unitStr {
	case "b", "":
		multiplier = 1
	case "k", "kb":
		multiplier = 1e3
	case "m", "mb":
		multiplier = 1e6
	case "g", "gb":
		multiplier = 1e9
	case "t", "tb":
		multiplier = 1e12
	case "ki", "kib":
		multiplier = 1024
	case "mi", "mib":
		multiplier = 1024 * 1024
	case "gi", "gib":
		multiplier = 1024 * 1024 * 1024
	case "ti", "tib":
		multiplier = 1024 * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unknown size unit: %q", unitStr)
	}

	return uint64(val * multiplier), nil
}
