// Package transport owns the shared-nothing shard manager and the
// in-process CacheService implementation.  It depends on
// internal/transport/api for the public Transport interface; this
// keeps the import graph acyclic when tcp and quic sub-packages both
// depend on api.
package transport

import (
	"encoding/binary"
	"errors"
	"hash/fnv"
	"net"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/horreum/horreum/internal/arena"
	"github.com/horreum/horreum/internal/compress"
	"github.com/horreum/horreum/internal/encrypt"
	"github.com/horreum/horreum/internal/eviction"
	"github.com/horreum/horreum/internal/transport/api"
)

// Public type aliases so callers can use transport.CacheService
// without importing api directly.
type (
	CacheService = api.CacheService
	ShardRouter  = api.ShardRouter
)

// Sentinel errors.
var (
	ErrNotFound = api.ErrNotFound
	ErrTooLarge = api.ErrTooLarge
)

// errBackendNil is internal.
var errBackendNil = errors.New("transport: shardCache is nil")

// shardCache is one shared-nothing cache instance.
type shardCache struct {
	mgr    *arena.Manager
	ev     *eviction.Eviction
	idx    *shardIndex
	comp   compress.Compressor // nil treated as Noop
	cipher encrypt.Cipher      // nil treated as Noop
}

// Compile-time check.
var _ CacheService = (*shardCache)(nil)

// NewShardCache creates a cache shard with the given arena region size
// and eviction capacity.  comp and cipher may be nil (no compression/encryption).
func NewShardCache(regionSize, evictCapacity uint64, comp compress.Compressor, cipher encrypt.Cipher) (*shardCache, error) {
	if regionSize == 0 {
		regionSize = 64 << 20
	}
	if evictCapacity == 0 {
		evictCapacity = 1024
	}
	if comp == nil {
		comp = compress.Noop{}
	}
	if cipher == nil {
		cipher = encrypt.Noop{}
	}
	mgr, err := arena.NewManager(regionSize, false)
	if err != nil {
		return nil, err
	}
	return &shardCache{
		mgr:    mgr,
		ev:     eviction.New(mgr, evictCapacity),
		idx:    newShardIndex(),
		comp:   comp,
		cipher: cipher,
	}, nil
}

// newShardCacheFromArena wraps an existing arena.Manager instead of
// allocating a new region.  Used by the persistent-mode ShardSet.
func newShardCacheFromArena(mgr *arena.Manager, evictCapacity uint64, comp compress.Compressor, cipher encrypt.Cipher) *shardCache {
	if evictCapacity == 0 {
		evictCapacity = 1024
	}
	if comp == nil {
		comp = compress.Noop{}
	}
	if cipher == nil {
		cipher = encrypt.Noop{}
	}
	return &shardCache{
		mgr:    mgr,
		ev:     eviction.New(mgr, evictCapacity),
		idx:    newShardIndex(),
		comp:   comp,
		cipher: cipher,
	}
}

