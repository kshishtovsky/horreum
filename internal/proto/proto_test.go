// Package proto implements the Horreum binary wire protocol:
// a fixed 16-byte header, big-endian framing, zero-copy parser
// and encoder. See docs/PROTOCOL.md for the wire format.
package proto

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"testing"

	"github.com/horreum/horreum/internal/arena"
)

var testIEEETable = crc32.MakeTable(crc32.IEEE)

const (
	testKey = "user:42"
	testVal = "hello world payload"
)

// TestRoundTripSET verifies that an encoded SET frame decodes back into
// identical fields and that KEY/VAL are zero-copy slices over the input buffer.
func TestRoundTripSET(t *testing.T) {
	want := []byte("hello world payload")
	dst := make([]byte, 0, 64+len(want)+16)
	enc, err := Encode(dst, CmdSET, 0, []byte(testKey), want)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(enc) != 16+len(testKey)+len(want) {
		t.Fatalf("encoded len = %d, want %d", len(enc), 16+len(testKey)+len(want))
	}

	p := NewParser()
	fr, n, err := p.Feed(enc)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if n != len(enc) {
		t.Fatalf("consumed = %d, want %d", n, len(enc))
	}
	if fr.Cmd != CmdSET {
		t.Errorf("Cmd = %d, want %d", fr.Cmd, CmdSET)
	}
	if string(fr.Key) != testKey {
		t.Errorf("Key = %q, want %q", fr.Key, testKey)
	}
	if string(fr.Val) != string(want) {
		t.Errorf("Val = %q, want %q", fr.Val, want)
	}

	// Zero-copy check: Key/Val slices must point into the input buffer.
	if &fr.Key[0] != &enc[16] {
		t.Errorf("Key is not a zero-copy slice over input buffer")
	}
	if &fr.Val[0] != &enc[16+len(testKey)] {
		t.Errorf("Val is not a zero-copy slice over input buffer")
	}
}

// TestRoundTripGET verifies GET requests have empty VAL.
func TestRoundTripGET(t *testing.T) {
	dst := make([]byte, 0, 64)
	enc, err := Encode(dst, CmdGET, 0, []byte(testKey), nil)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	p := NewParser()
	fr, n, err := p.Feed(enc)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if n != len(enc) {
		t.Fatalf("consumed = %d, want %d", n, len(enc))
	}
	if fr.Cmd != CmdGET {
		t.Errorf("Cmd = %d, want %d", fr.Cmd, CmdGET)
	}
	if len(fr.Val) != 0 {
		t.Errorf("Val len = %d, want 0", len(fr.Val))
	}
}

// TestEncodeResponseOK exercises the response path with both OK and NOK.
func TestEncodeResponseOK(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
		want uint8
	}{
		{"ok", true, FlagResponse | FlagOK},
		{"nok", false, FlagResponse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dst := make([]byte, 0, 64)
			enc, err := EncodeResponse(dst, CmdSET, tt.ok, false, nil, []byte("v"))
			if err != nil {
				t.Fatalf("EncodeResponse: %v", err)
			}
			if enc[2] != ver {
				t.Errorf("VER byte = %#x, want %#x", enc[2], ver)
			}
			if enc[4] != tt.want {
				t.Errorf("FLAGS = %#x, want %#x", enc[4], tt.want)
			}

			p := NewParser()
			fr, _, err := p.Feed(enc)
			if err != nil {
				t.Fatalf("Feed: %v", err)
			}
			if fr.Flags&FlagResponse == 0 {
				t.Errorf("FlagResponse not set")
			}
			if tt.ok && fr.Flags&FlagOK == 0 {
				t.Errorf("FlagOK not set")
			}
		})
	}
}

// TestCRC32RoundTrip verifies CRC32 is computed correctly across encode/decode.
func TestCRC32RoundTrip(t *testing.T) {
	val := []byte("payload-with-crc")
	dst := make([]byte, 0, 128)
	enc, err := Encode(dst, CmdSET, FlagCRC32, []byte("k"), val)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	want := crc32.Update(0, testIEEETable, []byte("k"))
	want = crc32.Update(want, testIEEETable, val)
	var got uint32
	got = binary.BigEndian.Uint32(enc[len(enc)-4:])
	if got != want {
		t.Fatalf("CRC32 = %#x, want %#x", got, want)
	}

	p := NewParser()
	fr, n, err := p.Feed(enc)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if n != len(enc) {
		t.Fatalf("consumed = %d, want %d", n, len(enc))
	}
	if fr.CRC32 != want {
		t.Errorf("CRC32 field = %#x, want %#x", fr.CRC32, want)
	}
}

