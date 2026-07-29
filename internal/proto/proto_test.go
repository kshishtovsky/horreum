package proto

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// TestOpCodeString covers every valid op code and an out-of-range one.
func TestOpCodeString(t *testing.T) {
	cases := []struct {
		op   OpCode
		want string
	}{
		{OpGet, "GET"},
		{OpSet, "SET"},
		{OpDel, "DEL"},
		{OpCode(0), ""},
		{OpCode(99), "?"},
	}
	for _, c := range cases {
		if got := c.op.String(); got != c.want {
			t.Errorf("OpCode(%d).String() = %q, want %q", c.op, got, c.want)
		}
	}
}

// TestFrameIsResponse: bit 0 of flags = is-response.
func TestFrameIsResponse(t *testing.T) {
	if (&Frame{}).IsResponse() {
		t.Errorf("zero flags should not be a response")
	}
	if !(&Frame{flags: 0x1}).IsResponse() {
		t.Errorf("flag bit 0 should mark a response")
	}
}

// TestFrameIsError: an error response has flag bit 0 AND the byte is
// non-zero (so a flags==0 response is not an error).
func TestFrameIsError(t *testing.T) {
	if (&Frame{flags: 0x0}).IsError() {
		t.Errorf("zero flags should not be an error")
	}
	if (&Frame{flags: 0x1}).IsError() != true {
		t.Errorf("flag=0x1 should be an error")
	}
	if (&Frame{flags: 0x3}).IsError() != true {
		t.Errorf("flag=0x3 should be an error")
	}
}

// TestEncodeRequestGrow: when dst has insufficient capacity, the
// function grows the buffer.  Verify the appended slice is consistent.
func TestEncodeRequestGrow(t *testing.T) {
	dst := make([]byte, 0, 1) // tiny — will force growth
	dst = EncodeRequest(dst, OpSet, []byte("hello"), []byte("world"))
	if len(dst) != headerSize+5+5 {
		t.Errorf("len(dst) = %d, want %d", len(dst), headerSize+10)
	}
}

// TestEncodeResponseGrow: same growth behaviour for responses.
func TestEncodeResponseGrow(t *testing.T) {
	dst := make([]byte, 0, 1)
	dst = EncodeResponse(dst, OpGet, statusOK, []byte("hello"), nil)
	if len(dst) != headerSize+5 {
		t.Errorf("len(dst) = %d, want %d", len(dst), headerSize+5)
	}
	if dst[3]&0x1 != 0 {
		t.Errorf("statusOK should not set error bit")
	}
}

// TestParseDirect: call Parser.Parse on a complete buffer.
func TestParseDirect(t *testing.T) {
	buf := make([]byte, 0, 32)
	buf = EncodeRequest(buf, OpGet, []byte("k"), nil)
	p := NewParser()
	// Force the pending buffer to be populated by a partial Feed.
	_, _ = p.Feed(buf[:3])
	if p.Pending() != 3 {
		t.Errorf("Pending after partial Feed = %d, want 3", p.Pending())
	}
	// Now call Parse directly — it should see the partial and return
	// ErrShortBuffer.
	if _, err := p.Parse(); !errors.Is(err, io.ErrShortBuffer) {
		t.Errorf("Parse on partial buffer = %v, want ErrShortBuffer", err)
	}
	// Feed the rest; Parse should now succeed.
	frames, err := p.Feed(buf[3:])
	if err != nil {
		t.Fatalf("Feed rest: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1", len(frames))
	}
	if frames[0].Op != OpGet || !bytes.Equal(frames[0].Key, []byte("k")) {
		t.Errorf("frame = %+v", frames[0])
	}
	if p.Pending() != 0 {
		t.Errorf("Pending after full parse = %d, want 0", p.Pending())
	}
}

// TestParseBadMagic: Parse on a non-magic header returns ErrBadMagic.
func TestParseBadMagic(t *testing.T) {
	buf := make([]byte, headerSize)
	binaryU16Put(buf[0:2], 0xDEAD)
	p := NewParser()
	p.pending = buf
	if _, err := p.Parse(); !errors.Is(err, ErrBadMagic) {
		t.Errorf("Parse(bad magic) = %v, want ErrBadMagic", err)
	}
}