// Set implements CacheService.
// If compression is enabled and the value exceeds the minimum
// threshold, the value is compressed before storing in the arena.
// The CompressedFlag meta bit is set so Get knows to decompress.
func (s *shardCache) Set(key, value []byte, ttlSeconds uint32) ([]byte, error) {
	if s == nil {
		return nil, errBackendNil
	}
	if uint64(len(value)) > arena.MaxObjectSize {
		return nil, ErrTooLarge
	}

	// 1. Try compression.
	storeVal := value
	compressed := false
	if cBuf, ok := s.comp.Compress(value); ok {
		storeVal = cBuf
		compressed = true
		// cBuf is a pool buffer; we copy into the arena below,
		// then return it.
		defer s.comp.PutBuf(cBuf)
	}

	// 2. Try encryption.
	encrypted := false
	if s.cipher != nil && s.cipher.Name() != "none" {
		encBuf, err := s.cipher.Encrypt(nil, storeVal)
		if err != nil {
			return nil, err
		}
		storeVal = encBuf
		encrypted = true
	}

	h, err := s.mgr.Put(storeVal)
	if err != nil {
		return nil, err
	}
	if compressed {
		s.mgr.OrMetaBits(h, arena.CompressedFlag)
	}
	if encrypted {
		s.mgr.OrMetaBits(h, arena.EncryptedFlag)
	}
	for _, e := range s.ev.Add(key, h) {
		_ = s.mgr.Free(e)
	}
	
	expiresAt := uint32(0)
	if ttlSeconds > 0 {
		expiresAt = uint32(time.Now().Unix()) + ttlSeconds
	}
	old, replaced := s.idx.put(key, h, expiresAt)
	if replaced {
		_ = s.mgr.Free(old)
	}
	// Return the original (uncompressed, unencrypted) value to the caller.
	if compressed || encrypted {
		// Make a copy of the original value — the caller expects
		// to read the value it just set.
		out := make([]byte, len(value))
		copy(out, value)
		return out, nil
	}
	return s.mgr.View(h)
}

// Get implements CacheService.  Touch on hit updates the S3-FIFO freq.
//
// If the stored value has the EncryptedFlag set, it is decrypted.
// If the stored value has the CompressedFlag set, it is decompressed.
func (s *shardCache) Get(key []byte) ([]byte, error) {
	if s == nil {
		return nil, errBackendNil
	}
	now := uint32(time.Now().Unix())
	h, ok, _ := s.idx.get(key, now)
	if !ok {
		return nil, ErrNotFound
	}
	s.ev.Touch(h)
	raw, err := s.mgr.View(h)
	if err != nil {
		return nil, err
	}

	// 1. Decrypt if the encrypted flag is set.
	if s.mgr.GetMeta(h)&arena.EncryptedFlag != 0 {
		if s.cipher == nil || s.cipher.Name() == "none" {
			return nil, errors.New("transport: data is encrypted but no cipher key is configured")
		}
		dec, err := s.cipher.Decrypt(nil, raw)
		if err != nil {
			return nil, err
		}
		raw = dec
	}

	// 2. Decompress if the compressed flag is set.
	if s.mgr.GetMeta(h)&arena.CompressedFlag != 0 {
		dec, err := s.comp.Decompress(raw)
		if err != nil {
			return nil, err
		}
		return dec, nil
	}
	return raw, nil
}

// CAS implements CacheService.
func (s *shardCache) CAS(key, expectedValue, newValue []byte) ([]byte, bool, error) {
	if s == nil {
		return nil, false, errBackendNil
	}
	now := uint32(time.Now().Unix())
	h, ok, expiresAt := s.idx.get(key, now)
	if !ok {
		return nil, false, nil
	}
	s.ev.Touch(h)
	raw, err := s.mgr.View(h)
	if err != nil {
		return nil, false, err
	}
	// For simplicity, we compare raw byte values directly. If they were compressed or encrypted,
	// we compare the uncompressed/unencrypted expectedValue against the decrypted/decompressed raw.
	currentValue := raw
	isEncrypted := s.mgr.GetMeta(h)&arena.EncryptedFlag != 0
	isCompressed := s.mgr.GetMeta(h)&arena.CompressedFlag != 0
	if isEncrypted {
		if s.cipher == nil || s.cipher.Name() == "none" {
			return nil, false, errors.New("transport: data is encrypted but no cipher key is configured")
		}
		dec, err := s.cipher.Decrypt(nil, currentValue)
		if err != nil {
			return nil, false, err
		}
		currentValue = dec
	}
	if isCompressed {
		dec, err := s.comp.Decompress(currentValue)
		if err != nil {
			return nil, false, err
		}
		currentValue = dec
	}

	if !bytesEq(currentValue, expectedValue) {
		// Mismatch, return current value
		out := make([]byte, len(currentValue))
		copy(out, currentValue)
		return out, false, nil
	}

	// Prepare new value
	if uint64(len(newValue)) > arena.MaxObjectSize {
		return nil, false, ErrTooLarge
	}
	valToWrite := newValue
	compressed := false
	if s.comp != nil && s.comp.Name() != "none" {
		if cBuf, ok := s.comp.Compress(valToWrite); ok {
			valToWrite = cBuf
			compressed = true
			defer s.comp.PutBuf(cBuf)
		}
	}
	encrypted := false
	if s.cipher != nil && s.cipher.Name() != "none" {
		encBuf, err := s.cipher.Encrypt(nil, valToWrite)
		if err != nil {
			return nil, false, err
		}
		valToWrite = encBuf
		encrypted = true
	}
	if uint64(len(valToWrite)) > arena.MaxObjectSize {
		return nil, false, ErrTooLarge
	}
	newH, err := s.mgr.Put(valToWrite)
	if err != nil {
		return nil, false, err
	}
	if compressed {
		s.mgr.OrMetaBits(newH, arena.CompressedFlag)
	}
	if encrypted {
		s.mgr.OrMetaBits(newH, arena.EncryptedFlag)
	}
	
	for _, e := range s.ev.Add(key, newH) {
		_ = s.mgr.Free(e)
	}
	old, replaced := s.idx.put(key, newH, expiresAt)
	if replaced {
		_ = s.mgr.Free(old)
	}
	return nil, true, nil
}