// TestPartialFeedByteByByte feeds a complete frame one byte at a time
// (each Feed gets a fresh 1-byte slice) and verifies the parser reassembles
// it without data loss across the incremental state transitions.
func TestPartialFeedByteByByte(t *testing.T) {
	val := bytes.Repeat([]byte("x"), 100)
	dst := make([]byte, 0, 256)
	enc, err := Encode(dst, CmdSET, FlagCRC32, []byte(testKey), val)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	p := NewParser()
	var got Frame
	for i := range enc {
		fr, _, err := p.Feed(enc[i : i+1])
		if err != nil {
			t.Fatalf("Feed at byte %d: %v", i, err)
		}
		if fr.Cmd != 0 {
			got = fr
		}
	}
	if string(got.Key) != testKey {
		t.Errorf("Key = %q, want %q", got.Key, testKey)
	}
	if !bytes.Equal(got.Val, val) {
		t.Errorf("Val mismatch")
	}
	if got.CRC32 == 0 {
		t.Errorf("CRC32 not populated")
	}
}

// TestPartialFeedChunked feeds the frame in chunks of varying size to
// exercise the mid-state transitions. The caller is responsible for
// passing contiguous, freshly-appended chunks each call.
func TestPartialFeedChunked(t *testing.T) {
	val := bytes.Repeat([]byte("y"), 50)
	dst := make([]byte, 0, 256)
	enc, err := Encode(dst, CmdSET, 0, []byte(testKey), val)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	p := NewParser()
	// Feed in varying chunk sizes — split the encoded bytes arbitrarily.
	chunks := []int{3, 5, 1, 8, 10, 2, 1000}
	idx := 0
	var got Frame
	for _, c := range chunks {
		end := idx + c
		if end > len(enc) {
			end = len(enc)
		}
		fr, _, err := p.Feed(enc[idx:end])
		if err != nil {
			t.Fatalf("Feed chunk %d..%d: %v", idx, end, err)
		}
		if fr.Cmd != 0 {
			got = fr
		}
		idx = end
		if idx >= len(enc) {
			break
		}
	}
	if idx < len(enc) {
		t.Errorf("did not consume entire frame: %d/%d", idx, len(enc))
	}
	if string(got.Key) != testKey {
		t.Errorf("Key = %q, want %q", got.Key, testKey)
	}
	if !bytes.Equal(got.Val, val) {
		t.Errorf("Val mismatch")
	}
}

// TestReset verifies Reset clears parser state.
func TestReset(t *testing.T) {
	val := []byte("v")
	dst := make([]byte, 0, 64)
	enc, _ := Encode(dst, CmdSET, 0, []byte(testKey), val)

	p := NewParser()
	// Feed partial header
	_, _, _ = p.Feed(enc[:5])
	// Reset
	p.Reset()
	// Feed full frame — must succeed
	fr, n, err := p.Feed(enc)
	if err != nil {
		t.Fatalf("Feed after Reset: %v", err)
	}
	if n != len(enc) {
		t.Errorf("consumed = %d, want %d", n, len(enc))
	}
	if fr.Cmd != CmdSET {
		t.Errorf("Cmd = %d, want %d", fr.Cmd, CmdSET)
	}
}

// TestErrors covers parser error paths.
func TestErrors(t *testing.T) {
	val := []byte("v")
	dst := make([]byte, 0, 64)
	enc, _ := Encode(dst, CmdSET, 0, []byte(testKey), val)

	tests := []struct {
		name string
		data []byte
		want error
	}{
		{"bad_magic", []byte{'X', 'R', 0x01, CmdSET, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0}, ErrBadMagic},
		{"bad_version", []byte{'H', 'R', 0x02, CmdSET, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0}, ErrBadVersion},
		{"bad_cmd", func() []byte {
			x := make([]byte, 16)
			x[0], x[1] = 'H', 'R'
			x[2] = ver
			x[3] = 0xFF // unknown command
			x[15] = crc8(x[:15])
			return x
		}(), ErrBadCmd},
		{"key_too_long", func() []byte {
			x := make([]byte, 16)
			x[0], x[1] = 'H', 'R'
			x[2] = ver
			x[3] = CmdSET
			x[5] = 1 // KEY_LEN = 256, exceeds max 255
			x[15] = crc8(x[:15])
			return x
		}(), ErrKeyTooLong},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewParser()
			_, _, err := p.Feed(tt.data)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Feed err = %v, want %v", err, tt.want)
			}
		})
	}

	// ValTooLarge requires a 16-byte header + enough bytes for VAL
	t.Run("val_too_large", func(t *testing.T) {
		b := make([]byte, 16)
		b[0], b[1] = 'H', 'R'
		b[2] = ver
		b[3] = CmdSET
		// VAL_LEN = MaxObjectSize+1
		binary.BigEndian.PutUint32(b[7:11], arena.MaxObjectSize+1)
		b[15] = crc8(b[:15])
		p := NewParser()
		_, _, err := p.Feed(b)
		if !errors.Is(err, ErrValTooLarge) {
			t.Fatalf("Feed err = %v, want %v", err, ErrValTooLarge)
		}
	})

	// HDR_CRC mismatch
	t.Run("hdr_crc_mismatch", func(t *testing.T) {
		bad := make([]byte, len(enc))
		copy(bad, enc)
		bad[15] ^= 0xFF
		p := NewParser()
		_, _, err := p.Feed(bad)
		if !errors.Is(err, ErrHdrCRC) {
			t.Fatalf("Feed err = %v, want %v", err, ErrHdrCRC)
		}
	})

	// CRC32 payload mismatch (1 bit flip)
	t.Run("payload_crc_mismatch", func(t *testing.T) {
		val := []byte("payload-crc")
		dst := make([]byte, 0, 64)
		enc, _ := Encode(dst, CmdSET, FlagCRC32, []byte("k"), val)
		bad := make([]byte, len(enc))
		copy(bad, enc)
		bad[16+3] ^= 0x01 // flip 1 bit in KEY
		p := NewParser()
		_, _, err := p.Feed(bad)
		if !errors.Is(err, ErrCRCMismatch) {
			t.Fatalf("Feed err = %v, want %v", err, ErrCRCMismatch)
		}
	})
}

