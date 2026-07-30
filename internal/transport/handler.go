package transport

import (
	"encoding/binary"
	"time"

	"github.com/horreum/horreum/internal/proto"
	"github.com/horreum/horreum/internal/transport/api"
)

type noopRecorder struct{}

func (noopRecorder) ObserveSet(string, time.Duration)       {}
func (noopRecorder) ObserveGet(string, time.Duration)       {}
func (noopRecorder) ObserveDel(string, time.Duration)       {}
func (noopRecorder) ObserveCAS(string, time.Duration)       {}
func (noopRecorder) ObserveIncr(string, time.Duration)      {}
func (noopRecorder) ObserveScan(string, time.Duration)      {}
func (noopRecorder) ObserveDelPrefix(string, time.Duration) {}

// HandleFrame processes one frame on the shard that owns key and
// appends the response to *writeBuf.
//
// IMPORTANT: this function MUST only be called from the shard worker
// whose index matches the shard router's CacheFor(key) result.
func HandleFrame(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, workerIdx int) bool {
	if rec == nil {
		rec = noopRecorder{}
	}
	if fr.Op != proto.OpScan && fr.Op != proto.OpDelPrefix {
		if router.CacheIndexFor(fr.Key) != workerIdx {
			// Key belongs to a different shard. Reject to prevent data races.
			*writeBuf = proto.EncodeResponse(*writeBuf, fr.Op, 1, fr.Key, nil)
			return true
		}
	}

	t0 := time.Now()
	switch fr.Op {
	case proto.OpGet:
		return handleGet(router, fr, writeBuf, rec, t0)
	case proto.OpSet:
		return handleSet(router, fr, writeBuf, rec, t0)
	case proto.OpSetEx:
		return handleSetEx(router, fr, writeBuf, rec, t0)
	case proto.OpDel:
		return handleDel(router, fr, writeBuf, rec, t0)
	case proto.OpCAS:
		return handleCAS(router, fr, writeBuf, rec, t0)
	case proto.OpIncr:
		return handleIncr(router, fr, writeBuf, rec, t0)
	case proto.OpScan:
		return handleScan(router, fr, writeBuf, rec, t0)
	case proto.OpDelPrefix:
		return handleDelPrefix(router, fr, writeBuf, rec, t0)

	case proto.OpHSet:
		return handleHSet(router, fr, writeBuf, rec, t0)
	case proto.OpHGet:
		return handleHGet(router, fr, writeBuf, rec, t0)
	case proto.OpHDel:
		return handleHDel(router, fr, writeBuf, rec, t0)
	case proto.OpHGetAll:
		return handleHGetAll(router, fr, writeBuf, rec, t0)

	case proto.OpLPush:
		return handleLPush(router, fr, writeBuf, rec, t0)
	case proto.OpLPop:
		return handleLPop(router, fr, writeBuf, rec, t0)
	case proto.OpRPush:
		return handleRPush(router, fr, writeBuf, rec, t0)
	case proto.OpRPop:
		return handleRPop(router, fr, writeBuf, rec, t0)
	case proto.OpLLen:
		return handleLLen(router, fr, writeBuf, rec, t0)

	case proto.OpSAdd:
		return handleSAdd(router, fr, writeBuf, rec, t0)
	case proto.OpSRem:
		return handleSRem(router, fr, writeBuf, rec, t0)
	case proto.OpSIsMember:
		return handleSIsMember(router, fr, writeBuf, rec, t0)
	case proto.OpSMembers:
		return handleSMembers(router, fr, writeBuf, rec, t0)
	default:
		return false
	}
}

func handleGet(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	val, err := cache.Get(fr.Key)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpGet, 1, fr.Key, nil)
		rec.ObserveGet("err", time.Since(t0))
		return true
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpGet, 0, fr.Key, val)
	rec.ObserveGet("ok", time.Since(t0))
	return true
}

func handleSet(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	if _, err := cache.Set(fr.Key, fr.Value, 0); err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSet, 1, fr.Key, nil)
		rec.ObserveSet("err", time.Since(t0))
		return true
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSet, 0, fr.Key, nil)
	rec.ObserveSet("ok", time.Since(t0))
	return true
}

