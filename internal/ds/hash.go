package ds

import (
	"bytes"
	"encoding/binary"
)

// HSet sets field in raw Hash payload. Layout: [Count:4] [ [FieldLen:2][Field][ValLen:4][Val] ... ]
func HSet(raw []byte, field, value []byte) ([]byte, bool) {
	if len(raw) < 4 {
		// New Hash payload
		hdr := make([]byte, 4+2+len(field)+4+len(value))
		binary.LittleEndian.PutUint32(hdr[:4], 1)
		binary.LittleEndian.PutUint16(hdr[4:6], uint16(len(field)))
		copy(hdr[6:6+len(field)], field)
		valOff := 6 + len(field)
		binary.LittleEndian.PutUint32(hdr[valOff:valOff+4], uint32(len(value)))
		copy(hdr[valOff+4:], value)
		return hdr, true
	}

	count := binary.LittleEndian.Uint32(raw[:4])
	off := 4
	for i := uint32(0); i < count; i++ {
		if off+2 > len(raw) {
			break
		}
		flen := int(binary.LittleEndian.Uint16(raw[off : off+2]))
		off += 2
		if off+flen > len(raw) {
			break
		}
		f := raw[off : off+flen]
		off += flen
		if off+4 > len(raw) {
			break
		}
		vlen := int(binary.LittleEndian.Uint32(raw[off : off+4]))
		off += 4
		if off+vlen > len(raw) {
			break
		}
		vOff := off
		off += vlen

		if bytes.Equal(f, field) {
			// Update existing field
			if vlen == len(value) {
				copy(raw[vOff:vOff+vlen], value)
				return raw, false
			}
			// Replace with new value length
			newBuf := make([]byte, 0, len(raw)-vlen+len(value))
			newBuf = append(newBuf, raw[:vOff-4]...)
			var lenBuf [4]byte
			binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(value)))
			newBuf = append(newBuf, lenBuf[:]...)
			newBuf = append(newBuf, value...)
			newBuf = append(newBuf, raw[vOff+vlen:]...)
			return newBuf, false
		}
	}

	// Append new field
	newBuf := make([]byte, len(raw)+2+len(field)+4+len(value))
	copy(newBuf, raw)
	binary.LittleEndian.PutUint32(newBuf[:4], count+1)
	appendOff := len(raw)
	binary.LittleEndian.PutUint16(newBuf[appendOff:appendOff+2], uint16(len(field)))
	copy(newBuf[appendOff+2:appendOff+2+len(field)], field)
	valOff := appendOff + 2 + len(field)
	binary.LittleEndian.PutUint32(newBuf[valOff:valOff+4], uint32(len(value)))
	copy(newBuf[valOff+4:], value)
	return newBuf, true
}

// HGet retrieves field from raw Hash payload.
func HGet(raw []byte, field []byte) ([]byte, bool) {
	if len(raw) < 4 {
		return nil, false
	}
	count := binary.LittleEndian.Uint32(raw[:4])
	off := 4
	for i := uint32(0); i < count; i++ {
		if off+2 > len(raw) {
			return nil, false
		}
		flen := int(binary.LittleEndian.Uint16(raw[off : off+2]))
		off += 2
		if off+flen > len(raw) {
			return nil, false
		}
		f := raw[off : off+flen]
		off += flen
		if off+4 > len(raw) {
			return nil, false
		}
		vlen := int(binary.LittleEndian.Uint32(raw[off : off+4]))
		off += 4
		if off+vlen > len(raw) {
			return nil, false
		}
		v := raw[off : off+vlen]
		off += vlen

		if bytes.Equal(f, field) {
			return v, true
		}
	}
	return nil, false
}

// HDel removes field from raw Hash payload.
func HDel(raw []byte, field []byte) ([]byte, bool) {
	if len(raw) < 4 {
		return raw, false
	}
	count := binary.LittleEndian.Uint32(raw[:4])
	off := 4
	for i := uint32(0); i < count; i++ {
		startFieldOff := off
		if off+2 > len(raw) {
			return raw, false
		}
		flen := int(binary.LittleEndian.Uint16(raw[off : off+2]))
		off += 2
		if off+flen > len(raw) {
			return raw, false
		}
		f := raw[off : off+flen]
		off += flen
		if off+4 > len(raw) {
			return raw, false
		}
		vlen := int(binary.LittleEndian.Uint32(raw[off : off+4]))
		off += 4
		if off+vlen > len(raw) {
			return raw, false
		}
		endValOff := off
		off += vlen

		if bytes.Equal(f, field) {
			if count == 1 {
				return nil, true
			}
			newBuf := make([]byte, len(raw)-(endValOff-startFieldOff))
			copy(newBuf[:startFieldOff], raw[:startFieldOff])
			copy(newBuf[startFieldOff:], raw[endValOff:])
			binary.LittleEndian.PutUint32(newBuf[:4], count-1)
			return newBuf, true
		}
	}
	return raw, false
}

// HGetAll returns all fields and values from raw Hash payload.
func HGetAll(raw []byte) (fields, values [][]byte) {
	if len(raw) < 4 {
		return nil, nil
	}
	count := binary.LittleEndian.Uint32(raw[:4])
	fields = make([][]byte, 0, count)
	values = make([][]byte, 0, count)
	off := 4
	for i := uint32(0); i < count; i++ {
		if off+2 > len(raw) {
			break
		}
		flen := int(binary.LittleEndian.Uint16(raw[off : off+2]))
		off += 2
		if off+flen > len(raw) {
			break
		}
		f := raw[off : off+flen]
		off += flen
		if off+4 > len(raw) {
			break
		}
		vlen := int(binary.LittleEndian.Uint32(raw[off : off+4]))
		off += 4
		if off+vlen > len(raw) {
			break
		}
		v := raw[off : off+vlen]
		off += vlen

		fields = append(fields, f)
		values = append(values, v)
	}
	return fields, values
}