// Incr implements CacheService.
func (s *shardCache) Incr(key []byte, delta int64) (int64, error) {
	if s == nil {
		return 0, errBackendNil
	}
	now := uint32(time.Now().Unix())
	h, ok, expiresAt := s.idx.get(key, now)
	var current int64 = 0
	if ok {
		s.ev.Touch(h)
		raw, err := s.mgr.View(h)
		if err == nil {
			if s.mgr.GetMeta(h)&arena.EncryptedFlag != 0 && s.cipher != nil && s.cipher.Name() != "none" {
				if dec, err := s.cipher.Decrypt(nil, raw); err == nil {
					raw = dec
				}
			}
			if s.mgr.GetMeta(h)&arena.CompressedFlag != 0 {
				if dec, err := s.comp.Decompress(raw); err == nil {
					raw = dec
				}
			}
			if len(raw) == 8 {
				current = int64(binary.LittleEndian.Uint64(raw))
			}
		}
	}

	current += delta
	var newValue [8]byte
	binary.LittleEndian.PutUint64(newValue[:], uint64(current))

	valToWrite := newValue[:]
	compressed := false
	if s.comp != nil && s.comp.Name() != "none" {
		if cBuf, ok := s.comp.Compress(valToWrite); ok {
			valToWrite = cBuf
			compressed = true
			defer s.comp.PutBuf(cBuf)
		}
	}
	encrypted := false
	if s.cipher != nil && s.cipher.Name() != "none" {
		encBuf, err := s.cipher.Encrypt(nil, valToWrite)
		if err != nil {
			return 0, err
		}
		valToWrite = encBuf
		encrypted = true
	}
	if uint64(len(valToWrite)) > arena.MaxObjectSize {
		return 0, ErrTooLarge
	}
	newH, err := s.mgr.Put(valToWrite)
	if err != nil {
		return 0, err
	}
	if compressed {
		s.mgr.OrMetaBits(newH, arena.CompressedFlag)
	}
	if encrypted {
		s.mgr.OrMetaBits(newH, arena.EncryptedFlag)
	}
	
	for _, e := range s.ev.Add(key, newH) {
		_ = s.mgr.Free(e)
	}
	old, replaced := s.idx.put(key, newH, expiresAt)
	if replaced {
		_ = s.mgr.Free(old)
	}
	return current, nil
}

