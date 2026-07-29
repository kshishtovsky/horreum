package logger

import (
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	// GlobalLogLevel holds the currently configured log level.
	GlobalLogLevel = slog.LevelInfo

	// SlowLogThreshold holds the duration threshold above which operations
	// are logged as slow (using Warn level).
	SlowLogThreshold = 10 * time.Millisecond
)

// RateLimiter implements a simple thread-safe token bucket rate limiter.
// It is used to prevent hot-path errors (like network packet errors)
// from spamming logs and hurting disk I/O.
type RateLimiter struct {
	mu         sync.Mutex
	rate       float64 // tokens refilled per second
	capacity   float64 // maximum tokens in the bucket
	tokens     float64 // current token count
	lastRefill time.Time
}

// NewRateLimiter constructs a new RateLimiter with the specified rate and burst capacity.
func NewRateLimiter(rate, capacity float64) *RateLimiter {
	return &RateLimiter{
		rate:       rate,
		capacity:   capacity,
		tokens:     capacity,
		lastRefill: time.Now(),
	}
}

// Allow returns true if a token was successfully consumed from the bucket, false otherwise.
func (rl *RateLimiter) Allow() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(rl.lastRefill).Seconds()
	rl.lastRefill = now

	// Refill tokens based on time elapsed
	rl.tokens += elapsed * rl.rate
	if rl.tokens > rl.capacity {
		rl.tokens = rl.capacity
	}

	if rl.tokens >= 1.0 {
		rl.tokens -= 1.0
		return true
	}
	return false
}

// NetErrorLimiter limits logging of connection and protocol errors to prevent spam.
// Allows at most 5 log messages per second with a burst capacity of 10.
var NetErrorLimiter = NewRateLimiter(5.0, 10.0)

// Init configures the default structured slog logger based on parameters.
func Init(levelStr, formatStr string, slowThreshold time.Duration) {
	var level slog.Level
	switch strings.ToLower(levelStr) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	GlobalLogLevel = level

	if slowThreshold > 0 {
		SlowLogThreshold = slowThreshold
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}

	var handler slog.Handler
	if strings.ToLower(formatStr) == "json" {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}

	slog.SetDefault(slog.New(handler))
}
