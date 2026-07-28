package proto

import (
	"errors"
	"testing"
)

// FuzzParseFrame feeds arbitrary bytes into the streaming parser. The fuzz
// harness must NEVER panic regardless of input — any panic is a bug.
func FuzzParseFrame(f *testing.F) {
	// Seed corpus.
	f.Add([]byte("HR\x01\x02k\x00\x07v\x00\x00\x00\x01\x00\x00\x00\x00\x00v"))
	f.Add([]byte("HR\x01\x01k\x00\x07\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"))
	f.Add([]byte("HR\x01\x03\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"))
	f.Add([]byte{})           // empty
	f.Add([]byte{0x48, 0x52}) // partial magic
	f.Add(make([]byte, 16))   // full header (zeros)
	f.Add(make([]byte, 64))   // full header + body
	f.Add([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF})

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("parser panicked on input %x: %v", data, r)
			}
		}()
		p := NewParser()
		// Feed one byte at a time to stress the state machine.
		for i := range data {
			_, _, err := p.Feed(data[i : i+1])
			if err != nil && !isExpectedErr(err) {
				t.Fatalf("unexpected error on input %x at byte %d: %v", data, i, err)
			}
		}
		// Also feed the whole buffer at once for the fast path.
		p2 := NewParser()
		_, _, err := p2.Feed(data)
		if err != nil && !isExpectedErr(err) {
			t.Fatalf("unexpected error on full input %x: %v", data, err)
		}
	})
}

// FuzzParseFrameChunked feeds the input in randomly-sized chunks. Both
// must converge to the same state and never panic.
func FuzzParseFrameChunked(f *testing.F) {
	f.Add(uint16(0), []byte("HR\x01\x02k\x00\x07v\x00\x00\x00\x01\x00\x00\x00\x00\x00v"))
	f.Add(uint16(7), []byte("HR\x01\x02k\x00\x07v\x00\x00\x00\x01\x00\x00\x00\x00\x00v"))

	f.Fuzz(func(t *testing.T, chunk uint16, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("parser panicked on input %x (chunk=%d): %v", data, chunk, r)
			}
		}()
		// Pick a chunk size from 1..16 via the fuzz input.
		cs := int(chunk)%16 + 1
		p := NewParser()
		for i := 0; i < len(data); i += cs {
			end := i + cs
			if end > len(data) {
				end = len(data)
			}
			_, _, err := p.Feed(data[i:end])
			if err != nil && !isExpectedErr(err) {
				t.Fatalf("unexpected error chunk=%d input=%x: %v", cs, data, err)
			}
		}
	})
}

// isExpectedErr returns true for the set of protocol-level errors that
// any malformed input may legitimately produce.
func isExpectedErr(err error) bool {
	switch {
	case errors.Is(err, ErrBadMagic),
		errors.Is(err, ErrBadVersion),
		errors.Is(err, ErrBadCmd),
		errors.Is(err, ErrKeyTooLong),
		errors.Is(err, ErrValTooLarge),
		errors.Is(err, ErrHdrCRC),
		errors.Is(err, ErrCRCMismatch):
		return true
	}
	return false
}