// TestInsufficientData verifies the parser returns 0,nil on partial input.
// Each Feed call receives a fresh sub-slice of the encoded frame
// (different-buffer-per-call convention).
func TestInsufficientData(t *testing.T) {
	val := []byte("v")
	dst := make([]byte, 0, 64)
	enc, _ := Encode(dst, CmdSET, 0, []byte(testKey), val)

	p := NewParser()
	for i := 0; i < len(enc)-1; i++ {
		_, n, err := p.Feed(enc[i : i+1])
		if err != nil {
			t.Fatalf("Feed at byte %d: %v", i, err)
		}
		if n != 0 {
			t.Errorf("at byte %d: consumed = %d, want 0", i, n)
		}
	}
}

// TestEncodeDstTooSmall verifies Encode fails when cap is insufficient.
func TestEncodeDstTooSmall(t *testing.T) {
	dst := make([]byte, 0, 8) // too small
	_, err := Encode(dst, CmdSET, 0, []byte("key"), []byte("val"))
	if !errors.Is(err, ErrDstTooSmall) {
		t.Fatalf("Encode err = %v, want %v", err, ErrDstTooSmall)
	}
}

// TestEncodeZeroAlloc verifies Encode reuses dst capacity without allocs.
func TestEncodeZeroAlloc(t *testing.T) {
	dst := make([]byte, 0, 1024)
	// Encode a frame that fits in cap
	out, err := Encode(dst, CmdSET, FlagCRC32, []byte("k"), bytes.Repeat([]byte("v"), 100))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if cap(out) != 1024 {
		t.Errorf("cap changed: got %d, want 1024", cap(out))
	}
}

// TestFrameMultipleInOneFeed verifies back-to-back frames in one Feed.
func TestFrameMultipleInOneFeed(t *testing.T) {
	val1 := []byte("first")
	val2 := []byte("second")
	buf := make([]byte, 64)
	a, _ := Encode(buf, CmdSET, 0, []byte("k1"), val1)
	b, _ := Encode(buf[len(a):], CmdSET, 0, []byte("k2"), val2)
	stream := buf[:len(a)+len(b)]

	p := NewParser()
	fr1, n1, err := p.Feed(stream)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if n1 != len(a) {
		t.Fatalf("first consumed = %d, want %d", n1, len(a))
	}
	if string(fr1.Key) != "k1" || string(fr1.Val) != "first" {
		t.Errorf("first frame mismatch: key=%q val=%q", fr1.Key, fr1.Val)
	}

	fr2, n2, err := p.Feed(stream[n1:])
	if err != nil {
		t.Fatalf("Feed 2: %v", err)
	}
	if n2 != len(b) {
		t.Fatalf("second consumed = %d, want %d", n2, len(b))
	}
	if string(fr2.Key) != "k2" || string(fr2.Val) != "second" {
		t.Errorf("second frame mismatch: key=%q val=%q", fr2.Key, fr2.Val)
	}
}

// TestEncodeResponseWithCRC exercises response encoder with CRC.
func TestEncodeResponseWithCRC(t *testing.T) {
	dst := make([]byte, 0, 128)
	enc, err := EncodeResponse(dst, CmdGET, true, true, []byte("k"), []byte("v"))
	if err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}
	if enc[4]&FlagCRC32 == 0 {
		t.Errorf("FlagCRC32 not set")
	}
	p := NewParser()
	fr, n, err := p.Feed(enc)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if n != len(enc) {
		t.Fatalf("consumed = %d, want %d", n, len(enc))
	}
	if fr.Cmd != CmdGET {
		t.Errorf("Cmd = %d, want %d", fr.Cmd, CmdGET)
	}
	if fr.CRC32 == 0 {
		t.Errorf("CRC32 not populated")
	}
}