func handleSetEx(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	if len(fr.Value) < 4 {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSetEx, 1, fr.Key, nil)
		rec.ObserveSet("err", time.Since(t0))
		return true
	}
	ttl := binary.LittleEndian.Uint32(fr.Value[:4])
	val := fr.Value[4:]
	cache := router.CacheFor(fr.Key)
	if _, err := cache.Set(fr.Key, val, ttl); err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSetEx, 1, fr.Key, nil)
		rec.ObserveSet("err", time.Since(t0))
		return true
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSetEx, 0, fr.Key, nil)
	rec.ObserveSet("ok", time.Since(t0))
	return true
}

func handleDel(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	_ = cache.Delete(fr.Key)
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpDel, 0, fr.Key, nil)
	rec.ObserveDel("ok", time.Since(t0))
	return true
}

func handleCAS(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	if len(fr.Value) < 4 {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpCAS, 1, fr.Key, nil)
		rec.ObserveCAS("err", time.Since(t0))
		return true
	}
	expLen := int(binary.LittleEndian.Uint32(fr.Value[:4]))
	if 4+expLen > len(fr.Value) {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpCAS, 1, fr.Key, nil)
		rec.ObserveCAS("err", time.Since(t0))
		return true
	}
	expectedValue := fr.Value[4 : 4+expLen]
	newValue := fr.Value[4+expLen:]
	cache := router.CacheFor(fr.Key)
	current, swapped, err := cache.CAS(fr.Key, expectedValue, newValue)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpCAS, 1, fr.Key, nil)
		rec.ObserveCAS("err", time.Since(t0))
		return true
	}
	if !swapped {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpCAS, 1, fr.Key, current)
		rec.ObserveCAS("err", time.Since(t0))
		return true
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpCAS, 0, fr.Key, nil)
	rec.ObserveCAS("ok", time.Since(t0))
	return true
}

func handleIncr(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	if len(fr.Value) != 8 {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpIncr, 1, fr.Key, nil)
		rec.ObserveIncr("err", time.Since(t0))
		return true
	}
	delta := int64(binary.LittleEndian.Uint64(fr.Value))
	cache := router.CacheFor(fr.Key)
	newVal, err := cache.Incr(fr.Key, delta)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpIncr, 1, fr.Key, nil)
		rec.ObserveIncr("err", time.Since(t0))
		return true
	}
	var respBuf [8]byte
	binary.LittleEndian.PutUint64(respBuf[:], uint64(newVal))
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpIncr, 0, fr.Key, respBuf[:])
	rec.ObserveIncr("ok", time.Since(t0))
	return true
}

func handleScan(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	if len(fr.Value) < 12 {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpScan, 1, fr.Key, nil)
		rec.ObserveScan("err", time.Since(t0))
		return true
	}
	cursor := binary.LittleEndian.Uint64(fr.Value[:8])
	count := int(binary.LittleEndian.Uint32(fr.Value[8:12]))

	keys, nextCursor, err := router.Scan(fr.Key, cursor, count)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpScan, 1, fr.Key, nil)
		rec.ObserveScan("err", time.Since(t0))
		return true
	}

	payloadLen := 8 + 4
	for _, k := range keys {
		payloadLen += 2 + len(k)
	}
	respPayload := make([]byte, payloadLen)
	binary.LittleEndian.PutUint64(respPayload[:8], nextCursor)
	binary.LittleEndian.PutUint32(respPayload[8:12], uint32(len(keys)))

	off := 12
	for _, k := range keys {
		binary.LittleEndian.PutUint16(respPayload[off:off+2], uint16(len(k)))
		copy(respPayload[off+2:], k)
		off += 2 + len(k)
	}

	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpScan, 0, fr.Key, respPayload)
	rec.ObserveScan("ok", time.Since(t0))
	return true
}

func handleDelPrefix(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	deleted, err := router.DelPrefix(fr.Key)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpDelPrefix, 1, fr.Key, nil)
		rec.ObserveDelPrefix("err", time.Since(t0))
		return true
	}
	var respBuf [8]byte
	binary.LittleEndian.PutUint64(respBuf[:], deleted)
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpDelPrefix, 0, fr.Key, respBuf[:])
	rec.ObserveDelPrefix("ok", time.Since(t0))
	return true
}

func handleHSet(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	if len(fr.Value) < 2 {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpHSet, 1, fr.Key, nil)
		return true
	}
	flen := int(binary.LittleEndian.Uint16(fr.Value[:2]))
	if 2+flen > len(fr.Value) {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpHSet, 1, fr.Key, nil)
		return true
	}
	field := fr.Value[2 : 2+flen]
	val := fr.Value[2+flen:]
	cache := router.CacheFor(fr.Key)
	_, err := cache.HSet(fr.Key, field, val)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpHSet, 1, fr.Key, nil)
		return true
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpHSet, 0, fr.Key, nil)
	return true
}

