package proto

import (
	"encoding/binary"
	"hash/crc32"

	"github.com/horreum/horreum/internal/arena"
)

// ieeeTab is the precomputed IEEE CRC-32 table. Allocated once at package
// init so per-frame CRC validation stays zero-allocation.
var ieeeTab = crc32.MakeTable(crc32.IEEE)

// Parser state-machine states.
const (
	stateMagic uint8 = iota
	stateHeader
	stateKey
	stateVal
	stateCRC
	stateDone
)

// Parser is a streaming, zero-allocation parser for the Horreum wire format.
//
// Feed advances the state machine using bytes from buf. On a complete frame
// it returns the parsed Frame, the number of bytes consumed, and a nil error.
// When more bytes are required it returns (Frame{}, 0, nil) and preserves
// internal state for the next call.
//
// Slicing strategy:
//   - Fast path (typical case): when buf contains all body bytes for the frame
//     in a single Feed call, Frame.Key and Frame.Val are zero-copy slices over
//     buf. The caller must keep buf alive while using the Frame.
//   - Slow path: when bytes arrive across multiple Feed calls (partial feed),
//     the parser accumulates into an internal buffer and Frame.Key/Frame.Val
//     are slices over that buffer. They remain valid until the next Feed or
//     Reset call.
//
// Both paths are zero-allocation after the first frame (bodyBuf capacity is
// retained across frames via Reset).
type Parser struct {
	state        uint8
	hdr          [headerSize]byte
	hdrPos       uint8
	bodyBuf      []byte
	bodyPos      int
	accumulating bool
	cmd          uint8
	flags        uint8
	keyLen       uint16
	valLen       uint32
	crcBuf       [crc32Size]byte
	crcHave      uint8
}

// NewParser returns a fresh parser ready to consume bytes.
func NewParser() *Parser {
	return &Parser{}
}

// Reset returns the parser to its initial state. The internal body buffer
// capacity is retained so subsequent frames do not reallocate.
func (p *Parser) Reset() {
	p.state = stateMagic
	p.hdrPos = 0
	p.bodyBuf = p.bodyBuf[:0]
	p.bodyPos = 0
	p.accumulating = false
	p.cmd = 0
	p.flags = 0
	p.keyLen = 0
	p.valLen = 0
	p.crcHave = 0
}

// Feed drives the state machine with bytes from buf.
//
// Returns:
//   - (Frame, n, nil) when a complete frame has been parsed. n is the number
//     of bytes consumed from buf (n > 0).
//   - (Frame{}, 0, nil) when more bytes are required.
//   - (Frame{}, n, err) on a protocol error; the parser is reset.
func (p *Parser) Feed(buf []byte) (Frame, int, error) {
	// Fast path: full frame present in buf, no accumulated state.
	// This avoids the per-state copy into hdrBuf and the state-machine loop.
	if !p.accumulating && p.state == stateMagic {
		if frame, n, ok, err := p.feedFast(buf); ok {
			return frame, n, err
		} else if err != nil {
			return Frame{}, 0, err
		}
	}
	return p.feedSlow(buf)
}

// feedFast attempts to parse a complete frame in one shot from buf.
// Returns (frame, n, true, nil) on success, (zero, 0, false, nil) when buf
// is too short or contains multiple frames, and (zero, n, true, err) on
// protocol errors.
func (p *Parser) feedFast(buf []byte) (Frame, int, bool, error) {
	if len(buf) < headerSize {
		return Frame{}, 0, false, nil
	}
	if buf[0] != magicH || buf[1] != magicR || buf[2] != ver {
		// Fall through to slow path so partial header accumulation works.
		return Frame{}, 0, false, nil
	}
	cmd := buf[3]
	switch cmd {
	case CmdGET, CmdSET, CmdDEL, CmdSTATS:
	default:
		return Frame{}, 0, false, nil
	}
	flags := buf[4]
	keyLen := int(buf[5])<<8 | int(buf[6])
	if keyLen > maxKeyLen {
		return Frame{}, 0, false, nil
	}
	valLen := int(buf[7])<<24 | int(buf[8])<<16 | int(buf[9])<<8 | int(buf[10])
	if valLen > arena.MaxObjectSize {
		return Frame{}, 0, false, nil
	}
	if crc8(buf[:15]) != buf[15] {
		p.Reset()
		return Frame{}, headerSize, true, ErrHdrCRC
	}

	total := headerSize + keyLen + valLen
	if flags&FlagCRC32 != 0 {
		total += crc32Size
	}
	if len(buf) < total {
		return Frame{}, 0, false, nil
	}

	var fr Frame
	fr.Cmd = cmd
	fr.Flags = flags
	fr.KeyLen = uint16(keyLen)
	fr.ValLen = uint32(valLen)
	body := buf[headerSize : headerSize+keyLen+valLen]
	fr.Key = buf[headerSize : headerSize+keyLen]
	if valLen > 0 {
		fr.Val = buf[headerSize+keyLen : total]
	}
	if flags&FlagCRC32 != 0 {
		want := binary.BigEndian.Uint32(buf[total-crc32Size : total])
		c := crc32.ChecksumIEEE(body)
		if c != want {
			p.Reset()
			return Frame{}, total, true, ErrCRCMismatch
		}
		fr.CRC32 = c
	}

	// Multi-frame in one Feed — reset for next frame.
	if len(buf) > total {
		// Reset and let caller loop with buf[total:].
		p.Reset()
		return fr, total, true, nil
	}

	p.Reset()
	return fr, total, true, nil
}