// Delete implements CacheService.
func (s *shardCache) Delete(key []byte) error {
	if s == nil {
		return errBackendNil
	}
	h, ok := s.idx.delete(key)
	if !ok {
		return nil
	}
	s.ev.Delete(key, h)
	return s.mgr.Free(h)
}

// DeleteExpired implements CacheService.
func (s *shardCache) DeleteExpired(limit int) error {
	if s == nil {
		return errBackendNil
	}
	now := uint32(time.Now().Unix())
	freed := s.idx.DeleteExpired(now, limit)
	for _, h := range freed {
		s.ev.Remove(h)
		_ = s.mgr.Free(h)
	}
	return nil
}

// Scan implements CacheService.
func (s *shardCache) Scan(prefix []byte, cursor uint64, count int) ([][]byte, uint64, error) {
	if s == nil {
		return nil, 0, errBackendNil
	}
	now := uint32(time.Now().Unix())
	keys, nextCursor := s.idx.scanPrefix(prefix, cursor, count, now)
	return keys, nextCursor, nil
}

// DelPrefix implements CacheService.
func (s *shardCache) DelPrefix(prefix []byte) (uint64, error) {
	if s == nil {
		return 0, errBackendNil
	}
	now := uint32(time.Now().Unix())
	freed, deletedKeys := s.idx.deletePrefix(prefix, now)
	for i, h := range freed {
		s.ev.Delete(deletedKeys[i], h)
		_ = s.mgr.Free(h)
	}
	return uint64(len(freed)), nil
}

// Close releases the arena region.
func (s *shardCache) Close() error {
	if s == nil {
		return nil
	}
	return s.mgr.Close()
}

// shardIndex is a minimal key→handle hash index.  Not concurrent-safe.
type shardIndex struct {
	keys       [][]byte
	handles    []arena.Handle
	expiresAt  []uint32
	count      int
	mask       uint64
	scanCursor uint64
}

func newShardIndex() *shardIndex {
	const initial = 64
	return &shardIndex{
		keys:      make([][]byte, initial),
		handles:   make([]arena.Handle, initial),
		expiresAt: make([]uint32, initial),
		mask:      initial - 1,
	}
}

func (s *shardIndex) put(key []byte, h arena.Handle, expiresAt uint32) (arena.Handle, bool) {
	if s.count*4 >= len(s.keys)*3 {
		s.grow()
	}
	i := hashKey(key) & s.mask
	for {
		if s.keys[i] == nil {
			s.keys[i] = key
			s.handles[i] = h
			s.expiresAt[i] = expiresAt
			s.count++
			return arena.Handle{}, false
		}
		if bytesEq(s.keys[i], key) {
			old := s.handles[i]
			s.handles[i] = h
			s.expiresAt[i] = expiresAt
			return old, true
		}
		i = (i + 1) & s.mask
	}
}

func (s *shardIndex) get(key []byte, now uint32) (arena.Handle, bool, uint32) {
	if s.count == 0 {
		return arena.Handle{}, false, 0
	}
	i := hashKey(key) & s.mask
	for {
		if s.keys[i] == nil {
			return arena.Handle{}, false, 0
		}
		if bytesEq(s.keys[i], key) {
			if s.expiresAt[i] != 0 && now >= s.expiresAt[i] {
				return arena.Handle{}, false, 0
			}
			return s.handles[i], true, s.expiresAt[i]
		}
		i = (i + 1) & s.mask
	}
}

func (s *shardIndex) deleteAt(i uint64) {
	s.keys[i] = nil
	s.handles[i] = arena.Handle{}
	s.expiresAt[i] = 0
	s.count--

	gap := i
	curr := (i + 1) & s.mask
	for s.keys[curr] != nil {
		desired := hashKey(s.keys[curr]) & s.mask
		if (curr > gap && (desired <= gap || desired > curr)) ||
			(curr < gap && (desired <= gap && desired > curr)) {
			s.keys[gap] = s.keys[curr]
			s.handles[gap] = s.handles[curr]
			s.expiresAt[gap] = s.expiresAt[curr]

			s.keys[curr] = nil
			s.handles[curr] = arena.Handle{}
			s.expiresAt[curr] = 0
			gap = curr
		}
		curr = (curr + 1) & s.mask
	}
}

