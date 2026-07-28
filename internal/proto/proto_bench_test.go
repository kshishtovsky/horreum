package proto

import (
	"bytes"
	"testing"
)

// makeFrameSET1K builds an encoded SET frame with a ~1 KiB value.
func makeFrameSET1K() []byte {
	val := bytes.Repeat([]byte("v"), 1024-len(testKey))
	dst := make([]byte, 0, 16+len(testKey)+len(val)+4)
	out, _ := Encode(dst, CmdSET, FlagCRC32, []byte(testKey), val)
	return out
}

// makeFrameGET1K builds an encoded GET request (no value).
func makeFrameGET1K() []byte {
	dst := make([]byte, 0, 16+len(testKey))
	out, _ := Encode(dst, CmdGET, 0, []byte(testKey), nil)
	return out
}

// makeResponse1K builds a successful response with ~1 KiB value.
func makeResponse1K() []byte {
	val := bytes.Repeat([]byte("v"), 1024-len(testKey))
	dst := make([]byte, 0, 16+len(testKey)+len(val)+4)
	out, _ := EncodeResponse(dst, CmdSET, true, true, []byte(testKey), val)
	return out
}

// BenchmarkParseSET1K parses a 1 KiB SET frame repeatedly. The pre-built
// encoded slice is reused so the cost reflects pure parser work.
func BenchmarkParseSET1K(b *testing.B) {
	frame := makeFrameSET1K()
	p := NewParser()
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.Reset()
		if _, _, err := p.Feed(frame); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEncodeResponse1K encodes a ~1 KiB successful response frame
// repeatedly using a pre-sized dst (no allocation).
func BenchmarkEncodeResponse1K(b *testing.B) {
	val := bytes.Repeat([]byte("v"), 1024-len(testKey))
	dst := make([]byte, 0, 16+len(testKey)+len(val)+4)
	b.ReportAllocs()
	b.SetBytes(int64(16 + len(testKey) + len(val) + 4))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := EncodeResponse(dst, CmdSET, true, true, []byte(testKey), val)
		if err != nil {
			b.Fatal(err)
		}
		dst = out[:0]
	}
}

// BenchmarkEncodeSET1K encodes a ~1 KiB SET request.
func BenchmarkEncodeSET1K(b *testing.B) {
	val := bytes.Repeat([]byte("v"), 1024-len(testKey))
	dst := make([]byte, 0, 16+len(testKey)+len(val)+4)
	b.ReportAllocs()
	b.SetBytes(int64(16 + len(testKey) + len(val) + 4))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := Encode(dst, CmdSET, FlagCRC32, []byte(testKey), val)
		if err != nil {
			b.Fatal(err)
		}
		dst = out[:0]
	}
}

// BenchmarkParseGET exercises a small (header-only) GET frame. Useful for
// measuring the per-frame fixed overhead.
func BenchmarkParseGET(b *testing.B) {
	frame := makeFrameGET1K()
	p := NewParser()
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.Reset()
		if _, _, err := p.Feed(frame); err != nil {
			b.Fatal(err)
		}
	}
}
