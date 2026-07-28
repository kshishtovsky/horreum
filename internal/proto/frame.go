package proto

import "errors"

// Wire-format constants for the Horreum binary protocol.
const (
	// magicH is the first byte of the magic prefix ("HR").
	magicH uint8 = 'H'
	// magicR is the second byte of the magic prefix.
	magicR uint8 = 'R'
	// ver is the protocol version implemented here.
	ver uint8 = 0x01
	// headerSize is the fixed header length (16 bytes).
	headerSize = 16
	// maxKeyLen bounds the wire-format KEY_LEN field at 255.
	maxKeyLen = 255
	// crc32Size is the trailing CRC32 length (4 bytes).
	crc32Size = 4
)

// Commands encoded in the CMD byte.
const (
	CmdGET   uint8 = 0x01
	CmdSET   uint8 = 0x02
	CmdDEL   uint8 = 0x03
	CmdSTATS uint8 = 0x04
)

// Frame flag bits (FLAGS byte).
const (
	// FlagCRC32 enables the trailing CRC32 over KEY+VALUE.
	FlagCRC32 uint8 = 1 << 0
	// FlagResponse marks a server-to-client response frame.
	FlagResponse uint8 = 1 << 1
	// FlagOK marks a successful response (NOK when absent).
	FlagOK uint8 = 1 << 2
)

// Parser errors. Callers compare via errors.Is.
var (
	// ErrBadMagic is returned when the magic prefix is not "HR".
	ErrBadMagic = errors.New("proto: bad magic")
	// ErrBadVersion is returned when the version byte is unsupported.
	ErrBadVersion = errors.New("proto: bad version")
	// ErrBadCmd is returned when CMD is outside the known command set.
	ErrBadCmd = errors.New("proto: bad cmd")
	// ErrKeyTooLong is returned when KEY_LEN exceeds 255.
	ErrKeyTooLong = errors.New("proto: key too long")
	// ErrValTooLarge is returned when VAL_LEN exceeds arena.MaxObjectSize.
	ErrValTooLarge = errors.New("proto: value too large")
	// ErrHdrCRC is returned when the header CRC8 fails to validate.
	ErrHdrCRC = errors.New("proto: header crc mismatch")
	// ErrCRCMismatch is returned when the trailing CRC32 fails to validate.
	ErrCRCMismatch = errors.New("proto: payload crc mismatch")
	// ErrDstTooSmall is returned when the destination buffer has insufficient capacity.
	ErrDstTooSmall = errors.New("proto: dst too small")
)

// Frame is a parsed protocol frame. Key and Val are zero-copy slices
// over the input buffer passed to Parser.Feed (or over the parser's
// internal accumulator when bytes arrive across multiple Feed calls).
type Frame struct {
	Cmd    uint8
	Flags  uint8
	KeyLen uint16
	ValLen uint32
	Key    []byte
	Val    []byte
	CRC32  uint32
}
