package encrypt

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestAESGCMCipher_RoundTrip(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}

	c, err := NewAESGCM(key)
	if err != nil {
		t.Fatalf("NewAESGCM failed: %v", err)
	}

	if c.Name() != "aes-256-gcm" {
		t.Errorf("expected name 'aes-256-gcm', got %q", c.Name())
	}

	plaintext := []byte("secret payload value to encrypt in cache")

	// Encrypt
	ciphertext, err := c.Encrypt(nil, plaintext)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	if len(ciphertext) <= len(plaintext) {
		t.Errorf("expected ciphertext to be longer than plaintext (due to nonce and auth tag)")
	}

	// Decrypt
	decrypted, err := c.Decrypt(nil, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted value mismatch: got %q, want %q", string(decrypted), string(plaintext))
	}
}

func TestAESGCMCipher_Tampering(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)

	c, _ := NewAESGCM(key)
	plaintext := []byte("super secret data")

	ciphertext, _ := c.Encrypt(nil, plaintext)

	// Tamper with the ciphertext (flip a byte)
	tampered := make([]byte, len(ciphertext))
	copy(tampered, ciphertext)
	tampered[len(tampered)-1] ^= 0xFF

	_, err := c.Decrypt(nil, tampered)
	if err == nil {
		t.Fatal("expected decryption to fail on tampered data")
	}
}

func TestAESGCMCipher_InvalidKeyLength(t *testing.T) {
	invalidKeys := [][]byte{
		nil,
		make([]byte, 16), // AES-128 (not supported)
		make([]byte, 24), // AES-192 (not supported)
		make([]byte, 31),
		make([]byte, 33),
	}

	for _, k := range invalidKeys {
		_, err := NewAESGCM(k)
		if err == nil {
			t.Errorf("expected error for key length %d, got nil", len(k))
		}
	}
}

func TestNoopCipher(t *testing.T) {
	n := Noop{}
	if n.Name() != "none" {
		t.Errorf("expected name 'none', got %q", n.Name())
	}

	data := []byte("raw data")
	enc, err := n.Encrypt(nil, data)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(enc, data) {
		t.Error("Noop.Encrypt should not modify data")
	}

	dec, err := n.Decrypt(nil, enc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dec, data) {
		t.Error("Noop.Decrypt should not modify data")
	}
}

func TestLoadKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.bin")

	// 1. File doesn't exist
	_, err := LoadKey(keyPath)
	if err == nil {
		t.Error("expected error for non-existent file")
	}

	// 2. File too short
	os.WriteFile(keyPath, make([]byte, 16), 0600)
	_, err = LoadKey(keyPath)
	if err == nil {
		t.Error("expected error for 16-byte key")
	}

	// 3. Valid key
	validKey := make([]byte, 32)
	rand.Read(validKey)
	os.WriteFile(keyPath, validKey, 0600)
	k, err := LoadKey(keyPath)
	if err != nil {
		t.Fatalf("LoadKey failed: %v", err)
	}
	if !bytes.Equal(k, validKey) {
		t.Error("loaded key mismatch")
	}
}

// ── Benchmarks ──────────────────────────────────────────────────────

func BenchmarkAESGCM_Encrypt(b *testing.B) {
	key := make([]byte, 32)
	rand.Read(key)
	c, _ := NewAESGCM(key)

	sizes := []int{256, 4096, 64 * 1024, 1 << 20}
	for _, size := range sizes {
		data := make([]byte, size)
		rand.Read(data)

		b.Run(fmt.Sprintf("%dB", size), func(b *testing.B) {
			b.SetBytes(int64(size))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, err := c.Encrypt(nil, data)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkAESGCM_Decrypt(b *testing.B) {
	key := make([]byte, 32)
	rand.Read(key)
	c, _ := NewAESGCM(key)

	sizes := []int{256, 4096, 64 * 1024, 1 << 20}
	for _, size := range sizes {
		data := make([]byte, size)
		rand.Read(data)
		enc, err := c.Encrypt(nil, data)
		if err != nil {
			b.Fatal(err)
		}

		b.Run(fmt.Sprintf("%dB", size), func(b *testing.B) {
			b.SetBytes(int64(size))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, err := c.Decrypt(nil, enc)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
