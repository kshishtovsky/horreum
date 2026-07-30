package ds

import (
	"encoding/binary"
)

// LPush prepends elem to list payload. Layout: [Count:4] [ [ElemLen:4][Elem] ... ]
func LPush(raw []byte, elem []byte) []byte {
	count := uint32(0)
	var body []byte
	if len(raw) >= 4 {
		count = binary.LittleEndian.Uint32(raw[:4])
		body = raw[4:]
	}
	newBuf := make([]byte, 4+4+len(elem)+len(body))
	binary.LittleEndian.PutUint32(newBuf[:4], count+1)
	binary.LittleEndian.PutUint32(newBuf[4:8], uint32(len(elem)))
	copy(newBuf[8:8+len(elem)], elem)
	copy(newBuf[8+len(elem):], body)
	return newBuf
}

// RPush appends elem to list payload.
func RPush(raw []byte, elem []byte) []byte {
	count := uint32(0)
	if len(raw) >= 4 {
		count = binary.LittleEndian.Uint32(raw[:4])
	}
	newBuf := make([]byte, len(raw)+4+len(elem))
	if len(raw) >= 4 {
		copy(newBuf, raw)
	} else {
		newBuf = newBuf[:4+4+len(elem)]
	}
	binary.LittleEndian.PutUint32(newBuf[:4], count+1)
	appendOff := len(raw)
	if len(raw) < 4 {
		appendOff = 4
	}
	binary.LittleEndian.PutUint32(newBuf[appendOff:appendOff+4], uint32(len(elem)))
	copy(newBuf[appendOff+4:], elem)
	return newBuf
}

// LPop removes and returns the first element of list.
func LPop(raw []byte) (newRaw []byte, popped []byte, ok bool) {
	if len(raw) < 8 {
		return raw, nil, false
	}
	count := binary.LittleEndian.Uint32(raw[:4])
	if count == 0 {
		return raw, nil, false
	}
	firstLen := int(binary.LittleEndian.Uint32(raw[4:8]))
	if 8+firstLen > len(raw) {
		return raw, nil, false
	}
	popped = raw[8 : 8+firstLen]
	if count == 1 {
		return nil, popped, true
	}

	remainingBody := raw[8+firstLen:]
	newRaw = make([]byte, 4+len(remainingBody))
	binary.LittleEndian.PutUint32(newRaw[:4], count-1)
	copy(newRaw[4:], remainingBody)
	return newRaw, popped, true
}

// RPop removes and returns the last element of list.
func RPop(raw []byte) (newRaw []byte, popped []byte, ok bool) {
	if len(raw) < 8 {
		return raw, nil, false
	}
	count := binary.LittleEndian.Uint32(raw[:4])
	if count == 0 {
		return raw, nil, false
	}
	off := 4
	var lastStartOff int
	var lastLen int

	for i := uint32(0); i < count; i++ {
		if off+4 > len(raw) {
			return raw, nil, false
		}
		lastStartOff = off
		lastLen = int(binary.LittleEndian.Uint32(raw[off : off+4]))
		off += 4
		if off+lastLen > len(raw) {
			return raw, nil, false
		}
		off += lastLen
	}

	popped = raw[lastStartOff+4 : lastStartOff+4+lastLen]
	if count == 1 {
		return nil, popped, true
	}

	newRaw = make([]byte, lastStartOff)
	copy(newRaw, raw[:lastStartOff])
	binary.LittleEndian.PutUint32(newRaw[:4], count-1)
	return newRaw, popped, true
}

// LLen returns number of elements in list.
func LLen(raw []byte) uint32 {
	if len(raw) < 4 {
		return 0
	}
	return binary.LittleEndian.Uint32(raw[:4])
}