func handleHGet(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	field := fr.Value
	cache := router.CacheFor(fr.Key)
	val, err := cache.HGet(fr.Key, field)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpHGet, 1, fr.Key, nil)
		return true
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpHGet, 0, fr.Key, val)
	return true
}

func handleHDel(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	field := fr.Value
	cache := router.CacheFor(fr.Key)
	deleted, err := cache.HDel(fr.Key, field)
	if err != nil || !deleted {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpHDel, 1, fr.Key, nil)
		return true
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpHDel, 0, fr.Key, nil)
	return true
}

func handleHGetAll(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	fields, values, err := cache.HGetAll(fr.Key)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpHGetAll, 1, fr.Key, nil)
		return true
	}
	payloadLen := 4
	for i := range fields {
		payloadLen += 2 + len(fields[i]) + 4 + len(values[i])
	}
	respPayload := make([]byte, payloadLen)
	binary.LittleEndian.PutUint32(respPayload[:4], uint32(len(fields)))
	off := 4
	for i := range fields {
		binary.LittleEndian.PutUint16(respPayload[off:off+2], uint16(len(fields[i])))
		copy(respPayload[off+2:], fields[i])
		off += 2 + len(fields[i])
		binary.LittleEndian.PutUint32(respPayload[off:off+4], uint32(len(values[i])))
		copy(respPayload[off+4:], values[i])
		off += 4 + len(values[i])
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpHGetAll, 0, fr.Key, respPayload)
	return true
}

func handleLPush(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	length, err := cache.LPush(fr.Key, fr.Value)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpLPush, 1, fr.Key, nil)
		return true
	}
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], length)
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpLPush, 0, fr.Key, buf[:])
	return true
}

func handleRPush(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	length, err := cache.RPush(fr.Key, fr.Value)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpRPush, 1, fr.Key, nil)
		return true
	}
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], length)
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpRPush, 0, fr.Key, buf[:])
	return true
}

func handleLPop(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	elem, err := cache.LPop(fr.Key)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpLPop, 1, fr.Key, nil)
		return true
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpLPop, 0, fr.Key, elem)
	return true
}

func handleRPop(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	elem, err := cache.RPop(fr.Key)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpRPop, 1, fr.Key, nil)
		return true
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpRPop, 0, fr.Key, elem)
	return true
}

func handleLLen(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	length, err := cache.LLen(fr.Key)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpLLen, 1, fr.Key, nil)
		return true
	}
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], length)
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpLLen, 0, fr.Key, buf[:])
	return true
}

func handleSAdd(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	added, err := cache.SAdd(fr.Key, fr.Value)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSAdd, 1, fr.Key, nil)
		return true
	}
	status := uint8(0)
	if !added {
		status = 1
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSAdd, status, fr.Key, nil)
	return true
}

func handleSRem(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	removed, err := cache.SRem(fr.Key, fr.Value)
	if err != nil || !removed {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSRem, 1, fr.Key, nil)
		return true
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSRem, 0, fr.Key, nil)
	return true
}

func handleSIsMember(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	isMember, err := cache.SIsMember(fr.Key, fr.Value)
	if err != nil || !isMember {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSIsMember, 1, fr.Key, nil)
		return true
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSIsMember, 0, fr.Key, nil)
	return true
}

func handleSMembers(router api.ShardRouter, fr *proto.Frame, writeBuf *[]byte, rec api.Recorder, t0 time.Time) bool {
	cache := router.CacheFor(fr.Key)
	members, err := cache.SMembers(fr.Key)
	if err != nil {
		*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSMembers, 1, fr.Key, nil)
		return true
	}
	payloadLen := 4
	for i := range members {
		payloadLen += 2 + len(members[i])
	}
	respPayload := make([]byte, payloadLen)
	binary.LittleEndian.PutUint32(respPayload[:4], uint32(len(members)))
	off := 4
	for i := range members {
		binary.LittleEndian.PutUint16(respPayload[off:off+2], uint16(len(members[i])))
		copy(respPayload[off+2:], members[i])
		off += 2 + len(members[i])
	}
	*writeBuf = proto.EncodeResponse(*writeBuf, proto.OpSMembers, 0, fr.Key, respPayload)
	return true
}
