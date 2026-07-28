package proto

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	buf := make([]byte, 0, 64)
	buf = EncodeRequest(buf, OpSet, []byte("hello"), []byte("world"))

	if got := binaryU16(buf[0:2]); got != magic {
		t.Fatalf("magic = 0x%x, want 0x%x", got, magic)
	}
	if buf[2] != opCodeSet {
		t.Errorf("op = %d, want %d", buf[2], opCodeSet)
	}
	if got := binaryU16(buf[4:6]); got != 5 {
		t.Errorf("keyLen = %d, want 5", got)
	}
	if got := binaryU32(buf[6:10]); got != 5 {
		t.Errorf("valLen = %d, want 5", got)
	}

	p := NewParser()
	frames, err := p.Feed(buf)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1", len(frames))
	}
	f := frames[0]
	if f.Op != OpSet {
		t.Errorf("op = %v, want SET", f.Op)
	}
	if !bytes.Equal(f.Key, []byte("hello")) {
		t.Errorf("key = %q, want hello", f.Key)
	}
	if !bytes.Equal(f.Value, []byte("world")) {
		t.Errorf("value = %q, want world", f.Value)
	}
}

func TestParseMultipleFramesInOneFeed(t *testing.T) {
	buf := make([]byte, 0, 128)
	buf = EncodeRequest(buf, OpSet, []byte("k1"), []byte("v1"))
	buf = EncodeRequest(buf, OpGet, []byte("k2"), nil)
	buf = EncodeRequest(buf, OpDel, []byte("k3"), nil)

	p := NewParser()
	frames, err := p.Feed(buf)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if len(frames) != 3 {
		t.Fatalf("got %d frames, want 3", len(frames))
	}
	if frames[0].Op != OpSet || !bytes.Equal(frames[0].Key, []byte("k1")) {
		t.Errorf("frame[0]: %+v", frames[0])
	}
	if frames[1].Op != OpGet || !bytes.Equal(frames[1].Key, []byte("k2")) {
		t.Errorf("frame[1]: %+v", frames[1])
	}
	if frames[2].Op != OpDel || !bytes.Equal(frames[2].Key, []byte("k3")) {
		t.Errorf("frame[2]: %+v", frames[2])
	}
}

func TestParseSplitFeed(t *testing.T) {
	buf := make([]byte, 0, 32)
	buf = EncodeRequest(buf, OpSet, []byte("abcdef"), []byte("12345678"))

	p := NewParser()

	// Feed first half — should retain partial.
	frames, err := p.Feed(buf[:7])
	if err != nil {
		t.Fatalf("Feed1: %v", err)
	}
	if len(frames) != 0 {
		t.Fatalf("Feed1 produced %d frames", len(frames))
	}
	if p.Pending() != 7 {
		t.Errorf("pending = %d, want 7", p.Pending())
	}

	// Feed remainder — should complete the frame.
	frames, err = p.Feed(buf[7:])
	if err != nil {
		t.Fatalf("Feed2: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("Feed2 produced %d frames", len(frames))
	}
	if !bytes.Equal(frames[0].Key, []byte("abcdef")) {
		t.Errorf("key = %q", frames[0].Key)
	}
	if !bytes.Equal(frames[0].Value, []byte("12345678")) {
		t.Errorf("value = %q", frames[0].Value)
	}
}

func TestBadMagic(t *testing.T) {
	buf := []byte{0xff, 0xff, opCodeGet, 0, 0, 0, 0, 0, 0, 0}
	p := NewParser()
	if _, err := p.Feed(buf); !errors.Is(err, ErrBadMagic) {
		t.Fatalf("err = %v, want ErrBadMagic", err)
	}
}

func TestUnknownOp(t *testing.T) {
	buf := make([]byte, 0, 16)
	buf = EncodeRequest(buf, OpCode(99), []byte("k"), []byte("v"))
	p := NewParser()
	if _, err := p.Feed(buf); !errors.Is(err, ErrUnknownOp) {
		t.Fatalf("err = %v, want ErrUnknownOp", err)
	}
}

func TestFrameTooLarge(t *testing.T) {
	buf := make([]byte, headerSize)
	binaryU16Put(buf[0:2], magic)
	buf[2] = opCodeSet
	binaryU16Put(buf[4:6], 0)
	binaryU32Put(buf[6:10], maxFrameSize+1)
	p := NewParser()
	if _, err := p.Feed(buf); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want ErrFrameTooLarge", err)
	}
}

func TestEncodeResponseError(t *testing.T) {
	buf := make([]byte, 0, 32)
	buf = EncodeResponse(buf, OpGet, statusErr, []byte("k"), nil)
	if buf[3]&0x1 != 1 {
		t.Errorf("error flag not set: flags=0x%x", buf[3])
	}
}

// helpers — wraps encoding/binary without importing in test header.
func binaryU16(b []byte) uint16 { return uint16(b[0]) | uint16(b[1])<<8 }
func binaryU32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
func binaryU16Put(b []byte, v uint16) { b[0] = byte(v); b[1] = byte(v >> 8) }
func binaryU32Put(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}

// silence unused import warning when build with !io.
var _ = io.EOF
