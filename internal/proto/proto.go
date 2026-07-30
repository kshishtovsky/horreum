// Package proto implements a minimal binary request/response protocol for
// horreum.  Frames are self-describing:
//
//	┌──────────┬──────────┬──────────┬────────────┬────────────┬─────────┬─────────┐
//	│ magic(2) │ type(1)  │ flags(1) │ keyLen(2)  │ valLen(4)  │ key     │ value   │
//	└──────────┴──────────┴──────────┴────────────┴────────────┴─────────┴─────────┘
//
//	magic:  0x4848 ("HH") — rejects foreign traffic.
//	type:   opCodeGet=1, opCodeSet=2, opCodeDel=3.
//	flags:  reserved; bit 0 = respStatus (set on responses, error=1).
//	keyLen: 0..65535.
//	valLen: 0..2^32-1; payload must fit in a single arena region (<= 64 MiB).
//
// The parser is zero-copy: Frame.Key and Frame.Value point directly into
// the input buffer.  The caller is responsible for keeping the input
// buffer alive until the frame is consumed.
//
// Wire-endianness: little-endian.
package proto

import (
	"encoding/binary"
	"errors"
	"io"
)

const (
	magic uint16 = 0x4848 // "HH"

	headerSize = 2 + 1 + 1 + 2 + 4 // 10 bytes

	maxFrameSize = 64 << 20 // 64 MiB — matches arena.MaxObjectSize

	opCodeGet uint8 = 1
	opCodeSet   uint8 = 2
	opCodeDel   uint8 = 3
	opCodeSetEx uint8 = 4

	// Response status codes (encoded in flags on responses).
	statusOK  uint8 = 0
	statusErr uint8 = 1
)

var opName = [5]string{"", "GET", "SET", "DEL", "SETEX"}

// OpCode is the parsed operation kind.
type OpCode uint8

// String returns a human-readable name.  Defined for log readability.
func (o OpCode) String() string {
	if int(o) < len(opName) {
		return opName[int(o)]
	}
	return "?"
}

const (
	OpGet   OpCode = OpCode(opCodeGet)
	OpSet   OpCode = OpCode(opCodeSet)
	OpDel   OpCode = OpCode(opCodeDel)
	OpSetEx OpCode = OpCode(opCodeSetEx)
)

// Errors returned by the parser.
var (
	ErrShortFrame     = errors.New("proto: short frame")
	ErrBadMagic       = errors.New("proto: bad magic")
	ErrUnknownOp      = errors.New("proto: unknown op")
	ErrFrameTooLarge  = errors.New("proto: frame too large")
	ErrTruncatedFrame = errors.New("proto: truncated frame")
)

// Frame is a parsed protocol message.  Key and Value are zero-copy views
// into the buffer passed to Parser.Feed.  They are valid only as long as
// that buffer is alive.
type Frame struct {
	Op    OpCode
	Key   []byte
	Value []byte // empty for GET/DEL
	// flags is the raw byte; bit 0 = error (on responses).
	flags uint8
}

// IsResponse reports whether this frame is a response (flags bit 0 set).
func (f *Frame) IsResponse() bool { return f.flags&0x1 == 0x1 }

// IsError reports whether this response signals an error.
func (f *Frame) IsError() bool { return f.flags&0x1 == 0x1 && f.flags != 0 }

// EncodeRequest appends an op + key + value to dst, returning the
// extended slice.  dst is grown as needed; the returned slice aliases
// dst.  Pass a reusable byte buffer to avoid allocations.
func EncodeRequest(dst []byte, op OpCode, key, value []byte) []byte {
	const hdrLen = headerSize
	need := hdrLen + len(key) + len(value)
	if cap(dst)-len(dst) < need {
		newBuf := make([]byte, len(dst)+need, 2*(len(dst)+need))
		copy(newBuf, dst)
		dst = newBuf[:len(dst)]
	}
	off := len(dst)
	dst = dst[:off+need]
	binary.LittleEndian.PutUint16(dst[off:off+2], magic)
	dst[off+2] = uint8(op)
	dst[off+3] = 0
	binary.LittleEndian.PutUint16(dst[off+4:off+6], uint16(len(key)))
	binary.LittleEndian.PutUint32(dst[off+6:off+10], uint32(len(value)))
	copy(dst[off+hdrLen:off+hdrLen+len(key)], key)
	copy(dst[off+hdrLen+len(key):], value)
	return dst
}