func (s *shardIndex) delete(key []byte) (arena.Handle, bool) {
	if s.count == 0 {
		return arena.Handle{}, false
	}
	i := hashKey(key) & s.mask
	for {
		if s.keys[i] == nil {
			return arena.Handle{}, false
		}
		if bytesEq(s.keys[i], key) {
			old := s.handles[i]
			s.deleteAt(i)
			return old, true
		}
		i = (i + 1) & s.mask
	}
}

func (s *shardIndex) grow() {
	oldKeys := s.keys
	oldHandles := s.handles
	oldExpires := s.expiresAt
	nc := uint64(len(oldKeys)) * 2
	s.keys = make([][]byte, nc)
	s.handles = make([]arena.Handle, nc)
	s.expiresAt = make([]uint32, nc)
	s.mask = nc - 1
	s.count = 0
	s.scanCursor = 0
	for i, k := range oldKeys {
		if k != nil {
			s.put(k, oldHandles[i], oldExpires[i])
		}
	}
}

func (s *shardIndex) DeleteExpired(now uint32, limit int) []arena.Handle {
	if s.count == 0 || limit <= 0 {
		return nil
	}
	var freed []arena.Handle
	scanned := 0
	for scanned < limit {
		idx := s.scanCursor
		s.scanCursor = (s.scanCursor + 1) & s.mask
		scanned++

		if s.keys[idx] == nil {
			continue
		}
		if s.expiresAt[idx] != 0 && now >= s.expiresAt[idx] {
			freed = append(freed, s.handles[idx])
			s.deleteAt(idx)
		}
	}
	return freed
}

func (s *shardIndex) scanPrefix(prefix []byte, cursor uint64, limit int, now uint32) ([][]byte, uint64) {
	if s.count == 0 || limit <= 0 {
		return nil, 0
	}
	cap := uint64(len(s.keys))
	if cursor >= cap {
		return nil, 0
	}

	var results [][]byte
	idx := cursor

	for idx < cap {
		if s.keys[idx] != nil {
			if s.expiresAt[idx] == 0 || now < s.expiresAt[idx] {
				if len(prefix) == 0 || bytesHasPrefix(s.keys[idx], prefix) {
					results = append(results, s.keys[idx])
					if len(results) >= limit {
						next := idx + 1
						if next >= cap {
							next = 0
						}
						return results, next
					}
				}
			}
		}
		idx++
	}

	return results, 0
}

func (s *shardIndex) deletePrefix(prefix []byte, now uint32) ([]arena.Handle, [][]byte) {
	if s.count == 0 {
		return nil, nil
	}
	var freed []arena.Handle
	var deletedKeys [][]byte
	idx := uint64(0)
	cap := uint64(len(s.keys))

	for idx < cap {
		if s.keys[idx] != nil {
			if len(prefix) == 0 || bytesHasPrefix(s.keys[idx], prefix) {
				freed = append(freed, s.handles[idx])
				deletedKeys = append(deletedKeys, s.keys[idx])
				s.deleteAt(idx)
				continue
			}
		}
		idx++
	}
	return freed, deletedKeys
}

func bytesHasPrefix(s, prefix []byte) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := range prefix {
		if s[i] != prefix[i] {
			return false
		}
	}
	return true
}

