package ds

import (
	"bytes"
	"encoding/binary"
)

// SAdd adds member to set. Layout: [Count:4] [ [MemberLen:2][Member] ... ]
func SAdd(raw []byte, member []byte) ([]byte, bool) {
	if len(raw) < 4 {
		newBuf := make([]byte, 4+2+len(member))
		binary.LittleEndian.PutUint32(newBuf[:4], 1)
		binary.LittleEndian.PutUint16(newBuf[4:6], uint16(len(member)))
		copy(newBuf[6:], member)
		return newBuf, true
	}

	count := binary.LittleEndian.Uint32(raw[:4])
	off := 4
	for i := uint32(0); i < count; i++ {
		if off+2 > len(raw) {
			break
		}
		mlen := int(binary.LittleEndian.Uint16(raw[off : off+2]))
		off += 2
		if off+mlen > len(raw) {
			break
		}
		m := raw[off : off+mlen]
		off += mlen

		if bytes.Equal(m, member) {
			return raw, false // Already exists
		}
	}

	newBuf := make([]byte, len(raw)+2+len(member))
	copy(newBuf, raw)
	binary.LittleEndian.PutUint32(newBuf[:4], count+1)
	appendOff := len(raw)
	binary.LittleEndian.PutUint16(newBuf[appendOff:appendOff+2], uint16(len(member)))
	copy(newBuf[appendOff+2:], member)
	return newBuf, true
}

// SRem removes member from set.
func SRem(raw []byte, member []byte) ([]byte, bool) {
	if len(raw) < 4 {
		return raw, false
	}
	count := binary.LittleEndian.Uint32(raw[:4])
	off := 4
	for i := uint32(0); i < count; i++ {
		startMemOff := off
		if off+2 > len(raw) {
			return raw, false
		}
		mlen := int(binary.LittleEndian.Uint16(raw[off : off+2]))
		off += 2
		if off+mlen > len(raw) {
			return raw, false
		}
		m := raw[off : off+mlen]
		endMemOff := off + mlen
		off = endMemOff

		if bytes.Equal(m, member) {
			if count == 1 {
				return nil, true
			}
			newBuf := make([]byte, len(raw)-(endMemOff-startMemOff))
			copy(newBuf[:startMemOff], raw[:startMemOff])
			copy(newBuf[startMemOff:], raw[endMemOff:])
			binary.LittleEndian.PutUint32(newBuf[:4], count-1)
			return newBuf, true
		}
	}
	return raw, false
}

// SIsMember checks if member exists in set.
func SIsMember(raw []byte, member []byte) bool {
	if len(raw) < 4 {
		return false
	}
	count := binary.LittleEndian.Uint32(raw[:4])
	off := 4
	for i := uint32(0); i < count; i++ {
		if off+2 > len(raw) {
			return false
		}
		mlen := int(binary.LittleEndian.Uint16(raw[off : off+2]))
		off += 2
		if off+mlen > len(raw) {
			return false
		}
		m := raw[off : off+mlen]
		off += mlen

		if bytes.Equal(m, member) {
			return true
		}
	}
	return false
}

// SMembers returns all members in set.
func SMembers(raw []byte) [][]byte {
	if len(raw) < 4 {
		return nil
	}
	count := binary.LittleEndian.Uint32(raw[:4])
	members := make([][]byte, 0, count)
	off := 4
	for i := uint32(0); i < count; i++ {
		if off+2 > len(raw) {
			break
		}
		mlen := int(binary.LittleEndian.Uint16(raw[off : off+2]))
		off += 2
		if off+mlen > len(raw) {
			break
		}
		m := raw[off : off+mlen]
		off += mlen
		members = append(members, m)
	}
	return members
}
