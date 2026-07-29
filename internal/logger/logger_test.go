package logger

import (
	"testing"
	"time"
)

func TestInitLogger(t *testing.T) {
	levels := []string{"debug", "info", "warn", "error", "unknown"}
	formats := []string{"text", "json"}

	for _, l := range levels {
		for _, f := range formats {
			Init(l, f, 5*time.Millisecond)
		}
	}
}

func TestRateLimiter(t *testing.T) {
	rl := NewRateLimiter(10.0, 2.0)

	// First 2 tokens should be allowed immediately (capacity=2)
	if !rl.Allow() {
		t.Error("expected first token to be allowed")
	}
	if !rl.Allow() {
		t.Error("expected second token to be allowed")
	}

	// 3rd token should be denied immediately
	if rl.Allow() {
		t.Error("expected 3rd token to be denied (bucket empty)")
	}

	// Wait for refill
	time.Sleep(150 * time.Millisecond)
	if !rl.Allow() {
		t.Error("expected token to be allowed after refill")
	}
}