// EncodeSetEx appends an OpSetEx + key + [ttlSeconds(4 bytes) + value] to dst.
func EncodeSetEx(dst []byte, key, value []byte, ttlSeconds uint32) []byte {
	const hdrLen = headerSize
	valLen := 4 + len(value)
	need := hdrLen + len(key) + valLen
	if cap(dst)-len(dst) < need {
		newBuf := make([]byte, len(dst)+need, 2*(len(dst)+need))
		copy(newBuf, dst)
		dst = newBuf[:len(dst)]
	}
	off := len(dst)
	dst = dst[:off+need]
	binary.LittleEndian.PutUint16(dst[off:off+2], magic)
	dst[off+2] = opCodeSetEx
	dst[off+3] = 0
	binary.LittleEndian.PutUint16(dst[off+4:off+6], uint16(len(key)))
	binary.LittleEndian.PutUint32(dst[off+6:off+10], uint32(valLen))
	copy(dst[off+hdrLen:off+hdrLen+len(key)], key)
	// Write TTL
	valOff := off + hdrLen + len(key)
	binary.LittleEndian.PutUint32(dst[valOff:valOff+4], ttlSeconds)
	// Write Value
	copy(dst[valOff+4:], value)
	return dst
}

// EncodeResponse appends a response frame to dst.
func EncodeResponse(dst []byte, op OpCode, status uint8, key, value []byte) []byte {
	const hdrLen = headerSize
	need := hdrLen + len(key) + len(value)
	if cap(dst)-len(dst) < need {
		newBuf := make([]byte, len(dst)+need, 2*(len(dst)+need))
		copy(newBuf, dst)
		dst = newBuf[:len(dst)]
	}
	off := len(dst)
	dst = dst[:off+need]
	binary.LittleEndian.PutUint16(dst[off:off+2], magic)
	dst[off+2] = uint8(op)
	dst[off+3] = status
	binary.LittleEndian.PutUint16(dst[off+4:off+6], uint16(len(key)))
	binary.LittleEndian.PutUint32(dst[off+6:off+10], uint32(len(value)))
	copy(dst[off+hdrLen:off+hdrLen+len(key)], key)
	copy(dst[off+hdrLen+len(key):], value)
	return dst
}

// Parser is a stateful streaming frame parser over a sequence of buffers.
// Feed processes all complete frames in buf and returns them.  Any
// remaining bytes (incomplete frame) are retained for the next Feed call.
//
// SAFETY: the returned Frames reference buf.  The caller MUST keep buf
// alive until the frame has been consumed (typically by writing it to
// the network or copying it into the arena).
type Parser struct {
	pending []byte // bytes from a previous Feed that did not complete a frame
}

// NewParser returns a fresh Parser.
func NewParser() *Parser { return &Parser{} }

// Reset discards any pending bytes.  Use after a connection error.
func (p *Parser) Reset() { p.pending = p.pending[:0] }

// Pending returns the number of buffered bytes from a prior Feed.
func (p *Parser) Pending() int { return len(p.pending) }

