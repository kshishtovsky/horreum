package compress

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"
)

// ── Table-driven round-trip tests ──────────────────────────────────

func TestLZ4RoundTrip(t *testing.T) {
	c := NewLZ4(1) // minSize=1 to test small inputs too

	tests := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"1byte", []byte{0x42}},
		{"4bytes", []byte{1, 2, 3, 4}},
		{"zeros_64", make([]byte, 64)},
		{"zeros_4K", make([]byte, 4096)},
		{"repeating_256", bytes.Repeat([]byte{0xAB, 0xCD}, 128)},
		{"text_short", []byte("the quick brown fox jumps over the lazy dog")},
		{"text_repeated", []byte(strings.Repeat("hello world! ", 100))},
		{"ascending_1K", ascending(1024)},
		{"mixed_64K", mixedPattern(64 * 1024)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compressed, ok := c.Compress(tt.data)
			if !ok {
				// Incompressible or too small — that's fine, skip round-trip.
				t.Logf("not compressed (len=%d)", len(tt.data))
				return
			}
			defer c.PutBuf(compressed)

			decompressed, err := c.Decompress(compressed)
			if err != nil {
				t.Fatalf("Decompress: %v", err)
			}
			defer c.PutBuf(decompressed)

			if !bytes.Equal(decompressed, tt.data) {
				t.Fatalf("round-trip mismatch: got %d bytes, want %d",
					len(decompressed), len(tt.data))
			}
		})
	}
}

func TestLZ4IncompressibleData(t *testing.T) {
	c := NewLZ4(1)
	random := make([]byte, 4096)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}

	compressed, ok := c.Compress(random)
	if ok {
		// If it compressed, the result must be smaller.
		if len(compressed) >= len(random) {
			t.Fatalf("compressed (%d) >= original (%d)", len(compressed), len(random))
		}
		c.PutBuf(compressed)
	}
}

func TestLZ4MinSizeThreshold(t *testing.T) {
	c := NewLZ4(64)

	// 32-byte input should not be compressed.
	small := bytes.Repeat([]byte{0x00}, 32)
	_, ok := c.Compress(small)
	if ok {
		t.Fatal("expected small input to be skipped")
	}

	// 128-byte zeros should compress.
	large := bytes.Repeat([]byte{0x00}, 128)
	compressed, ok := c.Compress(large)
	if !ok {
		t.Fatal("expected 128-byte zeros to compress")
	}
	c.PutBuf(compressed)
}

func TestLZ4DecompressCorrupt(t *testing.T) {
	c := NewLZ4(1)

	tests := []struct {
		name string
		data []byte
	}{
		{"too_short", []byte{1, 2}},
		{"empty_after_prefix", []byte{0x80, 0, 0, 0}}, // claims 128 bytes
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := c.Decompress(tt.data)
			if err == nil {
				t.Fatal("expected error on corrupt input")
			}
		})
	}
}

func TestLZ4LargePayload(t *testing.T) {
	c := NewLZ4(1)
	// 1 MiB of repeating pattern — highly compressible.
	data := bytes.Repeat([]byte("ABCDEFGHIJKLMNOP"), 64*1024)

	compressed, ok := c.Compress(data)
	if !ok {
		t.Fatal("expected large payload to compress")
	}
	defer c.PutBuf(compressed)

	ratio := float64(len(compressed)) / float64(len(data))
	t.Logf("1 MiB repeating: compressed to %.1f%% (%.0f:1)",
		ratio*100, 1/ratio)

	decompressed, err := c.Decompress(compressed)
	if err != nil {
		t.Fatalf("Decompress: %v", err)
	}
	defer c.PutBuf(decompressed)

	if !bytes.Equal(decompressed, data) {
		t.Fatal("round-trip mismatch on large payload")
	}
}

func TestNoopCompressor(t *testing.T) {
	n := Noop{}
	if n.Name() != "none" {
		t.Fatalf("expected name 'none', got %q", n.Name())
	}
	_, ok := n.Compress([]byte("test"))
	if ok {
		t.Fatal("Noop.Compress should always return false")
	}
	out, err := n.Decompress([]byte("test"))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "test" {
		t.Fatalf("Noop.Decompress mismatch")
	}
	n.PutBuf(nil) // should not panic
}

// ── Fuzz test ──────────────────────────────────────────────────────

func FuzzLZ4RoundTrip(f *testing.F) {
	f.Add([]byte("hello world"))
	f.Add([]byte(strings.Repeat("x", 256)))
	f.Add(make([]byte, 100))
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7})

	c := NewLZ4(1)
	f.Fuzz(func(t *testing.T, data []byte) {
		compressed, ok := c.Compress(data)
		if !ok {
			return
		}
		defer c.PutBuf(compressed)

		decompressed, err := c.Decompress(compressed)
		if err != nil {
			t.Fatalf("Decompress error: %v (input len=%d)", err, len(data))
		}
		defer c.PutBuf(decompressed)

		if !bytes.Equal(decompressed, data) {
			t.Fatalf("round-trip mismatch: input len=%d, decompressed len=%d",
				len(data), len(decompressed))
		}
	})
}

// ── Helpers ────────────────────────────────────────────────────────

func ascending(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

func mixedPattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		switch {
		case i%1024 < 256:
			b[i] = 0 // zeros
		case i%1024 < 512:
			b[i] = byte(i) // ascending
		default:
			b[i] = byte(i * 7) // pseudo-random
		}
	}
	return b
}

// ── Benchmark ──────────────────────────────────────────────────────

func BenchmarkLZ4Compress(b *testing.B) {
	sizes := []int{256, 4096, 64 * 1024, 1 << 20}
	for _, size := range sizes {
		data := bytes.Repeat([]byte("The quick brown fox jumps. "), size/26+1)
		data = data[:size]
		c := NewLZ4(1)

		b.Run(fmt.Sprintf("%dB", size), func(b *testing.B) {
			b.SetBytes(int64(size))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				compressed, ok := c.Compress(data)
				if ok {
					c.PutBuf(compressed)
				}
			}
		})
	}
}

func BenchmarkLZ4Decompress(b *testing.B) {
	sizes := []int{256, 4096, 64 * 1024, 1 << 20}
	for _, size := range sizes {
		data := bytes.Repeat([]byte("The quick brown fox jumps. "), size/26+1)
		data = data[:size]
		c := NewLZ4(1)
		compressed, ok := c.Compress(data)
		if !ok {
			b.Skipf("could not compress %d bytes", size)
		}
		// Make a stable copy for the benchmark.
		compCopy := make([]byte, len(compressed))
		copy(compCopy, compressed)
		c.PutBuf(compressed)

		b.Run(fmt.Sprintf("%dB", size), func(b *testing.B) {
			b.SetBytes(int64(size))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				out, err := c.Decompress(compCopy)
				if err != nil {
					b.Fatal(err)
				}
				c.PutBuf(out)
			}
		})
	}
}
