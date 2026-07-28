package proto

import (
	"encoding/binary"
	"hash/crc32"
)

// Encode writes a complete frame into dst and returns the populated slice.
//
// The returned slice is dst[:total]; no allocation occurs when
// cap(dst) >= total. If cap(dst) is insufficient, Encode returns
// ErrDstTooSmall and the original dst slice unchanged.
//
// When FlagCRC32 is set in flags, Encode appends a CRC32 (IEEE) over KEY+VAL.
// The CMD byte is stored as-is; callers are responsible for setting CMD to
// one of CmdGET/CmdSET/CmdDEL/CmdSTATS.
func Encode(dst []byte, cmd uint8, flags uint8, key, val []byte) ([]byte, error) {
	total := headerSize + len(key) + len(val)
	if flags&FlagCRC32 != 0 {
		total += crc32Size
	}
	if cap(dst) < total {
		return dst, ErrDstTooSmall
	}
	dst = dst[:total]

	// Header
	dst[0] = magicH
	dst[1] = magicR
	dst[2] = ver
	dst[3] = cmd
	dst[4] = flags
	binary.BigEndian.PutUint16(dst[5:7], uint16(len(key)))
	binary.BigEndian.PutUint32(dst[7:11], uint32(len(val)))
	// dst[11:15] reserved (zero from caller-provided buffer or previous use).
	dst[15] = crc8(dst[:15])

	// Body
	off := headerSize
	off += copy(dst[off:], key)
	off += copy(dst[off:], val)

	// Optional CRC32
	if flags&FlagCRC32 != 0 {
		c := crc32.ChecksumIEEE(dst[headerSize:off])
		binary.BigEndian.PutUint32(dst[off:off+crc32Size], c)
	}

	return dst, nil
}

// EncodeResponse writes a response frame (FlagResponse set, OK or NOK).
// When useCRC is true, FlagCRC32 is set and a trailing CRC32 is appended.
// Pass ok=false to signal failure; the FlagOK bit is cleared in that case.
func EncodeResponse(dst []byte, cmd uint8, ok bool, useCRC bool, key, val []byte) ([]byte, error) {
	flags := FlagResponse
	if ok {
		flags |= FlagOK
	}
	if useCRC {
		flags |= FlagCRC32
	}
	return Encode(dst, cmd, flags, key, val)
}