func bytesEq(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// hashKey is FNV-1a 64.
func hashKey(key []byte) uint64 {
	const (
		offset uint64 = 14695981039346656037
		prime  uint64 = 1099511628211
	)
	h := offset
	for _, b := range key {
		h ^= uint64(b)
		h *= prime
	}
	return h
}

// ──────────────────────────────────────────────────────────────────────
// ShardSet — routes keys to shards and exposes CacheFor.
// ──────────────────────────────────────────────────────────────────────

// ShardConfig configures a ShardSet.
type ShardConfig struct {
	// NumShards is the number of shards.  Zero means
	// DefaultShardCount (4 × NumCPU, bounded 4..64).
	NumShards int
	// RegionSize is the arena region size per shard in bytes.
	// Zero means 64 MiB.
	RegionSize uint64
	// EvictCapacity is the S3-FIFO eviction capacity per shard
	// (number of objects).
	EvictCapacity uint64
	// Compressor is the optional value compressor.  nil means no
	// compression (Noop).  Shared across all shards.
	Compressor compress.Compressor
	// Cipher is the optional value cipher.  nil means no
	// encryption (Noop).  Shared across all shards.
	Cipher encrypt.Cipher
}

// DefaultShardCount returns 4 × NumCPU, clamped to [4, 64].
func DefaultShardCount() int {
	n := runtime.NumCPU() * 4
	if n < 4 {
		n = 4
	}
	if n > 64 {
		n = 64
	}
	return n
}

// ShardSet is a shared-nothing collection of cache shards.  CacheFor
// routes a key to a shard via FNV-1a(key) modulo NumShards.
type ShardSet struct {
	shards []*shardCache
	closed atomic.Bool
}

// Compile-time check.
var _ ShardRouter = (*ShardSet)(nil)

// NewShardSet creates N independent shards.
func NewShardSet(cfg ShardConfig) (*ShardSet, error) {
	if cfg.NumShards == 0 {
		cfg.NumShards = DefaultShardCount()
	}
	ss := &ShardSet{shards: make([]*shardCache, cfg.NumShards)}
	for i := 0; i < cfg.NumShards; i++ {
		c, err := NewShardCache(cfg.RegionSize, cfg.EvictCapacity, cfg.Compressor, cfg.Cipher)
		if err != nil {
			_ = ss.Close()
			return nil, err
		}
		ss.shards[i] = c
	}
	return ss, nil
}

// CacheFor returns the cache whose shard owns key.
func (ss *ShardSet) CacheFor(key []byte) CacheService {
	if ss == nil || len(ss.shards) == 0 {
		return nil
	}
	return ss.shards[hashKey(key)%uint64(len(ss.shards))]
}

// CacheIndexFor returns the shard index that owns key.
func (ss *ShardSet) CacheIndexFor(key []byte) int {
	if ss == nil || len(ss.shards) == 0 {
		return 0
	}
	return int(hashKey(key) % uint64(len(ss.shards)))
}

// ShardCount returns the number of shards.
func (ss *ShardSet) ShardCount() int {
	if ss == nil {
		return 0
	}
	return len(ss.shards)
}

// CacheForShard returns the cache for a specific shard index.
func (ss *ShardSet) CacheForShard(idx int) CacheService {
	if ss == nil || idx < 0 || idx >= len(ss.shards) {
		return nil
	}
	return ss.shards[idx]
}

// Scan iterates matching keys across shards. Upper 16 bits of cursor = shard index, lower 48 bits = bucket index inside shard.
func (ss *ShardSet) Scan(prefix []byte, cursor uint64, count int) ([][]byte, uint64, error) {
	if ss == nil || len(ss.shards) == 0 {
		return nil, 0, nil
	}
	shardIdx := int(cursor >> 48)
	innerCursor := cursor & 0x0000FFFFFFFFFFFF

	if shardIdx >= len(ss.shards) {
		return nil, 0, nil
	}

	keys, nextInner, err := ss.shards[shardIdx].Scan(prefix, innerCursor, count)
	if err != nil {
		return nil, 0, err
	}

	var nextCursor uint64
	if nextInner == 0 {
		nextShard := shardIdx + 1
		if nextShard >= len(ss.shards) {
			nextCursor = 0
		} else {
			nextCursor = uint64(nextShard) << 48
		}
	} else {
		nextCursor = (uint64(shardIdx) << 48) | nextInner
	}

	return keys, nextCursor, nil
}

// DelPrefix deletes matching keys across all shards and returns total deleted count.
func (ss *ShardSet) DelPrefix(prefix []byte) (uint64, error) {
	if ss == nil || len(ss.shards) == 0 {
		return 0, nil
	}
	var total uint64
	for _, shard := range ss.shards {
		n, err := shard.DelPrefix(prefix)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// StartGCLoop starts a background goroutine that periodically submits
// DeleteExpired jobs to the provided WorkerPool.
func (ss *ShardSet) StartGCLoop(workers *WorkerPool, limit int) {
	if ss == nil || len(ss.shards) == 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if ss.closed.Load() {
					return
				}
				for i := 0; i < len(ss.shards); i++ {
					idx := i
					workers.Submit(Job{
						Handle: func(canceled bool) {
							if !canceled && !ss.closed.Load() {
								_ = ss.shards[idx].DeleteExpired(limit)
							}
						},
					}, idx)
				}
			}
		}
	}()
}

