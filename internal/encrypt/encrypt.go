// Package encrypt provides optional authenticated value encryption for the cache.
//
// Authenticated encryption (AEAD) is implemented using AES-256-GCM.  Each
// encrypted value carries a unique 12-byte random nonce and a 16-byte
// authentication tag, guaranteeing confidentiality and integrity.
//
// Key management supports reading keys from a secure file or using an
// environment variable.
package encrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
)

// Cipher defines the interface for value encryption and decryption.
// Implementations must be safe for concurrent use.
type Cipher interface {
	// Encrypt encrypts src and appends the result to dst.
	// The returned slice contains: [12-byte Nonce][Ciphertext][16-byte Auth Tag].
	Encrypt(dst, src []byte) ([]byte, error)

	// Decrypt decrypts src and appends the result to dst.
	Decrypt(dst, src []byte) ([]byte, error)

	// Name returns the cipher algorithm name ("aes-256-gcm", "none").
	Name() string
}

// AESGCMCipher implements value encryption using AES-256-GCM.
type AESGCMCipher struct {
	aead cipher.AEAD
}

// NewAESGCM constructs an AESGCMCipher using the provided 32-byte key.
func NewAESGCM(key []byte) (*AESGCMCipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("encrypt: invalid key length %d, expected 32 bytes for AES-256", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("encrypt: failed to create AES block cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("encrypt: failed to create GCM mode: %w", err)
	}
	return &AESGCMCipher{aead: aead}, nil
}

// Name returns "aes-256-gcm".
func (c *AESGCMCipher) Name() string { return "aes-256-gcm" }

const nonceSize = 12

// Encrypt encrypts src into dst.
func (c *AESGCMCipher) Encrypt(dst, src []byte) ([]byte, error) {
	// Grow dst to accommodate nonce + encrypted payload + tag.
	// GCM ciphertext length is len(src) + 16 (tag size).
	needed := len(dst) + nonceSize + len(src) + c.aead.Overhead()
	if cap(dst) < needed {
		newDst := make([]byte, len(dst), needed*2)
		copy(newDst, dst)
		dst = newDst
	}

	origLen := len(dst)
	dst = dst[:origLen+nonceSize]
	nonce := dst[origLen : origLen+nonceSize]

	// Generate cryptographically secure random nonce
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("encrypt: failed to generate nonce: %w", err)
	}

	// Encrypt and append to dst (after the nonce)
	result := c.aead.Seal(dst, nonce, src, nil)
	return result, nil
}

// Decrypt decrypts src into dst.
func (c *AESGCMCipher) Decrypt(dst, src []byte) ([]byte, error) {
	if len(src) < nonceSize+c.aead.Overhead() {
		return nil, errors.New("encrypt: ciphertext too short")
	}

	nonce := src[:nonceSize]
	ciphertext := src[nonceSize:]

	// Open decrypts and authenticates ciphertext.
	// If authentication fails, it returns an error.
	result, err := c.aead.Open(dst, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("encrypt: decryption failed (possible data tampering): %w", err)
	}
	return result, nil
}

// ──────────────────────────────────────────────────────────────────
// Noop — pass-through cipher.
// ──────────────────────────────────────────────────────────────────

// Noop is a pass-through cipher that does not encrypt or decrypt.
type Noop struct{}

// Encrypt appends src to dst as-is.
func (Noop) Encrypt(dst, src []byte) ([]byte, error) {
	return append(dst, src...), nil
}

// Decrypt appends src to dst as-is.
func (Noop) Decrypt(dst, src []byte) ([]byte, error) {
	return append(dst, src...), nil
}

// Name returns "none".
func (Noop) Name() string { return "none" }

// ──────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────

// LoadKey reads a 32-byte encryption key from the specified file path.
func LoadKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("encrypt: key file %q must contain exactly 32 bytes (got %d)", path, len(key))
	}
	return key, nil
}