// Parse attempts to extract one Frame from the head of p.pending.
// Returns (frame, true) on success, or (zero, false) if the buffer is
// incomplete — caller should wait for more bytes.
//
// On error (bad magic, oversized, etc.) the parser returns the error and
// invalidates the pending buffer; subsequent Feed calls will also fail
// until Reset is called.
func (p *Parser) Parse() (Frame, error) {
	if len(p.pending) < headerSize {
		return Frame{}, io.ErrShortBuffer
	}
	if binary.LittleEndian.Uint16(p.pending[0:2]) != magic {
		return Frame{}, ErrBadMagic
	}
	op := p.pending[2]
	flags := p.pending[3]
	keyLen := int(binary.LittleEndian.Uint16(p.pending[4:6]))
	valLen := int(binary.LittleEndian.Uint32(p.pending[6:10]))
	total := headerSize + keyLen + valLen
	if total > maxFrameSize {
		return Frame{}, ErrFrameTooLarge
	}
	if len(p.pending) < total {
		return Frame{}, io.ErrShortBuffer
	}
	var opc OpCode
	switch op {
	case opCodeGet, opCodeSet, opCodeDel, opCodeSetEx:
		opc = OpCode(op)
	default:
		return Frame{}, ErrUnknownOp
	}
	f := Frame{
		Op:    opc,
		Key:   p.pending[headerSize : headerSize+keyLen],
		Value: p.pending[headerSize+keyLen : total],
		flags: flags,
	}
	// Slide pending forward without copying, so the returned zero-copy
	// frame retains valid references to the original backing array.
	p.pending = p.pending[total:]
	return f, nil
}

// Feed appends buf to any pending bytes and attempts to parse all
// complete frames.  Returns parsed frames (which borrow memory from
// buf/pending) and an error if the connection should be dropped.
func (p *Parser) Feed(buf []byte) ([]Frame, error) {
	if len(p.pending) == 0 {
		work := buf
		var frames []Frame
		for {
			f, err := p.parseIn(work)
			if err == io.ErrShortBuffer {
				if len(work) > 0 {
					p.pending = append(p.pending[:0], work...)
				}
				return frames, nil
			}
			if err != nil {
				p.Reset()
				return frames, err
			}
			consumed := headerSize + len(f.Key) + len(f.Value)
			work = work[consumed:]
			frames = append(frames, f)
		}
	}

	p.pending = append(p.pending, buf...)
	work := p.pending

	var frames []Frame
	for {
		f, err := p.parseIn(work)
		if err == io.ErrShortBuffer {
			if len(work) == 0 {
				p.pending = p.pending[:0] // keep capacity
			} else if len(work) < cap(p.pending)/2 {
				// Slide to the front of the backing array to reclaim capacity.
				remain := copy(p.pending[:cap(p.pending)], work)
				p.pending = p.pending[:remain]
			} else {
				// Avoid O(N) sliding if we haven't consumed much.
				p.pending = work
			}
			return frames, nil
		}
		if err != nil {
			p.Reset()
			return frames, err
		}
		consumed := headerSize + len(f.Key) + len(f.Value)
		work = work[consumed:]
		frames = append(frames, f)
	}
}

// parseIn extracts one Frame from work without retaining state.  On
// short buffer returns io.ErrShortBuffer.
func (p *Parser) parseIn(work []byte) (Frame, error) {
	if len(work) < headerSize {
		return Frame{}, io.ErrShortBuffer
	}
	if binary.LittleEndian.Uint16(work[0:2]) != magic {
		return Frame{}, ErrBadMagic
	}
	op := work[2]
	flags := work[3]
	keyLen := int(binary.LittleEndian.Uint16(work[4:6]))
	valLen := int(binary.LittleEndian.Uint32(work[6:10]))
	total := headerSize + keyLen + valLen
	if total > maxFrameSize {
		return Frame{}, ErrFrameTooLarge
	}
	if len(work) < total {
		return Frame{}, io.ErrShortBuffer
	}
	var opc OpCode
	switch op {
	case opCodeGet, opCodeSet, opCodeDel, opCodeSetEx:
		opc = OpCode(op)
	default:
		return Frame{}, ErrUnknownOp
	}
	return Frame{
		Op:    opc,
		Key:   work[headerSize : headerSize+keyLen],
		Value: work[headerSize+keyLen : total],
		flags: flags,
	}, nil
}
