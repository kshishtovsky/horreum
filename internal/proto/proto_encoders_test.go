package proto

import (
	"bytes"
	"testing"
)

func TestEncodersCoverage(t *testing.T) {
	key := []byte("testkey")
	val := []byte("testval")

	p := NewParser()

	// EncodeSetEx
	bufSetEx := EncodeSetEx(nil, key, val, 60)
	frames, err := p.Feed(bufSetEx)
	if err != nil || len(frames) == 0 || frames[0].Op != OpSetEx {
		t.Fatalf("EncodeSetEx parse failed: err=%v", err)
	}
	p.Reset()

	// EncodeCAS
	bufCAS := EncodeCAS(nil, key, val, []byte("newval"))
	frames, err = p.Feed(bufCAS)
	if err != nil || len(frames) == 0 || frames[0].Op != OpCAS {
		t.Fatalf("EncodeCAS parse failed: err=%v", err)
	}
	p.Reset()

	// EncodeIncr
	bufIncr := EncodeIncr(nil, key, 5)
	frames, err = p.Feed(bufIncr)
	if err != nil || len(frames) == 0 || frames[0].Op != OpIncr {
		t.Fatalf("EncodeIncr parse failed: err=%v", err)
	}
	p.Reset()

	// EncodeScan
	bufScan := EncodeScan(nil, key, 100, 50)
	frames, err = p.Feed(bufScan)
	if err != nil || len(frames) == 0 || frames[0].Op != OpScan {
		t.Fatalf("EncodeScan parse failed: err=%v", err)
	}
	p.Reset()

	// EncodeDelPrefix
	bufDelPrefix := EncodeDelPrefix(nil, key)
	frames, err = p.Feed(bufDelPrefix)
	if err != nil || len(frames) == 0 || frames[0].Op != OpDelPrefix {
		t.Fatalf("EncodeDelPrefix parse failed: err=%v", err)
	}
	_ = bytes.Equal
}