// feedSlow is the streaming path. It uses the state machine and accumulates
// partial bytes into internal buffers.
func (p *Parser) feedSlow(buf []byte) (Frame, int, error) {
	pos := 0
	n := len(buf)
	var fr Frame

	for {
		switch p.state {
		case stateMagic:
			need := 2 - int(p.hdrPos)
			if n-pos < need {
				take := n - pos
				copy(p.hdr[p.hdrPos:], buf[pos:])
				p.hdrPos += uint8(take)
				return Frame{}, 0, nil
			}
			copy(p.hdr[p.hdrPos:p.hdrPos+uint8(need)], buf[pos:pos+need])
			pos += need
			p.hdrPos += uint8(need)
			p.state = stateHeader

		case stateHeader:
			need := headerSize - int(p.hdrPos)
			if n-pos < need {
				take := n - pos
				copy(p.hdr[p.hdrPos:], buf[pos:])
				p.hdrPos += uint8(take)
				return Frame{}, 0, nil
			}
			copy(p.hdr[p.hdrPos:headerSize], buf[pos:pos+need])
			pos += need
			p.hdrPos = headerSize

			if p.hdr[0] != magicH || p.hdr[1] != magicR {
				p.Reset()
				return Frame{}, pos, ErrBadMagic
			}
			if p.hdr[2] != ver {
				p.Reset()
				return Frame{}, pos, ErrBadVersion
			}
			p.cmd = p.hdr[3]
			switch p.cmd {
			case CmdGET, CmdSET, CmdDEL, CmdSTATS:
				// ok
			default:
				p.Reset()
				return Frame{}, pos, ErrBadCmd
			}
			p.flags = p.hdr[4]
			p.keyLen = uint16(p.hdr[5])<<8 | uint16(p.hdr[6])
			if p.keyLen > maxKeyLen {
				p.Reset()
				return Frame{}, pos, ErrKeyTooLong
			}
			p.valLen = uint32(p.hdr[7])<<24 |
				uint32(p.hdr[8])<<16 |
				uint32(p.hdr[9])<<8 |
				uint32(p.hdr[10])
			if p.valLen > arena.MaxObjectSize {
				p.Reset()
				return Frame{}, pos, ErrValTooLarge
			}
			if crc8(p.hdr[:15]) != p.hdr[15] {
				p.Reset()
				return Frame{}, pos, ErrHdrCRC
			}

			switch {
			case p.keyLen > 0:
				p.state = stateKey
			case p.valLen > 0:
				p.state = stateVal
			default:
				p.state = stateCRC
			}

		case stateKey:
			need := int(p.keyLen)
			if need == 0 {
				p.state = stateVal
				continue
			}
			if !p.accumulating {
				totalRemaining := need + int(p.valLen)
				if p.flags&FlagCRC32 != 0 {
					totalRemaining += crc32Size
				}
				if n-pos >= totalRemaining {
					fr.Key = buf[pos : pos+need]
					pos += need
					p.state = stateVal
					continue
				}
			}
			p.accumulating = true
			take := n - pos
			if take > need-p.bodyPos {
				take = need - p.bodyPos
			}
			p.bodyBuf = append(p.bodyBuf, buf[pos:pos+take]...)
			p.bodyPos += take
			pos += take
			if p.bodyPos == need {
				p.bodyPos = 0
				p.state = stateVal
				continue
			}
			return Frame{}, 0, nil

		case stateVal:
			need := int(p.valLen)
			if need == 0 {
				p.state = stateCRC
				continue
			}
			if !p.accumulating {
				totalRemaining := need
				if p.flags&FlagCRC32 != 0 {
					totalRemaining += crc32Size
				}
				if n-pos >= totalRemaining {
					fr.Val = buf[pos : pos+need]
					pos += need
					p.state = stateCRC
					continue
				}
			}
			p.accumulating = true
			take := n - pos
			if take > need-p.bodyPos {
				take = need - p.bodyPos
			}
			p.bodyBuf = append(p.bodyBuf, buf[pos:pos+take]...)
			p.bodyPos += take
			pos += take
			if p.bodyPos == need {
				p.bodyPos = 0
				p.state = stateCRC
				continue
			}
			return Frame{}, 0, nil

		case stateCRC:
			if p.flags&FlagCRC32 == 0 {
				p.state = stateDone
				continue
			}
			need := crc32Size - int(p.crcHave)
			if n-pos < need {
				take := n - pos
				copy(p.crcBuf[p.crcHave:], buf[pos:])
				p.crcHave += uint8(take)
				pos += take
				return Frame{}, 0, nil
			}
			copy(p.crcBuf[p.crcHave:crc32Size], buf[pos:pos+need])
			pos += need
			got := binary.BigEndian.Uint32(p.crcBuf[:])
			var c uint32
			if p.accumulating {
				bodyEnd := uint32(p.keyLen) + p.valLen
				c = crc32.ChecksumIEEE(p.bodyBuf[:bodyEnd])
			} else {
				c = crc32.Update(0, ieeeTab, fr.Key)
				if p.valLen > 0 {
					c = crc32.Update(c, ieeeTab, fr.Val)
				}
			}
			if c != got {
				p.Reset()
				return Frame{}, pos, ErrCRCMismatch
			}
			fr.CRC32 = c
			p.state = stateDone

		case stateDone:
			fr.Cmd = p.cmd
			fr.Flags = p.flags
			fr.KeyLen = p.keyLen
			fr.ValLen = p.valLen
			if p.accumulating {
				bodyEnd := uint32(p.keyLen) + p.valLen
				fr.Key = p.bodyBuf[:p.keyLen:p.keyLen]
				fr.Val = p.bodyBuf[p.keyLen:bodyEnd:bodyEnd]
			}
			consumed := pos
			p.Reset()
			return fr, consumed, nil
		}
	}
}