// TestParseTruncated: Parse on a too-short buffer returns ErrShortBuffer.
func TestParseTruncated(t *testing.T) {
	p := NewParser()
	p.pending = make([]byte, headerSize-1)
	if _, err := p.Parse(); !errors.Is(err, io.ErrShortBuffer) {
		t.Errorf("Parse(short) = %v, want ErrShortBuffer", err)
	}
}

// TestParseFrameTooLarge: Parse on a header declaring > maxFrameSize
// returns ErrFrameTooLarge.
func TestParseFrameTooLarge(t *testing.T) {
	buf := make([]byte, headerSize)
	binaryU16Put(buf[0:2], magic)
	buf[2] = opCodeSet
	binaryU16Put(buf[4:6], 0)
	binaryU32Put(buf[6:10], maxFrameSize+1)
	p := NewParser()
	p.pending = buf
	if _, err := p.Parse(); !errors.Is(err, ErrFrameTooLarge) {
		t.Errorf("Parse(too large) = %v, want ErrFrameTooLarge", err)
	}
}

// TestParseUnknownOp: Parse on a header with an unknown op returns
// ErrUnknownOp.
func TestParseUnknownOp(t *testing.T) {
	buf := make([]byte, headerSize)
	binaryU16Put(buf[0:2], magic)
	buf[2] = 99
	p := NewParser()
	p.pending = buf
	if _, err := p.Parse(); !errors.Is(err, ErrUnknownOp) {
		t.Errorf("Parse(unknown op) = %v, want ErrUnknownOp", err)
	}
}

// TestReset: Pending drops to 0 after Reset.
func TestReset(t *testing.T) {
	p := NewParser()
	_, _ = p.Feed([]byte{0xff, 0xff})
	p.Reset()
	if p.Pending() != 0 {
		t.Errorf("Pending after Reset = %d, want 0", p.Pending())
	}
}

// TestFeedAfterError: after a parse error, the parser is reset.
func TestFeedAfterError(t *testing.T) {
	p := NewParser()
	bad := make([]byte, headerSize)
	binaryU16Put(bad[0:2], 0xDEAD)
	if _, err := p.Feed(bad); !errors.Is(err, ErrBadMagic) {
		t.Fatalf("Feed(bad magic) = %v, want ErrBadMagic", err)
	}
	// After the error, pending is reset.
	if p.Pending() != 0 {
		t.Errorf("Pending after error = %d, want 0", p.Pending())
	}
	// Subsequent valid Feed should succeed.
	buf := make([]byte, 0, 32)
	buf = EncodeRequest(buf, OpGet, []byte("k"), nil)
	frames, err := p.Feed(buf)
	if err != nil {
		t.Errorf("Feed after recovery: %v", err)
	}
	if len(frames) != 1 {
		t.Errorf("got %d frames, want 1", len(frames))
	}
}

// TestParseMultiple: Parser.Parse can be called repeatedly on the same
// pending buffer.
func TestParseMultiple(t *testing.T) {
	buf := make([]byte, 0, 64)
	buf = EncodeRequest(buf, OpSet, []byte("k1"), []byte("v1"))
	buf = EncodeRequest(buf, OpDel, []byte("k2"), nil)

	p := NewParser()
	p.pending = buf
	f1, err := p.Parse()
	if err != nil {
		t.Fatalf("Parse 1: %v", err)
	}
	if f1.Op != OpSet {
		t.Errorf("f1.Op = %v, want SET", f1.Op)
	}
	f2, err := p.Parse()
	if err != nil {
		t.Fatalf("Parse 2: %v", err)
	}
	if f2.Op != OpDel {
		t.Errorf("f2.Op = %v, want DEL", f2.Op)
	}
	// Third call should return EOF (buffer empty).
	if _, err := p.Parse(); !errors.Is(err, io.ErrShortBuffer) {
		t.Errorf("Parse 3 = %v, want ErrShortBuffer", err)
	}
}

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