// PickByAddr returns the shard index for a remote address.  Used by
// transports that pin connections by source IP.
func (ss *ShardSet) PickByAddr(addr net.Addr) int {
	if ss == nil || len(ss.shards) == 0 {
		return 0
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		host = addr.String()
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(host))
	return int(h.Sum64() % uint64(len(ss.shards)))
}

// Close releases every shard's arena region.
func (ss *ShardSet) Close() error {
	if ss == nil {
		return nil
	}
	if !ss.closed.CompareAndSwap(false, true) {
		return nil
	}
	for _, s := range ss.shards {
		_ = s.Close()
	}
	return nil
}

// Stats returns aggregated arena stats across all shards.
func (ss *ShardSet) Stats() arena.Stats {
	if ss == nil {
		return arena.Stats{}
	}
	var out arena.Stats
	for _, s := range ss.shards {
		if s == nil {
			continue
		}
		// Each shardCache wraps an arena.Manager; we read its
		// Stats() directly.
		sss := s.mgr.Stats()
		out.UsedBytes += sss.UsedBytes
		out.FreeBytes += sss.FreeBytes
		out.LiveObjects += sss.LiveObjects
		out.FreelistLen += sss.FreelistLen
	}
	return out
}

// NewShardSetOrPanic is the same as NewShardSet but panics on
// failure.  Used by the CLI which has no recovery path.
func NewShardSetOrPanic(numShards int, regionSize, evictCap uint64, comp compress.Compressor, cipher encrypt.Cipher) *ShardSet {
	ss, err := NewShardSet(ShardConfig{
		NumShards:     numShards,
		RegionSize:    regionSize,
		EvictCapacity: evictCap,
		Compressor:    comp,
		Cipher:        cipher,
	})
	if err != nil {
		panic(err)
	}
	return ss
}

// NewShardSetFromArena wraps an existing arena.Manager in a ShardSet
// with a single shard.  The shard reuses the supplied arena instead
// of allocating its own region.
//
// Used by the persistent-mode path in cmd/horreum, which owns the
// arena Manager via PersistentManager and wants to feed it directly
// into the transport layer.
func NewShardSetFromArena(mgr *arena.Manager, numShards int, evictCap uint64, comp compress.Compressor, cipher encrypt.Cipher) *ShardSet {
	if numShards <= 0 {
		numShards = 1
	}
	if evictCap == 0 {
		evictCap = 1024
	}
	ss := &ShardSet{shards: make([]*shardCache, numShards)}
	for i := 0; i < numShards; i++ {
		// Wrap the shared arena Manager.  Each shard gets its own
		// shardIndex; the arena is shared.
		ss.shards[i] = newShardCacheFromArena(mgr, evictCap, comp, cipher)
	}
	return ss
}
